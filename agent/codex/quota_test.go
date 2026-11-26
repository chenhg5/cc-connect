package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfirmedQuotaFailureKeepsStructuredEvidence(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 2), currentTurn: "turn-1"}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"synthetic-secret","codexErrorInfo":"usageLimitExceeded"}}}`))
	ev := <-s.events
	if ev.Type != core.EventError || ev.Metadata["quota_exhausted"] != true || ev.Error.Error() != "quota exhausted" {
		t.Fatalf("quota evidence = %+v", ev)
	}
	if s.currentTurn != "" {
		t.Fatalf("turn not settled: %q", s.currentTurn)
	}
}

func TestGenericTurnFailureIsNotQuota(t *testing.T) {
	for _, info := range []string{`"rateLimitExceeded"`, `{"httpConnectionFailed":{"httpStatusCode":429}}`, `null`} {
		s := &appServerSession{events: make(chan core.Event, 2), currentTurn: "turn-1"}
		s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"provider said no","codexErrorInfo":`+info+`}}}`))
		ev := <-s.events
		if ev.Type != core.EventError || ev.Metadata["quota_exhausted"] == true || !strings.Contains(ev.Error.Error(), "provider said no") {
			t.Fatalf("info %s: event = %+v", info, ev)
		}
	}
}
