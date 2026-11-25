package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestIssue2196Lifecycle_RejectedPrimaryDoesNotPoisonNextCommand(t *testing.T) {
	for _, disposition := range []string{"started", "handled"} {
		t.Run(disposition, func(t *testing.T) {
			session, wire := issue2161UnitSession(t)
			command, done := issue2161UnitStartSend(t, session, wire, "rejected work")
			session.handleEvent(map[string]any{"type": "agent_start"})
			session.handleEvent(issue2161UnitAssistant("old provider error"))
			session.handleEvent(map[string]any{"type": "agent_end"})
			session.handleEvent(issue2161UnitResponse(command, false, "prompt rejected"))
			issue2161UnitWaitSend(t, done, "prompt rejected")
			issue2161UnitNoTerminal(t, session, "rejected primary")
			next, nextDone := issue2161UnitStartSend(t, session, wire, "successful work")
			if disposition == "handled" {
				session.handleEvent(issue2161UnitText("command succeeded"))
			}
			session.handleEvent(issue2161UnitResponse(next, true, disposition))
			issue2161UnitWaitSend(t, nextDone, "")
			if disposition == "started" {
				session.handleEvent(map[string]any{"type": "agent_start"})
				session.handleEvent(issue2161UnitText("command succeeded"))
				session.handleEvent(map[string]any{"type": "agent_settled"})
			}
			events := drainEvents(session)
			if len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult {
				t.Fatalf("successful command inherited the rejected primary's error: %+v", events)
			}
		})
	}
}

func TestIssue2196Lifecycle_RejectedSupplementPreservesCurrentError(t *testing.T) {
	session, wire := issue2161UnitSession(t)
	command, done := issue2161UnitStartSend(t, session, wire, "primary")
	session.handleEvent(issue2161UnitResponse(command, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	session.handleEvent(map[string]any{"type": "agent_start"})
	session.handleEvent(issue2161UnitAssistant("current provider error"))
	supplement, supplementDone := issue2161UnitStartSend(t, session, wire, "supplement")
	session.handleEvent(issue2161UnitResponse(supplement, false, "supplement rejected"))
	issue2161UnitWaitSend(t, supplementDone, "supplement rejected")
	issue2161UnitNoTerminal(t, session, "rejected supplement")
	session.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(session)
	if len(events) != 1 || events[0].Type != core.EventError || events[0].Error == nil || events[0].Error.Error() != "current provider error" {
		t.Fatalf("supplement rejection dropped the original run's error: %+v", events)
	}
}

func TestIssue2196Lifecycle_StaleRejectionPreservesCurrentError(t *testing.T) {
	session, wire := issue2161UnitSession(t)
	oldCommand, oldDone := issue2161UnitStartSend(t, session, wire, "old primary")
	session.handleEvent(map[string]any{"type": "agent_start"})
	session.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(session); len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("old run did not complete: %+v", events)
	}
	command, done := issue2161UnitStartSend(t, session, wire, "new primary")
	session.handleEvent(issue2161UnitResponse(command, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	session.handleEvent(map[string]any{"type": "agent_start"})
	session.handleEvent(issue2161UnitAssistant("new run error"))
	session.handleEvent(issue2161UnitResponse(oldCommand, false, "late rejection"))
	issue2161UnitWaitSend(t, oldDone, "late rejection")
	issue2161UnitNoTerminal(t, session, "stale rejection")
	session.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(session)
	if len(events) != 1 || events[0].Type != core.EventError || events[0].Error == nil || events[0].Error.Error() != "new run error" {
		t.Fatalf("stale rejection dropped the current run's error: %+v", events)
	}
}
