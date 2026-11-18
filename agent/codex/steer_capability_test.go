package codex

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestCodexSession_SteeringCapabilityMatrix(t *testing.T) {
	if (&appServerSession{}).SupportsMidTurnSteering() != true {
		t.Fatal("app-server session must support turn/steer")
	}
	if (&codexSession{}).SupportsMidTurnSteering() != false {
		t.Fatal("exec-subprocess session must not claim steering")
	}
}

// A failed turn/steer drops the current turn id so the next message starts a
// fresh turn. The running turn must still deliver its EventResult, otherwise
// the engine's foreground loop waits for a result that never arrives.
func TestAppServerSession_TurnResultSurvivesSteerError(t *testing.T) {
	s := &appServerSession{
		events:      make(chan core.Event, 8),
		currentTurn: "turn-1",
		turnActive:  true,
	}
	s.retireSteerTurn("turn-1")

	var notif turnNotification
	notif.Turn.ID = "turn-1"
	notif.Turn.Status = "completed"
	raw, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	s.handleNotification("turn/completed", raw)

	select {
	case ev := <-s.events:
		if ev.Type != core.EventResult {
			t.Fatalf("first event type = %v, want EventResult", ev.Type)
		}
	default:
		t.Fatal("turn/completed after a failed steer produced no EventResult; the engine would hang until timeout")
	}
}
