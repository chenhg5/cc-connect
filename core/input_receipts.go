package core

import (
	"context"
	"log/slog"
)

func reportDroppedInput(ctx context.Context, p Platform, reply any, reason string) {
	if reporter, ok := p.(interface {
		ReportDropped(context.Context, any, string)
	}); ok {
		reporter.ReportDropped(ctx, reply, reason)
	}
}

func reportQueuedInput(ctx context.Context, p Platform, reply any) {
	if reporter, ok := p.(interface{ ReportInputQueued(context.Context, any) }); ok {
		reporter.ReportInputQueued(ctx, reply)
	}
}

func reportStartedInput(ctx context.Context, p Platform, reply any) {
	if reporter, ok := p.(interface{ ReportInputStarted(context.Context, any) }); ok {
		reporter.ReportInputStarted(ctx, reply)
	}
}

func (e *Engine) discardQueuedInputs(state *interactiveState, reason string) {
	state.mu.Lock()
	remaining := state.pendingMessages
	state.pendingMessages = nil
	state.mu.Unlock()
	for _, q := range remaining {
		reportDroppedInput(e.ctx, q.platform, q.replyCtx, reason)
	}
}

// popStaleQueuedInputsLocked detaches the stale prefix from the queue with
// state.mu held. Keep platform callbacks outside that lock.
func (e *Engine) popStaleQueuedInputsLocked(state *interactiveState) []queuedMessage {
	n := 0
	for n < len(state.pendingMessages) && e.isQueuedUserMessageStaleForDrainLocked(state, state.pendingMessages[n].userMessageTimeMs) {
		n++
	}
	stale := state.pendingMessages[:n]
	state.pendingMessages = state.pendingMessages[n:]
	return stale
}

func (e *Engine) reportStaleQueuedInputs(stale []queuedMessage, sessionKey, phase string) {
	if len(stale) == 0 {
		return
	}
	slog.Info("dropped stale queued user messages "+phase, "session", sessionKey, "dropped", len(stale))
	for _, q := range stale {
		reportDroppedInput(e.ctx, q.platform, q.replyCtx, "stale")
	}
}
