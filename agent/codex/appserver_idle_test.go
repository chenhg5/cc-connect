package codex

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_IdleDoesNotFinishTurnBeforeFinalAnswer(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "turn-1"}
	s.threadID.Store("thread-1")

	s.handleNotification("thread/status/changed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"status":   map[string]any{"type": "idle"},
	}))
	select {
	case event := <-s.events:
		t.Fatalf("idle emitted premature terminal event: %#v", event)
	default:
	}

	s.handleNotification("item/completed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"item":     map[string]any{"type": "agentMessage", "text": "final answer"},
	}))
	completed := notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	s.handleNotification("turn/completed", completed)
	s.handleNotification("turn/completed", completed)

	if len(s.events) != 2 {
		t.Fatalf("got %d events, want final answer followed by one result", len(s.events))
	}
	if event := <-s.events; event.Type != core.EventText || event.Content != "final answer" {
		t.Fatalf("text event = %#v, want final answer", event)
	}
	if event := <-s.events; event.Type != core.EventResult || !event.Done {
		t.Fatalf("terminal event = %#v, want completed result", event)
	}
}

func TestAppServerSession_IdleDoesNotHideTurnFailure(t *testing.T) {
	s := &appServerSession{
		events:      make(chan core.Event, 8),
		currentTurn: "turn-1",
		pendingMsgs: []string{"unfinished answer"},
	}
	s.threadID.Store("thread-1")

	s.handleNotification("thread/status/changed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"status":   map[string]any{"type": "idle"},
	}))
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"turn": map[string]any{
			"id":     "turn-1",
			"status": "failed",
			"error":  map[string]any{"message": "service unavailable"},
		},
	}))

	if len(s.events) != 1 {
		t.Fatalf("got %d events, want one error and no successful result", len(s.events))
	}
	if event := <-s.events; event.Type != core.EventError || event.Error == nil || event.Error.Error() != "service unavailable" {
		t.Fatalf("terminal event = %#v, want service unavailable error", event)
	}
	if got := appServerCurrentTurn(s); got != "" {
		t.Fatalf("current turn = %q after failure, want empty", got)
	}
}

func TestAppServerSession_StaleIdleDoesNotFinishNextTurn(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "turn-1"}
	s.threadID.Store("thread-1")
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	}))
	<-s.events
	s.handleNotification("turn/started", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-2", "status": "inProgress"},
	}))
	// A thread status has no turn ID, so a delayed idle cannot be attributed
	// to the active turn even when its thread ID matches.
	s.handleNotification("thread/status/changed", notificationProbe(t, map[string]any{
		"threadId": "thread-1",
		"status":   map[string]any{"type": "idle"},
	}))
	if got := appServerCurrentTurn(s); got != "turn-2" {
		t.Fatalf("current turn = %q after stale idle, want turn-2", got)
	}
	select {
	case event := <-s.events:
		t.Fatalf("stale idle emitted event: %#v", event)
	default:
	}
}
