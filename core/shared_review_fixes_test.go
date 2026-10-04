package core

import (
	"strings"
	"testing"
	"time"
)

func TestCUJ_C7_LateApprovalSurvivesPresentationTimeout(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.eventIdleTimeout = 120 * time.Millisecond
	env.e.maxTurnTime = 180 * time.Millisecond
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, TurnID: "active-first", RequestID: "late", ToolName: "Bash", ToolInput: "WAITING-APPROVAL"})
	env.await("WAITING-APPROVAL")
	// The presentation is only created after the already displayed approval.
	as.emit(Event{Type: EventToolOutput, TurnID: "active-first", ItemID: "parallel", ToolName: "Bash", Content: "PARALLEL LIVE"})
	env.await("PARALLEL LIVE")
	time.Sleep(360 * time.Millisecond)
	env.send("/steer still waiting")
	env.await("STEER observed: still waiting")
	if strings.Contains(env.visible(), "timed out") || strings.Contains(env.visible(), "interrupted") {
		t.Fatalf("approval wait cancelled task: %s", env.visible())
	}
	as.emit(Event{Type: EventPermissionResolved, TurnID: "active-first", RequestID: "late"})
	// Human wait must not consume the max-turn budget on resolution.
	as.emit(Event{Type: EventText, TurnID: "active-first", Content: "CONTINUED AFTER APPROVAL", Metadata: map[string]any{"phase": "commentary"}})
	time.Sleep(30 * time.Millisecond)
	as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "APPROVAL FINAL", Done: true})
	env.await("APPROVAL FINAL")
	env.send("/history")
	env.await("APPROVAL FINAL")
	env.send("/detach")
	env.await("Detached")
	if strings.Contains(env.visible(), "timed out") {
		t.Fatalf("resolution triggered overdue timer: %s", env.visible())
	}
}

func TestSharedLegacyCompletedToolDoesNotReturnToInProgress(t *testing.T) {
	for _, final := range []string{"FINAL A", ""} {
		t.Run("result="+final, func(t *testing.T) {
			env := newSharedTestEnv(t)
			env.e.display.CardMode = "legacy"
			env.send("/attach first")
			env.await("Attached to session first")
			as := env.a.connection("first")
			as.emit(Event{Type: EventToolOutput, TurnID: "active-first", ItemID: "a", ToolName: "Bash", Content: "STALE A LIVE"})
			env.await("STALE A LIVE")
			before := strings.Count(strings.Join(env.p.getSent(), "\n"), "STALE A LIVE")
			as.emit(Event{Type: EventToolResult, TurnID: "active-first", ItemID: "a", ToolName: "Bash", ToolResult: final, ToolStatus: "completed"})
			as.emit(Event{Type: EventToolOutput, TurnID: "active-first", ItemID: "b", ToolName: "Bash", Content: "B LIVE"})
			env.await("B LIVE")
			after := strings.Count(strings.Join(env.p.getSent(), "\n"), "STALE A LIVE")
			if after != before {
				t.Fatalf("completed A resent with B: %s", env.visible())
			}
			as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "TOOLS FINAL", Done: true})
			env.await("TOOLS FINAL")
		})
	}
}

func TestCUJ_C7_RecoveredTerminalResultDrainsQueue(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.display.ToolMessages = false
	env.send("first work")
	first := awaitSharedCompatCall(t, a)
	first.session.emit(Event{Type: EventText, TurnID: first.turn, Content: "INCOMPLETE BEFORE DISCONNECT", Metadata: map[string]any{"phase": "final_answer"}})
	env.send("queued work")
	// Model adapter emits only the reconciled EventResult, without a text event.
	first.session.mu.Lock()
	first.session.turn = ""
	first.session.mu.Unlock()
	first.session.emit(Event{Type: EventResult, TurnID: first.turn, Content: "RECOVERED ANSWER", Done: true, Metadata: map[string]any{"result_authoritative": true}})
	second := awaitSharedCompatCall(t, a)
	if second.prompt != "queued work" {
		t.Fatalf("wrong queued request: %q", second.prompt)
	}
	second.finish("QUEUE FINISHED")
	env.await("QUEUE FINISHED")
	env.send("/history")
	env.await("RECOVERED ANSWER")
}
