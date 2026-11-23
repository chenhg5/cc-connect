package codex

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestConfirmedQuotaFailureKeepsStructuredEvidence(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 2), currentTurn: "turn-1"}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"synthetic-secret","codexErrorInfo":"usageLimitExceeded"}}}`))
	ev := <-s.events
	if ev.Type != core.EventError || ev.Metadata["quota_exhausted"] != true || ev.Error.Error() != "quota exhausted" {
		t.Fatalf("quota evidence = %+v", ev)
	}
}
