package claudecode

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleResultParsesUsage(t *testing.T) {
	raw := map[string]any{
		"type":       "result",
		"result":     "test response",
		"session_id": "sess-123",
		"usage": map[string]any{
			"input_tokens":  float64(50000),
			"output_tokens": float64(1500),
		},
	}

	cs := &claudeSession{
		events: make(chan core.Event, 1),
		ctx:    context.Background(),
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", evt.InputTokens)
	}
	if evt.OutputTokens != 1500 {
		t.Errorf("OutputTokens = %d, want 1500", evt.OutputTokens)
	}
}

func TestHandleResultNoUsage(t *testing.T) {
	raw := map[string]any{
		"type":   "result",
		"result": "test response",
	}

	cs := &claudeSession{
		events: make(chan core.Event, 1),
		ctx:    context.Background(),
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", evt.InputTokens)
	}
	if evt.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", evt.OutputTokens)
	}
}
