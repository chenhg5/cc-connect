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

// TestBuildArgs_ResumeSession ensures session continuity flags are emitted
// regardless of the --print probe result.
func TestBuildArgs_ResumeSession(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", nil, "/tmp", "", "default", "sess-xyz", nil, 0, kimiFlagSupport{Print: false})
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
	require.GreaterOrEqual(t, resumeIdx, 0, "args should include --resume; got %v", args)
	require.Less(t, resumeIdx+1, len(args), "--resume should be followed by an id")
	assert.Equal(t, "sess-xyz", args[resumeIdx+1])
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
