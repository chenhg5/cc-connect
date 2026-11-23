package core

import "context"

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
