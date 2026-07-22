package kimi

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKimiSession(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "kimi-k2", "default", "resume-123", nil, 0, kimiFlagSupport{})
	require.NoError(t, err)
	require.NotNil(t, ks)
	assert.True(t, ks.Alive())
	assert.Equal(t, "resume-123", ks.CurrentSessionID())

	err = ks.Close()
	assert.NoError(t, err)
	assert.False(t, ks.Alive())
}

func TestExtractResumeSessionID(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"To resume this session: kimi -r e3690555-60eb-4d50-874b-e3647e9cee5b", "e3690555-60eb-4d50-874b-e3647e9cee5b"},
		{"To resume this session: kimi --resume abc-def", ""},
		{"To resume this session: no-id-here", ""},
		{"random text", ""},
	}

	for _, c := range cases {
		assert.Equal(t, c.expected, extractResumeSessionID(c.input), "input: %s", c.input)
	}
}

func TestHandleAssistantWithText(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.handleEvent(map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "Hello!"},
		},
	})

	// pendingMsgs should buffer the text
	assert.Len(t, ks.pendingMsgs, 1)
	assert.Equal(t, "Hello!", ks.pendingMsgs[0])
}

func TestHandleAssistantWithThink(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.handleEvent(map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "think", "think": "Let me think..."},
			map[string]any{"type": "text", "text": "Done!"},
		},
	})

	events := drainEvents(ks.events, 2)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventThinking, events[0].Type)
	assert.Equal(t, "Let me think...", events[0].Content)
	assert.Equal(t, "Done!", ks.pendingMsgs[0])
}

func TestHandleAssistantWithToolCalls(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.handleEvent(map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "I will run a command"},
		},
		"tool_calls": []any{
			map[string]any{
				"id": "tool_abc",
				"function": map[string]any{
					"name":      "Shell",
					"arguments": `{"command":"echo hello"}`,
				},
			},
		},
	})

	events := drainEvents(ks.events, 3)
	require.Len(t, events, 2)
	assert.Equal(t, core.EventThinking, events[0].Type)
	assert.Equal(t, "I will run a command", events[0].Content)
	assert.Equal(t, core.EventToolUse, events[1].Type)
	assert.Equal(t, "Shell", events[1].ToolName)
	assert.Equal(t, `{"command":"echo hello"}`, events[1].ToolInput)
	assert.Equal(t, "tool_abc", events[1].RequestID)
}

func TestHandleTool(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.handleEvent(map[string]any{
		"role":         "tool",
		"tool_call_id": "tool_abc",
		"content": []any{
			map[string]any{"type": "text", "text": "hello\n"},
		},
	})

	events := drainEvents(ks.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventToolResult, events[0].Type)
	assert.Equal(t, "tool_abc", events[0].ToolName)
	assert.Contains(t, events[0].ToolResult, "hello")
}

func TestFlushPendingAsText(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.pendingMsgs = []string{"Hello", " ", "world"}
	ks.flushPendingAsText()

	events := drainEvents(ks.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Equal(t, "Hello world", events[0].Content)
	assert.Empty(t, ks.pendingMsgs)
}

func TestFlushPendingAsThinking(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer ks.Close()

	ks.pendingMsgs = []string{"Thinking..."}
	ks.flushPendingAsThinking()

	events := drainEvents(ks.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventThinking, events[0].Type)
	assert.Equal(t, "Thinking...", events[0].Content)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hello world", truncate("hello world", 11))
	assert.Equal(t, "hello worl...", truncate("hello world", 10))
}

// TestBuildArgs_NoPrintSupportOmitsPrintFlag is the regression test for #1456.
// When the locally installed Kimi CLI does not advertise --print in its help
// output, cc-connect must omit that flag — otherwise the newer Kimi Code CLI
// exits with `error: unknown option '--print' (Did you mean --prompt?)`.
func TestBuildArgs_NoPrintSupportOmitsPrintFlag(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{Print: false})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("hello")

	for _, a := range args {
		if a == "--print" {
			t.Fatalf("buildArgs unexpectedly emitted --print when flagSupport.Print=false; args=%v", args)
		}
	}
	// --prompt must still be present so Kimi enters non-interactive mode.
	assert.Contains(t, args, "--prompt")
	assert.Contains(t, args, "hello")
}

// TestBuildArgs_PrintSupportIncludesPrintFlag covers the legacy kimi-cli
// branch — the binary advertises --print, so we must keep emitting it for
// --output-format stream-json to take effect.
func TestBuildArgs_PrintSupportIncludesPrintFlag(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{Print: true})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("hello")
	assert.Contains(t, args, "--print")
	assert.Contains(t, args, "--output-format")
	assert.Contains(t, args, "stream-json")
}

// TestBuildArgs_WorkDirFlagGated is the regression test for #1476. When the
// locally installed Kimi Code CLI does not advertise --work-dir in its help
// output, cc-connect must omit that flag — otherwise the CLI exits with
// `error: unknown option --work-dir`. The agent still runs in the correct
// directory because exec.Command.Dir is set separately (see session.go).
func TestBuildArgs_WorkDirFlagGated(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/", "", "default", "", nil, 0, kimiFlagSupport{WorkDir: false})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("hello")

	for _, a := range args {
		if a == "--work-dir" {
			t.Fatalf("buildArgs unexpectedly emitted --work-dir when flagSupport.WorkDir=false; args=%v", args)
		}
	}
	// The work-dir value must also NOT leak into args even though we asked
	// for one. (Confirms the gate is at the flag level, not just dropping
	// the flag-name while keeping the value.)
	for _, a := range args {
		if a == "/" {
			t.Fatalf("buildArgs leaked workDir value %q into args; args=%v", a, args)
		}
	}
}

// TestBuildArgs_WorkDirFlagEmitted covers the legacy kimi-cli branch — the
// binary advertises --work-dir, so we must keep emitting it for non-default
// workspace locations.
func TestBuildArgs_WorkDirFlagEmitted(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/", "", "default", "", nil, 0, kimiFlagSupport{WorkDir: true})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("hello")
	assert.Contains(t, args, "--work-dir")
	assert.Contains(t, args, "/")
}

// TestBuildArgs_PlanMode confirms plan mode still passes --plan independent
// of --print support.
func TestBuildArgs_PlanMode(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "kimi-k2", "plan", "", nil, 0, kimiFlagSupport{Print: false})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("plan this")
	assert.Contains(t, args, "--plan")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "kimi-k2")
}

// TestBuildArgs_ResumeSession_LegacyDialect covers the legacy kimi-cli
// resume flag: with --print advertised (legacy flavor), session continuity
// must keep using `--resume <id>`.
func TestBuildArgs_ResumeSession_LegacyDialect(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "sess-xyz", nil, 0, kimiFlagSupport{Print: true})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("continue")
	resumeIdx := -1
	for i, a := range args {
		if a == "--resume" {
			resumeIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, resumeIdx, 0, "legacy args should include --resume; got %v", args)
	require.Less(t, resumeIdx+1, len(args), "--resume should be followed by an id")
	assert.Equal(t, "sess-xyz", args[resumeIdx+1])
	assert.NotContains(t, args, "-r", "legacy dialect must not use -r")
}

// TestBuildArgs_ResumeSession_ModernDialect is the #1561 resume-flag
// regression test: the Kimi Code CLI rejects `--resume` (unknown option) and
// expects `-r <id>` — the same command its own resume hint prints
// ("To resume this session: kimi -r <id>").
func TestBuildArgs_ResumeSession_ModernDialect(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "session_abc-123", nil, 0, kimiFlagSupport{})
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("continue")
	for _, a := range args {
		if a == "--resume" {
			t.Fatalf("modern dialect must not emit --resume; args=%v", args)
		}
	}
	rIdx := -1
	for i, a := range args {
		if a == "-r" {
			rIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, rIdx, 0, "modern args should include -r; got %v", args)
	require.Less(t, rIdx+1, len(args), "-r should be followed by an id")
	assert.Equal(t, "session_abc-123", args[rIdx+1])
}

// TestBuildArgs_QuietFlagGated covers the #1561 --quiet dialect difference:
// legacy kimi-cli accepts --quiet; the Kimi Code CLI dropped it, so quiet
// mode must be emulated by local event suppression instead of the flag.
func TestBuildArgs_QuietFlagGated(t *testing.T) {
	ctx := context.Background()

	legacy, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "quiet", "", nil, 0,
		kimiFlagSupport{Print: true, Quiet: true})
	require.NoError(t, err)
	defer func() { _ = legacy.Close() }()
	assert.Contains(t, legacy.buildArgs("hi"), "--quiet",
		"legacy flavor should pass --quiet through")
	assert.False(t, legacy.suppressIntermediateEvents(),
		"legacy flavor lets the CLI do quiet filtering")

	modern, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "quiet", "", nil, 0,
		kimiFlagSupport{})
	require.NoError(t, err)
	defer func() { _ = modern.Close() }()
	assert.NotContains(t, modern.buildArgs("hi"), "--quiet",
		"modern flavor must not emit --quiet (unknown option)")
	assert.True(t, modern.suppressIntermediateEvents(),
		"modern flavor emulates quiet mode by suppressing events locally")
}

// TestHandleAssistantStringContent is the #1561 content-shape regression
// test: the Kimi Code CLI emits assistant content as a plain string instead
// of typed blocks; the text must still be buffered for the final reply.
func TestHandleAssistantStringContent(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer func() { _ = ks.Close() }()

	ks.handleEvent(map[string]any{
		"role":    "assistant",
		"content": "OK",
	})

	require.Len(t, ks.pendingMsgs, 1)
	assert.Equal(t, "OK", ks.pendingMsgs[0])
}

// TestHandleToolStringContent covers the plain-string content shape for tool
// results on the Kimi Code CLI (#1561).
func TestHandleToolStringContent(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer func() { _ = ks.Close() }()

	ks.handleEvent(map[string]any{
		"role":         "tool",
		"tool_call_id": "tool_abc",
		"content":      "file written\n",
	})

	events := drainEvents(ks.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventToolResult, events[0].Type)
	assert.Contains(t, events[0].ToolResult, "file written")
}

// TestHandleMetaResumeHint is the #1561 session-continuity regression test:
// the Kimi Code CLI reports the resumable session id via a stdout JSON meta
// line ({"role":"meta","type":"session.resume_hint",...}) instead of the
// legacy plain-text/stderr hint, and cc-connect must capture it so the next
// turn can resume.
func TestHandleMetaResumeHint(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer func() { _ = ks.Close() }()

	// Verbatim meta line captured from kimi-code v0.26.0.
	ks.handleEvent(map[string]any{
		"role":       "meta",
		"type":       "session.resume_hint",
		"session_id": "session_c85029b1-6449-4b1e-a25f-018acd258eeb",
		"command":    "kimi -r session_c85029b1-6449-4b1e-a25f-018acd258eeb",
		"content":    "To resume this session: kimi -r session_c85029b1-6449-4b1e-a25f-018acd258eeb",
	})
	assert.Equal(t, "session_c85029b1-6449-4b1e-a25f-018acd258eeb", ks.CurrentSessionID())

	// Fallback path: no session_id field, parse the legacy hint in content.
	ks2, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "", nil, 0, kimiFlagSupport{})
	defer func() { _ = ks2.Close() }()
	ks2.handleEvent(map[string]any{
		"role":    "meta",
		"type":    "session.resume_hint",
		"content": "To resume this session: kimi -r session_fallback-id",
	})
	assert.Equal(t, "session_fallback-id", ks2.CurrentSessionID())

	// Unrelated meta types must not disturb the session id.
	ks2.handleEvent(map[string]any{"role": "meta", "type": "some.other.meta"})
	assert.Equal(t, "session_fallback-id", ks2.CurrentSessionID())
}

// TestQuietModeSuppressesIntermediateEvents verifies the local quiet-mode
// emulation for the Kimi Code CLI (#1561): thinking, tool-use and
// tool-result events are dropped, while the final text still gets through.
func TestQuietModeSuppressesIntermediateEvents(t *testing.T) {
	ctx := context.Background()
	ks, _ := newKimiSession(ctx, "kimi", nil, "/tmp", "", "quiet", "", nil, 0, kimiFlagSupport{})
	defer func() { _ = ks.Close() }()

	ks.handleEvent(map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "think", "think": "secret reasoning"},
			map[string]any{"type": "text", "text": "working on it"},
		},
		"tool_calls": []any{
			map[string]any{
				"id":       "tool_1",
				"function": map[string]any{"name": "Shell", "arguments": `{"command":"ls"}`},
			},
		},
	})
	ks.handleEvent(map[string]any{
		"role":         "tool",
		"tool_call_id": "tool_1",
		"content":      "listing...",
	})
	// Final assistant text of the turn (stays buffered until end-of-turn).
	ks.handleEvent(map[string]any{
		"role":    "assistant",
		"content": "done!",
	})

	events := drainEvents(ks.events, 3)
	assert.Empty(t, events, "quiet mode must suppress thinking/tool events, got %v", events)

	// The remaining buffered text flushes as the final message.
	ks.flushPendingAsText()
	final := drainEvents(ks.events, 1)
	require.Len(t, final, 1)
	assert.Equal(t, core.EventText, final[0].Type)
	assert.Equal(t, "done!", final[0].Content)
}

func drainEvents(ch <-chan core.Event, max int) []core.Event {
	var events []core.Event
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < max; i++ {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-timeout:
			return events
		}
	}
	return events
}
