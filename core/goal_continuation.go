package core

import "strings"

// nextInteractiveTurnLocked selects the next turn without inserting internal
// prompts into the user queue. state.mu must be held across this decision.
func nextInteractiveTurnLocked(state *interactiveState) (queuedMessage, bool, bool) {
	if state.stopped {
		return queuedMessage{}, false, false
	}
	if len(state.pendingMessages) > 0 {
		next := state.pendingMessages[0]
		state.pendingMessages = state.pendingMessages[1:]
		return next, false, true
	}
	provider, ok := state.agentSession.(GoalContinuationProvider)
	if !ok || !provider.GoalContinuationEnabled() {
		return queuedMessage{}, false, false
	}
	status, known := provider.GoalStatus()
	limit := provider.GoalContinuationLimit()
	prompt := strings.TrimSpace(provider.GoalContinuationPrompt())
	if !known || status != "active" || prompt == "" || limit < 0 || (limit > 0 && state.goalContinuations >= limit) {
		return queuedMessage{}, false, false
	}
	state.goalContinuations++
	return queuedMessage{content: prompt, platform: state.platform, replyCtx: state.replyCtx}, true, true
}
