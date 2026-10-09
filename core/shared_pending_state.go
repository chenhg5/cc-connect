package core

import "log/slog"

// sharedPresentationState binds one turn's display to its persistent observer.
// The runtime pointer is immutable after construction; turnID is protected by
// the view's interactiveState.mu. Requests remain owned by runtime.shared.
type sharedPresentationState struct {
	runtime *interactiveState
	turnID  string
}

func sharedPresentationIsCurrent(view *interactiveState) bool {
	view.mu.Lock()
	if view.sharedPresentation == nil {
		view.mu.Unlock()
		return false
	}
	id := view.sharedPresentation.turnID
	as, ok := view.agentSession.(SharedAgentSession)
	view.mu.Unlock()
	return ok && id != "" && as.RuntimeState().TurnID == id
}

func cancelSharedPresentationTurn(view *interactiveState) {
	view.mu.Lock()
	if view.sharedPresentation == nil {
		view.mu.Unlock()
		return
	}
	id := view.sharedPresentation.turnID
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
	if view.sharedPresentation == nil {
		return pending
	}
	owner := view.sharedPresentation.runtime
	if owner == nil {
		return pending
	}
	view.mu.Lock()
	turnID := view.sharedPresentation.turnID
	view.mu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for id, event := range owner.shared.requests {
		if turnID == "" || event.TurnID == "" || event.TurnID == turnID {
			pending[id] = true
		}
	}
	return pending
}
