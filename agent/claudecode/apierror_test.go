package claudecode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const usageLimitLine = `{"type":"assistant","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your session limit"}]},"error":"rate_limit","is_api_error_message":true}`
const usageLimitResult = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit"}`

func newAPIErrTestSession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{events: make(chan core.Event, 8), ctx: ctx}
	cs.sessionID.Store("s")
	old := apiErrorResultGrace
	apiErrorResultGrace = 50 * time.Millisecond
	t.Cleanup(func() { apiErrorResultGrace = old })
	return cs
}

func nextEvent(t *testing.T, cs *claudeSession) (core.Event, bool) {
	t.Helper()
	select {
	case e := <-cs.events:
		return e, true
	case <-time.After(300 * time.Millisecond):
		return core.Event{}, false
	}
}

// Usage limit with no result from the CLI: the fallback must end the turn.
func TestAPIErrorWithoutResultSynthesizesTurnEnd(t *testing.T) {
	cs := newAPIErrTestSession(t)
	cs.handleReadLoopLine(usageLimitLine)
	if e, _ := nextEvent(t, cs); e.Type != core.EventText {
		t.Fatalf("first event = %v, want text", e.Type)
	}
	e, ok := nextEvent(t, cs)
	if !ok || e.Type != core.EventResult || !e.Done || e.Content != "You've hit your session limit" {
		t.Fatalf("want synthesized done result, got ok=%v %+v", ok, e)
	}
	// The CLI's late result for the same turn is dropped.
	cs.handleReadLoopLine(usageLimitResult)
	if e, ok := nextEvent(t, cs); ok {
		t.Fatalf("late result should be dropped, got %+v", e)
	}
}

// Usage limit followed by the CLI's own result: exactly one turn end.
func TestAPIErrorWithResultEndsTurnOnce(t *testing.T) {
	cs := newAPIErrTestSession(t)
	cs.handleReadLoopLine(usageLimitLine)
	nextEvent(t, cs) // text
	cs.handleReadLoopLine(usageLimitResult)
	if e, ok := nextEvent(t, cs); !ok || e.Type != core.EventResult {
		t.Fatalf("want real result, got ok=%v %+v", ok, e)
	}
	if e, ok := nextEvent(t, cs); ok {
		t.Fatalf("fallback fired after real result: %+v", e)
	}
}

// The session closes before the fallback fires: no send on the closed channel.
func TestAPIErrorFallbackAfterCloseDoesNotPanic(t *testing.T) {
	cs := newAPIErrTestSession(t)
	cs.handleReadLoopLine(usageLimitLine)
	nextEvent(t, cs) // text
	cs.apiErrMu.Lock()
	cs.apiErrClosed = true
	if cs.apiErrTimer != nil {
		cs.apiErrTimer.Stop()
		cs.apiErrTimer = nil
	}
	cs.apiErrMu.Unlock()
	close(cs.events)
	time.Sleep(2 * apiErrorResultGrace)
}
