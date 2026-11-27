package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Preserve upstream #2390 handled command feedback, but use the
// correlated request ownership and settled terminal from #2196.
func TestPiRPCExtensionCommandCompletesWithoutAgentEnd(t *testing.T) {
	s, w := issue2161UnitSession(t)
	cmd, done := issue2161UnitStartSend(t, s, w, "/memory")
	s.handleEvent(map[string]any{"type": "extension_ui_request", "id": "notice-1", "method": "notify", "message": "memory found", "notifyType": "info"})
	s.handleEvent(issue2161UnitResponse(cmd, true, "handled"))
	issue2161UnitWaitSend(t, done, "")
	events := drainEvents(s)
	if len(events) != 2 || events[0].Type != core.EventText || events[0].Content != "memory found" || events[1].Type != core.EventResult {
		t.Fatalf("extension command events = %#v; want notice then completion", events)
	}
}

func TestPiRPCStartedAndQueuedPromptWaitForAgentSettled(t *testing.T) {
	s, w := issue2161UnitSession(t)
	cmd, done := issue2161UnitStartSend(t, s, w, "primary")
	s.handleEvent(issue2161UnitResponse(cmd, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	cmd, done = issue2161UnitStartSend(t, s, w, "supplement")
	s.handleEvent(issue2161UnitResponse(cmd, true, "queued"))
	issue2161UnitWaitSend(t, done, "")
	s.handleEvent(map[string]any{"type": "agent_end"})
	issue2161UnitNoTerminal(t, s, "agent_end")
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("wanted one settled terminal: %+v", events)
	}
}

func TestPiRPCPromptRejectionReturnsErrorWithoutDuplicateTerminal(t *testing.T) {
	s, w := issue2161UnitSession(t)
	cmd, done := issue2161UnitStartSend(t, s, w, "rejected")
	s.handleEvent(issue2161UnitResponse(cmd, false, "command rejected"))
	issue2161UnitWaitSend(t, done, "command rejected")
	issue2161UnitNoTerminal(t, s, "rejected primary via Send")
}

func TestPiRPCHandledResponseDoesNotCompleteActiveTurnTwice(t *testing.T) {
	s, w := issue2161UnitSession(t)
	cmd, done := issue2161UnitStartSend(t, s, w, "primary")
	s.handleEvent(issue2161UnitResponse(cmd, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	cmd, done = issue2161UnitStartSend(t, s, w, "/memory")
	s.handleEvent(issue2161UnitResponse(cmd, true, "handled"))
	issue2161UnitWaitSend(t, done, "")
	issue2161UnitNoTerminal(t, s, "handled supplement")
	s.handleEvent(map[string]any{"type": "agent_end"})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("wanted one terminal: %+v", events)
	}
}
