package claudecode

import (
	"context"
	"testing"

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

// TestHandleResultSuccessSubtype ensures a successful result still routes
// through EventResult, not EventError.
func TestHandleResultSuccessSubtype(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("s1")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":    "result",
		"subtype": "success",
		"result":  "all good",
	}
	cs.handleResult(raw)
	evt := <-cs.events
	if evt.Type != core.EventResult {
		t.Fatalf("Type = %v, want EventResult", evt.Type)
	}
	if evt.ErrorKind != core.ErrorKindUnknown {
		t.Errorf("ErrorKind = %q, want empty", evt.ErrorKind)
	}
}

// TestHandleResultRateLimitClassified verifies a result event carrying an
// Anthropic rate_limit_error payload is surfaced as EventError with
// ErrorKindRateLimit so the engine can schedule a retry.
func TestHandleResultRateLimitClassified(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("s1")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":     "result",
		"subtype":  "error_during_execution",
		"is_error": true,
		"result":   `{"error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`,
	}
	cs.handleResult(raw)
	evt := <-cs.events
	if evt.Type != core.EventError {
		t.Fatalf("Type = %v, want EventError", evt.Type)
	}
	if evt.ErrorKind != core.ErrorKindRateLimit {
		t.Errorf("ErrorKind = %q, want %q", evt.ErrorKind, core.ErrorKindRateLimit)
	}
	if evt.Error == nil {
		t.Fatal("Error is nil")
	}
}

// TestHandleResultOverloadedClassified verifies 529/overloaded_error is
// classified as ErrorKindOverloaded.
func TestHandleResultOverloadedClassified(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("s1")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":     "result",
		"subtype":  "error_during_execution",
		"is_error": true,
		"result":   `{"error":{"type":"overloaded_error","message":"overloaded"}}`,
	}
	cs.handleResult(raw)
	evt := <-cs.events
	if evt.Type != core.EventError {
		t.Fatalf("Type = %v, want EventError", evt.Type)
	}
	if evt.ErrorKind != core.ErrorKindOverloaded {
		t.Errorf("ErrorKind = %q, want %q", evt.ErrorKind, core.ErrorKindOverloaded)
	}
}

// TestHandleResultMaxTurnsNotRetriable verifies error_max_turns surfaces as
// EventError but with ErrorKindUnknown (not retriable) — the engine must
// fall through to the normal error path, not spin forever.
func TestHandleResultMaxTurnsNotRetriable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("s1")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":     "result",
		"subtype":  "error_max_turns",
		"is_error": true,
		"result":   "Reached max turns",
	}
	cs.handleResult(raw)
	evt := <-cs.events
	if evt.Type != core.EventError {
		t.Fatalf("Type = %v, want EventError", evt.Type)
	}
	if evt.ErrorKind != core.ErrorKindUnknown {
		t.Errorf("ErrorKind = %q, want empty/unknown", evt.ErrorKind)
	}
}
