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
	ks, err := newKimiSession(ctx, "kimi", "/tmp", "kimi-k2", "default", "resume-123", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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
	ks, _ := newKimiSession(ctx, "kimi", "/tmp", "", "default", "", nil, 0)
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

// TestBuildArgs_QuietModeDoesNotPassQuietFlag is the regression test for issue #806.
// Kimi CLI does not support --quiet and returns an error when it is passed.
// Switching to /mode quiet must not produce a --quiet CLI flag.
func TestBuildArgs_QuietModeDoesNotPassQuietFlag(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", "/tmp", "", "quiet", "", nil, 0)
	require.NoError(t, err)
	defer ks.Close()

	args := ks.buildArgs("test prompt")

	for _, arg := range args {
		if arg == "--quiet" {
			t.Errorf("buildArgs produced --quiet for quiet mode, Kimi CLI does not support this flag")
		}
	}
}

// TestBuildArgs_PlanModePassesPlanFlag ensures --plan is still passed in plan mode.
func TestBuildArgs_PlanModePassesPlanFlag(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", "/tmp", "", "plan", "", nil, 0)
	require.NoError(t, err)
	defer ks.Close()

	args := ks.buildArgs("test prompt")

	hasPlan := false
	for _, arg := range args {
		if arg == "--plan" {
			hasPlan = true
		}
		if arg == "--quiet" {
			t.Error("plan mode should not pass --quiet")
		}
	}
	if !hasPlan {
		t.Error("plan mode should pass --plan to Kimi CLI")
	}
}

// TestBuildArgs_DefaultModeHasNoExtraFlags verifies default mode only passes
// the base flags and no mode-specific extras.
func TestBuildArgs_DefaultModeHasNoExtraFlags(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", "/tmp/work", "kimi-k2", "default", "", nil, 0)
	require.NoError(t, err)
	defer ks.Close()

	args := ks.buildArgs("hello")

	// Must contain base flags
	assertContainsArg(t, args, "--print")
	assertContainsArg(t, args, "stream-json")
	// Must NOT contain mode-specific flags
	assertNotContainsArg(t, args, "--quiet")
	assertNotContainsArg(t, args, "--plan")
}

// TestBuildArgs_ResumeSessionID checks that --resume is passed when a session ID exists.
func TestBuildArgs_ResumeSessionID(t *testing.T) {
	ctx := context.Background()
	ks, err := newKimiSession(ctx, "kimi", "/tmp", "", "default", "session-abc", nil, 0)
	require.NoError(t, err)
	defer ks.Close()

	args := ks.buildArgs("continue")

	// Find --resume and its value
	for i, arg := range args {
		if arg == "--resume" && i+1 < len(args) {
			if args[i+1] != "session-abc" {
				t.Errorf("--resume value = %q, want %q", args[i+1], "session-abc")
			}
			return
		}
	}
	t.Error("buildArgs should include --resume <session-id> when session ID is set")
}

func assertContainsArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v should contain %q", args, want)
}

func assertNotContainsArg(t *testing.T, args []string, unwanted string) {
	t.Helper()
	for _, a := range args {
		if a == unwanted {
			t.Errorf("args %v should NOT contain %q", args, unwanted)
			return
		}
	}
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
