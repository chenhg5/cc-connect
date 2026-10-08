package core

import (
	"strings"
	"testing"
	"time"
)

func TestSharedPresentationTimeoutCannotCancelReplacementTurn(t *testing.T) {
	for _, idle := range []bool{true, false} {
		t.Run(map[bool]string{true: "idle", false: "maximum"}[idle], func(t *testing.T) {
			env := newSharedTestEnv(t)
			if idle {
				env.e.eventIdleTimeout = 80 * time.Millisecond
			} else {
				env.e.eventIdleTimeout = 0
				env.e.maxTurnTime = 80 * time.Millisecond
			}
			env.send("/attach first")
			env.await("Attached to session first")
			as := env.a.connection("first")
			as.emit(Event{Type: EventToolUse, TurnID: "active-first", ToolName: "Bash", ToolInput: "OLD LIVE"})
			env.await("OLD LIVE")
			as.mu.Lock()
			as.turn = "replacement"
			as.mu.Unlock()
			time.Sleep(180 * time.Millisecond)
			env.send("/steer still alive")
			env.await("STEER observed: still alive")
			if strings.Contains(env.visible(), "timed out") || strings.Contains(env.visible(), "maximum time") {
				t.Fatal(env.visible())
			}
		})
	}
}
