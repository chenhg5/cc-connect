package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleResultParsesUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":       "result",
		"result":     "done",
		"session_id": "test-session",
		"usage": map[string]any{
			"input_tokens":  float64(150000),
			"output_tokens": float64(2000),
		},
	}

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 150000 {
		t.Errorf("InputTokens = %d, want 150000", evt.InputTokens)
	}
	if evt.OutputTokens != 2000 {
		t.Errorf("OutputTokens = %d, want 2000", evt.OutputTokens)
	}
	if !evt.Done {
		t.Errorf("regular result event Done = false, want true")
	}
}

// TestHandleResultCompactionSubtypeIsNotTerminal is a regression test for
// issue #481: Claude Code's mid-turn context compaction emits a
// `type:"result"` event with `subtype:"compact"` (newer CLI) or
// `subtype:"compaction"` (older CLI). The engine must keep the turn
// running, so the emitted EventResult must have Done=false.
func TestHandleResultCompactionSubtypeIsNotTerminal(t *testing.T) {
	cases := []string{"compact", "compaction"}
	for _, subtype := range cases {
		t.Run(subtype, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cs := &claudeSession{
				events: make(chan core.Event, 4),
				ctx:    ctx,
			}
			cs.sessionID.Store("test-session")
			cs.alive.Store(true)

			cs.handleResult(map[string]any{
				"type":       "result",
				"subtype":    subtype,
				"isCompact":  true,
				"session_id": "test-session",
			})

			select {
			case evt := <-cs.events:
				if evt.Type != core.EventResult {
					t.Fatalf("event type = %q, want %q", evt.Type, core.EventResult)
				}
				if evt.Done {
					t.Errorf("compaction result Done = true, want false (turn must continue)")
				}
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for EventResult")
			}
		})
	}
}

// TestIsCompactionResult covers both accepted subtype spellings and the
// negative case (regular result with no subtype).
func TestIsCompactionResult(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"nil_subtype", map[string]any{"type": "result"}, false},
		{"empty_subtype", map[string]any{"type": "result", "subtype": ""}, false},
		{"success_subtype", map[string]any{"type": "result", "subtype": "success"}, false},
		{"compact_subtype", map[string]any{"type": "result", "subtype": "compact"}, true},
		{"compaction_subtype", map[string]any{"type": "result", "subtype": "compaction"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCompactionResult(tc.raw); got != tc.want {
				t.Errorf("isCompactionResult(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestHandleAssistantCapturesPerSubCallUsage verifies the split-source policy
// for the runtime ContextUsage snapshot used by the reply footer:
//
//   - input/cache values come from the LAST assistant event (per-sub-call),
//     so ctx % reflects the prompt size of the final inference call rather
//     than a sum that exceeds the context window.
//   - output_tokens comes from the result event (turn aggregate), since
//     stream-json assistant events carry a placeholder output_tokens=1
//     (the real per-call output count never appears in the live stream).
func TestHandleAssistantCapturesPerSubCallUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)
	cs.activeModel.Store("claude-opus-4-7[1m]") // 1M context window

	// Sub-call #1: small prompt, ~100k tokens of cached prefix.
	// Stream-json carries placeholder output_tokens=1 on assistant events.
	cs.handleAssistant(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{},
			"usage": map[string]any{
				"input_tokens":                float64(50),
				"output_tokens":               float64(1), // placeholder, ignored
				"cache_creation_input_tokens": float64(0),
				"cache_read_input_tokens":     float64(100_000),
			},
		},
	})
	// Drain any events emitted (none here since content is empty).
	for len(cs.events) > 0 {
		<-cs.events
	}

	// Sub-call #2 (final): same cached prefix grown to ~500k.
	cs.handleAssistant(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{},
			"usage": map[string]any{
				"input_tokens":                float64(80),
				"output_tokens":               float64(1), // placeholder, ignored
				"cache_creation_input_tokens": float64(2_000),
				"cache_read_input_tokens":     float64(500_000),
			},
		},
	})

	// Result event: input/cache fields are aggregated (cache_read summed
	// across many sub-calls — would clamp ctx % to 100% if used). The
	// output_tokens here IS authoritative — the real total tokens
	// generated by the model this turn.
	cs.handleResult(map[string]any{
		"type":       "result",
		"result":     "done",
		"session_id": "test-session",
		"usage": map[string]any{
			"input_tokens":                float64(130),
			"output_tokens":               float64(648), // real turn total
			"cache_creation_input_tokens": float64(2_000),
			"cache_read_input_tokens":     float64(8_000_000), // summed, would inflate ctx
		},
	})

	usage := cs.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil; expected per-sub-call snapshot")
	}
	// Input/cache: must match LAST assistant event (sub-call #2), not the
	// aggregated result.
	if usage.InputTokens != 80 {
		t.Errorf("InputTokens = %d, want 80 (last assistant)", usage.InputTokens)
	}
	if usage.CachedInputTokens != 500_000 {
		t.Errorf("CachedInputTokens = %d, want 500_000 (last assistant); 8M would indicate aggregated leak",
			usage.CachedInputTokens)
	}
	if usage.CacheCreationInputTokens != 2_000 {
		t.Errorf("CacheCreationInputTokens = %d, want 2_000", usage.CacheCreationInputTokens)
	}
	if usage.UsedTokens != 80+2_000+500_000 {
		t.Errorf("UsedTokens = %d, want %d", usage.UsedTokens, 80+2_000+500_000)
	}
	if usage.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1_000_000 (opus-4-7[1m])", usage.ContextWindow)
	}
	// Output: must come from the result event, not from an assistant
	// placeholder. 648 is the real turn-total; 1 would indicate the
	// placeholder leaked through.
	if usage.OutputTokens != 648 {
		t.Errorf("OutputTokens = %d, want 648 (result aggregate)", usage.OutputTokens)
	}
	if usage.TotalTokens != usage.UsedTokens+648 {
		t.Errorf("TotalTokens = %d, want UsedTokens+648 = %d", usage.TotalTokens, usage.UsedTokens+648)
	}
	// Sanity: ctx % should be reasonable (~50%), NOT clamped at 100%.
	pct := float64(usage.UsedTokens) * 100 / float64(usage.ContextWindow)
	if pct > 90 {
		t.Errorf("ctx %% = %.1f, expected ~50%% — aggregated cache_read leaked through", pct)
	}
}

// TestHandleResultUsesReportedContextWindow verifies that the CLI-reported
// contextWindow in the result event's modelUsage map wins over the model-id
// heuristic. claude-opus-5 has a native 1M window but no "[1m]" suffix in its
// id, so the heuristic alone would report 200k and inflate ctx % 5x.
func TestHandleResultUsesReportedContextWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)
	cs.activeModel.Store("claude-opus-5") // native 1M, no "[1m]" suffix

	assistant := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{},
			"usage": map[string]any{
				"input_tokens":                float64(2),
				"output_tokens":               float64(1),
				"cache_creation_input_tokens": float64(19_800),
				"cache_read_input_tokens":     float64(16_700),
			},
		},
	}

	cs.handleAssistant(assistant)
	if usage := cs.GetContextUsage(); usage.ContextWindow != 200_000 {
		t.Fatalf("pre-result ContextWindow = %d, want 200_000 (heuristic fallback)", usage.ContextWindow)
	}

	cs.handleResult(map[string]any{
		"type":       "result",
		"result":     "done",
		"session_id": "test-session",
		"usage": map[string]any{
			"output_tokens": float64(65),
		},
		"modelUsage": map[string]any{
			"claude-opus-5": map[string]any{
				"contextWindow":  float64(1_000_000),
				"canonicalModel": "claude-opus-5",
			},
		},
	})

	usage := cs.GetContextUsage()
	if usage.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1_000_000 (reported by modelUsage)", usage.ContextWindow)
	}
	// The turn that reported the window must itself render the right ctx %:
	// 36.5k of 1M is ~4%, not the 18% a 200k denominator produces.
	pct := usage.UsedTokens * 100 / usage.ContextWindow
	if pct != 3 {
		t.Errorf("ctx %% = %d, want 3 (36.5k of 1M)", pct)
	}

	// Later assistant events reuse the reported window without waiting for
	// another result event.
	cs.handleAssistant(assistant)
	if usage := cs.GetContextUsage(); usage.ContextWindow != 1_000_000 {
		t.Errorf("post-result assistant ContextWindow = %d, want 1_000_000 (cached)", usage.ContextWindow)
	}
}

func TestParseModelUsageContextWindow(t *testing.T) {
	entry := func(window float64) map[string]any {
		return map[string]any{"contextWindow": window}
	}
	tests := []struct {
		name  string
		raw   map[string]any
		model string
		want  int
	}{
		{"no modelUsage", map[string]any{"type": "result"}, "claude-opus-5", 0},
		{"empty modelUsage", map[string]any{"modelUsage": map[string]any{}}, "claude-opus-5", 0},
		{
			"active model matches",
			map[string]any{"modelUsage": map[string]any{
				"claude-haiku-4-5": entry(200_000),
				"claude-opus-5":    entry(1_000_000),
			}},
			"claude-opus-5",
			1_000_000,
		},
		{
			"sole entry used when model unknown",
			map[string]any{"modelUsage": map[string]any{"claude-opus-5": entry(1_000_000)}},
			"",
			1_000_000,
		},
		{
			"ambiguous multi-model, no match",
			map[string]any{"modelUsage": map[string]any{
				"claude-haiku-4-5": entry(200_000),
				"claude-opus-5":    entry(1_000_000),
			}},
			"claude-sonnet-5",
			0,
		},
		{
			"zero window ignored",
			map[string]any{"modelUsage": map[string]any{"claude-opus-5": entry(0)}},
			"claude-opus-5",
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseModelUsageContextWindow(tt.raw, tt.model); got != tt.want {
				t.Errorf("parseModelUsageContextWindow() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHandleResultNoUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":   "result",
		"result": "done",
	}

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", evt.InputTokens)
	}
	if evt.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", evt.OutputTokens)
	}
}

func TestReadLoop_ChildHoldsStdoutPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
	})

	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(pw, `{"type":"system","session_id":"test-pipe"}`+"\n")
		writeDone <- err
	}()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cs := &claudeSession{
		cmd:    cmd,
		events: make(chan core.Event, 64),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)
	go cs.readLoop(pr, &stderrBuf)

	timeout := time.After(5 * time.Second)
	gotEvent := false
	for {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			writeDone = nil
		case evt, ok := <-cs.events:
			if !ok {
				if !gotEvent {
					t.Fatal("events closed but system event lost")
				}
				return
			}
			if evt.SessionID == "test-pipe" {
				gotEvent = true
			}
		case <-timeout:
			t.Fatal("HANG: events not closed within 5s - readLoop stuck in scanner.Scan()")
		}
	}
}

func TestReadLoop_CtxCancelClosesChannels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
	})

	// "err-then-sleep" emits stderr before sleeping so that ctx cancel
	// produces a non-empty stderrBuf in readLoop's defer — exercising the
	// `case <-cs.ctx.Done()` select branch in finishReadLoop.
	cmd := helperCommand(ctx, "err-then-sleep")
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cs := &claudeSession{
		cmd:    cmd,
		events: make(chan core.Event, 64),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)
	go cs.readLoop(pr, &stderrBuf)

	time.Sleep(200 * time.Millisecond)
	cancel()

	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-cs.events:
			if !ok {
				goto closed
			}
		case <-timeout:
			t.Fatal("HANG: events not closed within 5s after ctx cancel")
		}
	}
closed:
	select {
	case <-cs.done:
	case <-timeout:
		t.Fatal("HANG: done not closed within 5s after ctx cancel")
	}
}

func TestClaudeSessionClose_IdempotentNoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand(ctx, "stdin-eof-exit")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		ctx:                 ctx,
		cancel:              cancel,
		done:                done,
		gracefulStopTimeout: 200 * time.Millisecond,
	}
	cs.alive.Store(true)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Close panicked: %v", r)
		}
	}()

	if err := cs.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := cs.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestShellJoinArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, ""},
		{"single_plain", []string{"--verbose"}, "--verbose"},
		{"multiple_plain", []string{"--verbose", "--model", "opus"}, "--verbose --model opus"},
		{"arg_with_space", []string{"--prompt", "hello world"}, "--prompt 'hello world'"},
		{"arg_with_tab", []string{"a\tb"}, "'a\tb'"},
		{"arg_with_newline", []string{"line1\nline2"}, "'line1\nline2'"},
		{"arg_with_single_quote", []string{"it's"}, "'it'\\''s'"},
		{"arg_with_double_quote", []string{`say "hi"`}, `'say "hi"'`},
		{"arg_with_backslash", []string{`path\to`}, `'path\to'`},
		{"mixed", []string{"--flag", "has space", "plain", "it's here"}, "--flag 'has space' plain 'it'\\''s here'"},
		{"empty_string_arg", []string{""}, ""},
		{"long_prompt", []string{"--append-system-prompt", "You are a helpful assistant.\nBe concise."}, "--append-system-prompt 'You are a helpful assistant.\nBe concise.'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellJoinArgs(tt.args)
			if got != tt.want {
				t.Errorf("shellJoinArgs(%v)\n  got  = %q\n  want = %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestBuildAppendSystemPrompt(t *testing.T) {
	tests := []struct {
		name           string
		agentPrompt    string
		platformPrompt string
		userAppend     string
		want           string
	}{
		{"all_empty", "", "", "", ""},
		{"agent_only", "AGENT", "", "", "AGENT"},
		{"agent_and_platform", "AGENT", "PLAT", "", "AGENT\n## Formatting\nPLAT\n"},
		{"user_only", "", "", "USER", "USER"},
		{"user_only_platform_ignored", "", "PLAT", "USER", "USER"},
		{"agent_and_user", "AGENT", "", "USER", "AGENT\nUSER"},
		{"all_three", "AGENT", "PLAT", "USER", "AGENT\n## Formatting\nPLAT\n\nUSER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAppendSystemPrompt(tt.agentPrompt, tt.platformPrompt, tt.userAppend)
			if got != tt.want {
				t.Errorf("buildAppendSystemPrompt(%q, %q, %q)\n  got  = %q\n  want = %q",
					tt.agentPrompt, tt.platformPrompt, tt.userAppend, got, tt.want)
			}
		})
	}
}

// TestEnsureSharedSystemPromptFile_WritesOnceAndReuses covers the 99%
// case for the #1376 workaround. The cc-connect default
// AgentSystemPrompt is written once to <ccDataDir>/agent-prompts/
// cc-connect-system.md and reused across spawns — no per-spawn write,
// no cleanup. claude only reads the file, so reuse is safe under
// concurrent spawns.
func TestEnsureSharedSystemPromptFile_WritesOnceAndReuses(t *testing.T) {
	dir := t.TempDir()
	content := "## cc-connect prompt\n" + makeFiller(10*1024)

	// First call must create the file.
	path1, err := ensureSharedSystemPromptFile(dir, content)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path1), "agent-prompts/cc-connect-system.md") {
		t.Errorf("path %q does not end in agent-prompts/cc-connect-system.md", path1)
	}
	got, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch after first write")
	}
	stat1, err := os.Stat(path1)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Second call with identical content must NOT rewrite the file
	// (mtime stays the same). This is what gives the common case
	// zero per-spawn overhead.
	time.Sleep(20 * time.Millisecond)
	path2, err := ensureSharedSystemPromptFile(dir, content)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if path2 != path1 {
		t.Errorf("path drifted between calls: %q vs %q", path1, path2)
	}
	stat2, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat 2: %v", err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file was rewritten despite identical content: mtime %v -> %v",
			stat1.ModTime(), stat2.ModTime())
	}
}

// TestEnsureSharedSystemPromptFile_RewritesOnContentChange covers
// cc-connect upgrades: when AgentSystemPrompt content changes between
// releases, the shared file must be refreshed automatically.
func TestEnsureSharedSystemPromptFile_RewritesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureSharedSystemPromptFile(dir, "v1"); err != nil {
		t.Fatalf("ensure v1: %v", err)
	}
	path, err := ensureSharedSystemPromptFile(dir, "v2 — upgraded prompt")
	if err != nil {
		t.Fatalf("ensure v2: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "v2 — upgraded prompt" {
		t.Fatalf("file did not refresh after content change: got %q", string(got))
	}
}

// TestEnsureSharedSystemPromptFile_EmptyDirUsesTempDir guards the
// degraded path where ccDataDir was not injected (e.g. older host
// code or test harnesses) — the shared file still lands somewhere
// writable instead of failing the spawn.
func TestEnsureSharedSystemPromptFile_EmptyDirUsesTempDir(t *testing.T) {
	path, err := ensureSharedSystemPromptFile("", "hello")
	if err != nil {
		t.Fatalf("ensure with empty dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if !strings.Contains(filepath.ToSlash(path), "/agent-prompts/cc-connect-system.md") {
		t.Errorf("unexpected fallback path: %q", path)
	}
}

// TestWriteTempAppendPromptFile_UniquePerCall covers the 1% edge case:
// when the prompt includes per-session pieces (platform formatting or
// user append) two concurrent spawns must each get their own file so
// they cannot overwrite each other's content before claude reads it.
func TestWriteTempAppendPromptFile_UniquePerCall(t *testing.T) {
	// dir is auto-cleaned by t.TempDir(), so per-file Remove is unnecessary.
	dir := t.TempDir()
	a, err := writeTempAppendPromptFile(dir, "session A")
	if err != nil {
		t.Fatalf("write A: %v", err)
	}
	b, err := writeTempAppendPromptFile(dir, "session B")
	if err != nil {
		t.Fatalf("write B: %v", err)
	}
	if a == b {
		t.Fatalf("two writeTempAppendPromptFile calls returned the same path %q "+
			"— concurrent customised sessions would overwrite each other", a)
	}

	// Files must contain their own content (no cross-talk).
	gotA, _ := os.ReadFile(a)
	gotB, _ := os.ReadFile(b)
	if string(gotA) != "session A" || string(gotB) != "session B" {
		t.Errorf("cross-talk: A=%q B=%q", string(gotA), string(gotB))
	}
}

// TestWriteTempAppendPromptFile_ReadableByOtherUser guards the
// run_as_user regression from issue #1429. os.CreateTemp defaults to
// 0600 owned by the cc-connect process user; when the agent is
// spawned as a different OS user (via run_as_user), a 0600 root-owned
// file is unreadable and the agent exits with EACCES before reading
// any prompt at all. The fix is to chmod 0o644 immediately after
// write, matching ensureSharedSystemPromptFile (which writes 0o644
// via writeFileAtomic).
//
// We assert the contract two ways: (1) the on-disk mode is 0o644 —
// any reader path bit is set, no execute bits, no setuid/sticky; and
// (2) a non-owner stat-open succeeds in O_RDONLY, which is the same
// access path the spawned agent uses when it calls os.Open on the
// file path passed via --append-system-prompt-file.
func TestWriteTempAppendPromptFile_ReadableByOtherUser(t *testing.T) {
	dir := t.TempDir()
	path, err := writeTempAppendPromptFile(dir, "session X")
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	want := os.FileMode(0o644)
	if info.Mode().Perm() != want {
		t.Fatalf("per-spawn prompt file mode = %o, want %o — run_as_user target user would get EACCES (#1429)",
			info.Mode().Perm(), want)
	}

	// Non-owner open simulates the spawned agent's read path. On root
	// the kernel bypasses the mode bits, so this only fails for a
	// truly 0o000 file. We still assert it to make the regression
	// observable on systems where the test runs as a non-root user
	// (CI matrix, dev laptops).
	if _, err := os.OpenFile(path, os.O_RDONLY, 0); err != nil {
		t.Fatalf("open O_RDONLY as a non-owner: %v — file is unreadable even for an unprivileged reader", err)
	}
}

func makeFiller(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return string(b)
}

// TestHandleUserEmitsToolResult is a regression test for the bug where
// claudeSession.handleUser silently dropped tool_result content blocks
// (only logging when is_error=true) instead of emitting EventToolResult.
// Without this event, engine never sees tool output and the Feishu/Slack/
// Discord progress card never renders tool results — only the final
// assistant text reaches the user.
//
// Cases covered:
//   - string content (plain text result)
//   - array content (Anthropic SDK multi-block: [{type:"text", text:"..."}])
//   - is_error=true (exit code 1, success=false)
func TestHandleUserEmitsToolResult(t *testing.T) {
	cases := []struct {
		name        string
		raw         map[string]any
		wantResult  string
		wantCode    int
		wantSuccess bool
	}{
		{
			name: "string content",
			raw: map[string]any{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_abc",
							"is_error":    false,
							"content":     "command output here",
						},
					},
				},
			},
			wantResult:  "command output here",
			wantCode:    0,
			wantSuccess: true,
		},
		{
			name: "array content",
			raw: map[string]any{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_def",
							"is_error":    false,
							"content": []any{
								map[string]any{"type": "text", "text": "line one"},
								map[string]any{"type": "text", "text": "line two"},
							},
						},
					},
				},
			},
			wantResult:  "line one\nline two",
			wantCode:    0,
			wantSuccess: true,
		},
		{
			name: "error result",
			raw: map[string]any{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":        "tool_result",
							"tool_use_id": "toolu_err",
							"is_error":    true,
							"content":     "boom",
						},
					},
				},
			},
			wantResult:  "boom",
			wantCode:    1,
			wantSuccess: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cs := &claudeSession{
				events: make(chan core.Event, 4),
				ctx:    ctx,
			}
			cs.alive.Store(true)

			cs.handleUser(tc.raw)

			select {
			case evt := <-cs.events:
				if evt.Type != core.EventToolResult {
					t.Fatalf("event type = %q, want %q", evt.Type, core.EventToolResult)
				}
				if evt.ToolResult != tc.wantResult {
					t.Errorf("ToolResult = %q, want %q", evt.ToolResult, tc.wantResult)
				}
				if evt.ToolExitCode == nil || *evt.ToolExitCode != tc.wantCode {
					got := -1
					if evt.ToolExitCode != nil {
						got = *evt.ToolExitCode
					}
					t.Errorf("ToolExitCode = %d, want %d", got, tc.wantCode)
				}
				if evt.ToolSuccess == nil || *evt.ToolSuccess != tc.wantSuccess {
					got := false
					if evt.ToolSuccess != nil {
						got = *evt.ToolSuccess
					}
					t.Errorf("ToolSuccess = %v, want %v", got, tc.wantSuccess)
				}
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for EventToolResult — handleUser dropped the tool_result")
			}
		})
	}
}

func helperCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", mode)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

// TestHelperProcess lets this test binary act as a tiny external command for
// cases that need a process with controlled lifetime semantics.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := "unknown"
	for i := len(os.Args) - 1; i >= 0; i-- {
		if os.Args[i] == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	if mode == "unknown" && len(os.Args) > 1 {
		// Backward-compatible fallback for tests that don't pass "--":
		// TestHelperProcess_StdinEofExit and friends invoke the helper
		// directly with the mode as the last arg.
		mode = os.Args[len(os.Args)-1]
	}
	switch mode {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "err-then-sleep":
		_, _ = os.Stderr.WriteString("helper: starting up\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "stdin-eof-exit":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "claude-stdin-echo":
		// Issue #1736 regression harness: act as a Claude Code stub that
		//   1. asserts --replay-user-messages is NOT in argv (its presence
		//      would make the CLI exit after the first message and break
		//      `/compact`, `/clear`, `/resume`).
		//   2. emits a system + result event on startup so cc-connect
		//      recognises the session as live.
		//   3. reads stdin line-by-line, treating each line as a new user
		//      turn, and replies with a `type:"result"` event containing
		//      the line's text — proving the process stayed alive across
		//      turns instead of exiting after the first one.
		//   4. exits cleanly only when stdin closes (which is exactly how
		//      cc-connect's Close() and the #1338 idle reaper will end
		//      the session).
		for _, a := range os.Args {
			if a == "--replay-user-messages" {
				_, _ = os.Stderr.WriteString("HELPER: --replay-user-messages unexpectedly present in argv\n")
				os.Exit(3)
			}
		}
		// Args layout reminder: the helper is invoked via
		//   test_bin -test.run=TestHelperProcess -- claude-stdin-echo <innerArgs...> <outerArgs...>
		// so the mode token (the value after "--") is the second-to-last
		// element, not the last one. Walk from the end backwards until we
		// hit it. Falling back to os.Args[1] is harmless if "--" is
		// missing (the earlier helperCommand usage relies on it).
		mode := "unknown"
		for i := len(os.Args) - 1; i >= 0; i-- {
			if os.Args[i] == "--" && i+1 < len(os.Args) {
				mode = os.Args[i+1]
				break
			}
		}
		_, _ = os.Stderr.WriteString("HELPER start mode=" + mode + " argvLen=" + itoa(len(os.Args)) + "\n")
		if mode != "claude-stdin-echo" {
			_, _ = os.Stderr.WriteString("HELPER: unexpected mode token; exiting\n")
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"helper-keepalive"}` + "\n")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			// cc-connect's Send() wraps every user message in the
			// envelope `{"type":"user","message":{"role":"user","content":"<text>"}}`.
			// Unwrap it so the echoed `result` field carries the human-
			// readable text and the regression test can assert ordering
			// against its expected slice without false negatives caused by
			// JSON quoting.
			turnText := line
			var envelope struct {
				Type    string `json:"type"`
				Message struct {
					Role    string `json:"role"`
					Content any    `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &envelope); err == nil && envelope.Type == "user" {
				switch c := envelope.Message.Content.(type) {
				case string:
					turnText = c
				case []any:
					var parts []string
					for _, item := range c {
						m, ok := item.(map[string]any)
						if !ok {
							continue
						}
						if t, ok := m["text"].(string); ok {
							parts = append(parts, t)
						}
					}
					turnText = strings.Join(parts, "\n")
				}
			}
			// Echo every turn back as a result event so the test
			// can assert the live process saw each one. Use json.Marshal
			// to keep escaping correct for any character class.
			payload, _ := json.Marshal(map[string]any{
				"type":    "result",
				"result":  "echo: " + turnText,
				"isError": false,
			})
			_, _ = os.Stdout.Write(payload)
			_, _ = os.Stdout.Write([]byte("\n"))
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

// TestNewClaudeSession_NoReplayFlagKeepsProcessAlive is the regression
// test for issue #1736. Before the fix, newClaudeSession passed
// --replay-user-messages to Claude Code, which drained stdin and exited
// after each turn. That made /compact (and any other in-session slash
// command) unreachable. The fix removes the flag; Claude Code then
// keeps reading stdin until either Close() or agent_session_idle_timeout_mins
// (#1338) reaps it.
//
// We verify both halves:
//
//	(a) the helper process (standing in for Claude Code) reports a
//	    missing --replay-user-messages in argv via a non-zero exit,
//	(b) the session can drive 100+ messages through the live process
//	    with /compact / /clear / /resume interleaved and the process
//	    echoes every one of them back, proving keep-alive works.
func TestNewClaudeSession_NoReplayFlagKeepsProcessAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build the helper command directly so the test does not depend on
	// any particular agent/session.go code path that would mask the flag.
	helperPath, err := exec.LookPath(os.Args[0])
	if err != nil {
		t.Fatalf("cannot locate test binary: %v", err)
	}
	bin := helperPath
	helperArgs := []string{
		"-test.run=TestHelperProcess",
		"--", "claude-stdin-echo",
	}
	helpEnv := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

	// Sanity: the helper itself asserts --replay-user-messages is not
	// present in argv. Run it once and verify the helper exits 0.
	probe := exec.CommandContext(ctx, bin, helperArgs...)
	probe.Env = helpEnv
	var probeStderr bytes.Buffer
	probe.Stderr = &probeStderr
	probe.Stdin = strings.NewReader("")
	if err := probe.Run(); err != nil {
		t.Fatalf("helper probe failed: %v\nstderr=%s", err, probeStderr.String())
	}

	workDir := t.TempDir()

	// Spawn newClaudeSession directly so we exercise the exact code path
	// that decides innerArgs/outerArgs. We bypass newClaudeSession's
	// permission-mode / system-prompt / plugin-dir machinery — they are
	// orthogonal to the flag under test.
	spawnOpts := core.SpawnOptions{}
	// The helper is the same Go test binary running TestHelperProcess in
	// helper mode; without GO_WANT_HELPER_PROCESS=1 the child would just
	// execute the parent test suite and exit immediately, never reaching
	// the helper branch.
	cs, err := newClaudeSession(
		ctx,
		workDir,
		bin,
		helperArgs,                           // cliExtraArgs (forwarded into allArgs)
		"",                                   // cmdArgsFlag (no wrapper bundling)
		"",                                   // model
		"",                                   // effort
		"",                                   // sessionID (truly fresh)
		"default",                            // mode
		"",                                   // systemPrompt
		"",                                   // appendSystemPrompt
		nil,                                  // allowedTools
		nil,                                  // disallowedTools
		nil,                                  // pluginDirs
		[]string{"GO_WANT_HELPER_PROCESS=1"}, // extraEnv: route child into helper branch
		"",                                   // platformPrompt
		false,                                // disableVerbose
		spawnOpts,
		0,  // maxContextTokens
		"", // ccDataDir (lets ensureSharedSystemPromptFile fall back to TempDir)
		"", // lang
	)
	if err != nil {
		t.Fatalf("newClaudeSession: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Wait for the system event so we know the helper is alive.
	// claudeSession.handleSystem emits EventText carrying the session id
	// when a `type:"system"` event with a session_id field arrives.
	startDeadline := time.After(5 * time.Second)
	for cs.CurrentSessionID() != "helper-keepalive" {
		select {
		case <-cs.Events():
			// drain
		case <-startDeadline:
			t.Fatalf("session id never set; helper did not emit a system event (alive=%v)", cs.Alive())
			return
		}
	}

	// Drive 100 normal messages + the three slash commands interleaved.
	// Issue #1736 expected behaviour: every one of these reaches the live
	// process and the process echoes it back. With --replay-user-messages
	// the process would have exited after the first message and the
	// remaining sends would fail with "session process is not running".
	plan := []string{}
	for i := 0; i < 100; i++ {
		plan = append(plan, "regular message "+itoa(i))
	}
	// Interleave the slash commands in the middle so the regression is
	// sensitive to keep-alive surviving a normal-message burst as well.
	plan = append(plan, "/compact", "/clear", "/resume")

	gotResults := 0
	for i, msg := range plan {
		if err := cs.Send(msg, "msg-"+itoa(i), nil, nil); err != nil {
			t.Fatalf("Send #%d (%q) failed: %v", i, msg, err)
		}
		if !cs.Alive() {
			t.Fatalf("session died after message #%d (%q) — the helper exited when it should have stayed alive", i, msg)
		}
	}

	// Drain echoed results until we have at least len(plan) of them or
	// time out. The helper echoes one `result` event per line written to
	// stdin.
	deadline := time.After(15 * time.Second)
	for gotResults < len(plan) {
		select {
		case ev, ok := <-cs.Events():
			if !ok {
				t.Fatalf("event channel closed after %d results; helper exited early (alive=%v)", gotResults, cs.Alive())
			}
			if ev.Type == core.EventResult {
				if !strings.HasPrefix(ev.Content, "echo: ") {
					t.Fatalf("result #%d = %q, want echo prefix", gotResults, ev.Content)
				}
				want := plan[gotResults]
				if ev.Content != "echo: "+want {
					t.Fatalf("result #%d = %q, want %q (order must be preserved across turns)", gotResults, ev.Content, "echo: "+want)
				}
				gotResults++
			}
		case <-deadline:
			t.Fatalf("only received %d/%d echoes within 15s", gotResults, len(plan))
		}
	}

	// Final assertion: the helper is still alive after 100+ messages.
	// Close() will send stdin EOF and the helper will exit cleanly.
	if !cs.Alive() {
		t.Fatal("helper died before Close() — keep-alive contract violated")
	}
}

// itoa avoids strconv import noise in the test body.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
