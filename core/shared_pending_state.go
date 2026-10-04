package core

import "log/slog"

func sharedPresentationIsCurrent(view *interactiveState) bool {
	view.mu.Lock()
	id := view.sharedTurnID
	as, ok := view.agentSession.(SharedAgentSession)
	view.mu.Unlock()
	return ok && id != "" && as.RuntimeState().TurnID == id
}

func cancelSharedPresentationTurn(view *interactiveState) {
	view.mu.Lock()
	id := view.sharedTurnID
	as := view.agentSession
	view.mu.Unlock()
	if canceller, ok := as.(AgentTurnCanceller); ok && id != "" {
		if err := canceller.CancelExpectedTurn(id); err != nil {
			slog.Warn("shared presentation timeout interrupt", "turn_id", id, "error", err)
		}
	}
}

// Presentation views never own request lifecycle. New views inherit outstanding
// requests, including approvals replayed before the first external tool event.
func sharedPresentationPending(view *interactiveState) map[string]bool {
	pending := map[string]bool{}
	owner := view.sharedRuntime
	if owner == nil {
		return pending
	}
	view.mu.Lock()
	turnID := view.sharedTurnID
	view.mu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for id, event := range owner.sharedRequests {
		if turnID == "" || event.TurnID == "" || event.TurnID == turnID {
			pending[id] = true
		}
	}
	return pending
}
