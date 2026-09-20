package claudecode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Regression tests for issue #1877: after `claude --resume` of a session whose
// previous turn was interrupted, the CLI injects an isMeta user message
// ("Continue from where you left off.") whose micro-turn terminates with an
// EMPTY result event. That result belongs to no cc-connect Send() — if the
// engine consumes it as the pending user message's turn completion, the reply
// collapses to "(empty response)" while the CLI keeps working on the real
// prompt (production timeline: 2026-09-20, "(空响应)" 2.4 s after spawn, real
// report in the transcript 30 min later, never delivered).

func newResumeTestSession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)
	return cs
}

func resumeContinuationUserEvent() map[string]any {
	return map[string]any{
		"type":   "user",
		"isMeta": true,
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Continue from where you left off."},
			},
		},
	}
}

// The empty result of the auto-continuation micro-turn must be swallowed.
func TestResumeContinuationEmptyResultSuppressed(t *testing.T) {
	cs := newResumeTestSession(t)

	cs.handleUser(resumeContinuationUserEvent())
	if _, ok := cs.resumeContinueAt.Load().(time.Time); !ok {
		t.Fatal("handleUser did not record the continuation injection time")
	}

	cs.handleResult(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"result":     "",
		"session_id": "test-session",
	})

	select {
	case evt := <-cs.events:
		t.Fatalf("empty auto-continuation result was not suppressed, got event %+v", evt)
	default:
	}

	// The REAL turn's terminal result must still terminate the turn.
	cs.handleResult(map[string]any{
		"type":    "result",
		"result":  "real final reply",
		"usage":   map[string]any{"input_tokens": float64(10)},
	})
	evt := <-cs.events
	if evt.Type != core.EventResult || !evt.Done || evt.Content != "real final reply" {
		t.Fatalf("real turn result mishandled: %+v", evt)
	}
}

// A non-empty result from the continuation turn passes through: the model may
// legitimately resume the interrupted work and produce visible output.
func TestResumeContinuationNonEmptyResultPasses(t *testing.T) {
	cs := newResumeTestSession(t)

	cs.handleUser(resumeContinuationUserEvent())
	cs.handleResult(map[string]any{
		"type":    "result",
		"result":  "continued the interrupted task",
	})

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventResult || evt.Content != "continued the interrupted task" {
			t.Fatalf("non-empty continuation result mishandled: %+v", evt)
		}
	default:
		t.Fatal("non-empty continuation result was suppressed")
	}

	// Marker is consumed: a later empty result (a different turn) must pass.
	cs.handleResult(map[string]any{"type": "result", "result": ""})
	select {
	case evt := <-cs.events:
		if evt.Content != "" {
			t.Fatalf("expected empty later result to pass through, got %+v", evt)
		}
	default:
		t.Fatal("later result was suppressed after the marker was already consumed")
	}
}

// Without the injection, empty results keep today's behavior (emitted), so
// the guard cannot regress turns that never went through a resume.
func TestNoContinuationEmptyResultStillEmitted(t *testing.T) {
	cs := newResumeTestSession(t)

	cs.handleResult(map[string]any{"type": "result", "result": ""})
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventResult || evt.Content != "" {
			t.Fatalf("unexpected event shape: %+v", evt)
		}
	default:
		t.Fatal("plain empty result was suppressed without any continuation injection")
	}
}

func TestIsResumeContinuationEvent(t *testing.T) {
	if !isResumeContinuationEvent(resumeContinuationUserEvent()) {
		t.Fatal("isMeta continuation event not recognized")
	}

	// Non-meta user message with the same text must not match.
	raw := resumeContinuationUserEvent()
	raw["isMeta"] = false
	if isResumeContinuationEvent(raw) {
		t.Fatal("non-meta event matched")
	}

	// isMeta with different text (e.g. a caveat injection) must not match.
	raw = resumeContinuationUserEvent()
	raw["message"].(map[string]any)["content"] = []any{
		map[string]any{"type": "text", "text": "Caveat: some harness note"},
	}
	if isResumeContinuationEvent(raw) {
		t.Fatal("unrelated isMeta event matched")
	}

	// String content form.
	if !isResumeContinuationEvent(map[string]any{
		"isMeta":  true,
		"message": map[string]any{"content": "Continue from where you left off."},
	}) {
		t.Fatal("string-content continuation event not recognized")
	}

	// Missing message.
	if isResumeContinuationEvent(map[string]any{"isMeta": true}) {
		t.Fatal("event without message matched")
	}
}
