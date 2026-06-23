package claudecode

import (
	"bytes"
	"context"
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
			"output_tokens":               float64(648),       // real turn total
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
//  - string content (plain text result)
//  - array content (Anthropic SDK multi-block: [{type:"text", text:"..."}])
//  - is_error=true (exit code 1, success=false)
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
							"type":          "tool_result",
							"tool_use_id":   "toolu_abc",
							"is_error":      false,
							"content":       "command output here",
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

	mode := os.Args[len(os.Args)-1]
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
	default:
		os.Exit(2)
	}
}
