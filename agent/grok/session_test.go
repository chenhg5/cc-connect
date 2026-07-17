package grok

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGrokSession(t *testing.T) {
	ctx := context.Background()
	gs, err := newGrokSession(ctx, sessionConfig{
		cmd:      "grok",
		workDir:  "/tmp",
		model:    "grok-4.5",
		mode:     "default",
		resumeID: "resume-abc",
	})
	require.NoError(t, err)
	require.NotNil(t, gs)
	assert.True(t, gs.Alive())
	assert.Equal(t, "resume-abc", gs.CurrentSessionID())

	err = gs.Close()
	assert.NoError(t, err)
	assert.False(t, gs.Alive())
}

func TestBuildArgs_PromptFileAndResume(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{
		cmd:      "grok",
		workDir:  "/proj",
		model:    "grok-4.5",
		mode:     "yolo",
		resumeID: "sid-1",
	})
	require.NoError(t, err)
	defer gs.Close()

	args := gs.buildArgs("", "/tmp/prompt.txt")
	assert.Contains(t, args, "--output-format")
	assert.Contains(t, args, "streaming-json")
	assert.Contains(t, args, "--cwd")
	assert.Contains(t, args, "/proj")
	assert.Contains(t, args, "--resume")
	assert.Contains(t, args, "sid-1")
	assert.Contains(t, args, "-m")
	assert.Contains(t, args, "grok-4.5")
	assert.Contains(t, args, "--permission-mode")
	assert.Contains(t, args, "bypassPermissions")
	assert.Contains(t, args, "--always-approve")
	assert.Contains(t, args, "--prompt-file")
	assert.Contains(t, args, "/tmp/prompt.txt")
	assert.NotContains(t, args, "-p")
}

func TestBuildArgs_PlanModeNoAlwaysApprove(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{
		cmd:     "grok",
		workDir: "/proj",
		mode:    "plan",
	})
	require.NoError(t, err)
	defer gs.Close()

	args := gs.buildArgs("hello", "")
	assert.Contains(t, args, "plan")
	assert.NotContains(t, args, "--always-approve")
	assert.Contains(t, args, "-p")
	assert.Contains(t, args, "hello")
}

func TestBuildArgs_InlinePrompt(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{
		cmd:  "grok",
		mode: "default",
	})
	require.NoError(t, err)
	defer gs.Close()

	args := gs.buildArgs("ping", "")
	// Find -p value
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			assert.Equal(t, "ping", args[i+1])
			return
		}
	}
	t.Fatal("expected -p flag")
}

func TestHandleEvent_ThoughtTextEnd(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok"})
	require.NoError(t, err)
	defer gs.Close()

	// Grok streams thought/text as token deltas — must coalesce.
	gs.handleEvent(map[string]any{"type": "thought", "data": "hmm"})
	gs.handleEvent(map[string]any{"type": "thought", "data": " "})
	gs.handleEvent(map[string]any{"type": "thought", "data": "ok"})
	gs.handleEvent(map[string]any{"type": "text", "data": "po"})
	gs.handleEvent(map[string]any{"type": "text", "data": "ng"})
	terminal := gs.handleEvent(map[string]any{
		"type":      "end",
		"sessionId": "019f-test-session",
		"usage": map[string]any{
			"input_tokens":            float64(100),
			"output_tokens":           float64(5),
			"cache_read_input_tokens": float64(50),
		},
	})
	assert.True(t, terminal)
	assert.Equal(t, "019f-test-session", gs.CurrentSessionID())

	events := drainEvents(gs.events, 3)
	require.Len(t, events, 3)

	assert.Equal(t, core.EventThinking, events[0].Type)
	assert.Equal(t, "hmm ok", events[0].Content)

	assert.Equal(t, core.EventText, events[1].Type)
	assert.Equal(t, "pong", events[1].Content)

	assert.Equal(t, core.EventResult, events[2].Type)
	assert.True(t, events[2].Done)
	assert.Equal(t, "019f-test-session", events[2].SessionID)
	assert.Equal(t, 100, events[2].InputTokens)
	assert.Equal(t, 5, events[2].OutputTokens)
	assert.Equal(t, 50, events[2].CacheReadInputTokens)
}

func TestHandleEvent_ThoughtTokensNotEmittedPerToken(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok"})
	require.NoError(t, err)
	defer gs.Close()

	// Mimic the WeChat spam case: one EventThinking per word would be wrong.
	for _, w := range []string{"The", " user", " sent", " just", " \"", "1", "\"."} {
		gs.handleEvent(map[string]any{"type": "thought", "data": w})
	}
	// No non-thought event yet → nothing flushed.
	select {
	case e := <-gs.events:
		t.Fatalf("unexpected early event: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	gs.flushThought()
	events := drainEvents(gs.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventThinking, events[0].Type)
	assert.Equal(t, `The user sent just "1".`, events[0].Content)
}

func TestHandleEvent_ToolUseAndResult(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok"})
	require.NoError(t, err)
	defer gs.Close()

	gs.handleEvent(map[string]any{
		"type": "tool_use",
		"name": "Shell",
		"id":   "t1",
		"input": map[string]any{
			"command": "echo hi",
		},
	})
	gs.handleEvent(map[string]any{
		"type":         "tool_result",
		"tool_call_id": "t1",
		"data":         "hi\n",
	})

	events := drainEvents(gs.events, 2)
	require.Len(t, events, 2)
	assert.Equal(t, core.EventToolUse, events[0].Type)
	assert.Equal(t, "Shell", events[0].ToolName)
	assert.Contains(t, events[0].ToolInput, "echo hi")
	assert.Equal(t, "t1", events[0].RequestID)

	assert.Equal(t, core.EventToolResult, events[1].Type)
	assert.Equal(t, "hi\n", events[1].ToolResult)
}

func TestHandleEvent_Error(t *testing.T) {
	gs, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok"})
	require.NoError(t, err)
	defer gs.Close()

	gs.handleEvent(map[string]any{"type": "error", "message": "boom"})
	events := drainEvents(gs.events, 1)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventError, events[0].Type)
	require.Error(t, events[0].Error)
	assert.Contains(t, events[0].Error.Error(), "boom")
}

func drainEvents(ch <-chan core.Event, n int) []core.Event {
	var out []core.Event
	for i := 0; i < n; i++ {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-time.After(2 * time.Second):
			return out
		}
	}
	return out
}
