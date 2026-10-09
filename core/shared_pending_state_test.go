package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSharedPresentationPendingUsesOwnerRequestsAcrossViews(t *testing.T) {
	owner := &interactiveState{}
	owner.shared.requests = map[string]Event{
		"first":   {TurnID: "turn-1"},
		"second":  {TurnID: "turn-2"},
		"unbound": {},
	}
	first := &interactiveState{sharedPresentation: &sharedPresentationState{runtime: owner, turnID: "turn-1"}}
	second := &interactiveState{sharedPresentation: &sharedPresentationState{runtime: owner, turnID: "turn-2"}}
	// Views must not use a private request copy, even if one exists.
	first.shared.requests = map[string]Event{"private": {TurnID: "turn-1"}}
	for _, test := range []struct {
		view *interactiveState
		want map[string]bool
	}{
		{first, map[string]bool{"first": true, "unbound": true}},
		{second, map[string]bool{"second": true, "unbound": true}},
		{&interactiveState{sharedPresentation: &sharedPresentationState{runtime: owner}}, map[string]bool{"first": true, "second": true, "unbound": true}},
	} {
		if got := sharedPresentationPending(test.view); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("pending=%v want %v", got, test.want)
		}
	}
	owner.mu.Lock()
	delete(owner.shared.requests, "first")
	owner.mu.Unlock()
	if got := sharedPresentationPending(first); !reflect.DeepEqual(got, map[string]bool{"unbound": true}) {
		t.Fatalf("resolved request remained in presentation: %v", got)
	}
}

func TestSharedPresentationHelpersAllowOrdinarySession(t *testing.T) {
	state := &interactiveState{}
	if len(sharedPresentationPending(state)) != 0 || sharedPresentationIsCurrent(state) {
		t.Fatal("ordinary session acquired shared presentation state")
	}
	cancelSharedPresentationTurn(state)
}

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
