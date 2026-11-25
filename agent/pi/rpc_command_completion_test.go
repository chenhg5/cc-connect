package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPiRPCExtensionCommandCompletesWithoutAgentEnd(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.rpcPromptPending.Store(true)

	s.handleEvent(map[string]any{"type": "extension_ui_request", "id": "notice-1", "method": "notify", "message": "memory found", "notifyType": "info"})
	s.handleEvent(map[string]any{"type": "response", "command": "prompt", "success": true, "data": map[string]any{"disposition": "handled"}})

	events := drainEvents(s)
	if len(events) != 2 || events[0].Type != core.EventText || events[0].Content != "memory found" || events[1].Type != core.EventResult {
		t.Fatalf("extension command events = %#v; want notice then completion", events)
	}
}

func TestPiRPCStartedAndQueuedPromptWaitForAgentEnd(t *testing.T) {
	for _, disposition := range []string{"started", "queued"} {
		t.Run(disposition, func(t *testing.T) {
			s := newTestSession(true)
			defer s.cancel()
			s.handleEvent(map[string]any{"type": "response", "command": "prompt", "success": true, "data": map[string]any{"disposition": disposition}})
			if got := drainEvents(s); len(got) != 0 {
				t.Fatalf("prompt response emitted completion before agent_end: %#v", got)
			}
		})
	}
}

func TestPiRPCPromptRejectionReturnsErrorAndCompletes(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.handleEvent(map[string]any{"type": "response", "command": "prompt", "success": false, "error": "command rejected"})
	events := drainEvents(s)
	if len(events) != 2 || events[0].Type != core.EventError || events[1].Type != core.EventResult {
		t.Fatalf("rejected prompt events = %#v; want error then completion", events)
	}
}

func TestPiRPCHandledResponseDoesNotCompleteActiveTurnTwice(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(map[string]any{"type": "response", "command": "prompt", "success": true, "data": map[string]any{"disposition": "handled"}})
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("handled extension closed an active agent turn: %#v", events)
	}
	s.handleEvent(map[string]any{"type": "agent_end"})
	s.handleEvent(map[string]any{"type": "response", "command": "prompt", "success": true, "data": map[string]any{"disposition": "handled"}})
	if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("duplicate completion after agent_end: %#v", events)
	}
}
