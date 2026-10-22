package core

import (
	"context"
	"log/slog"
)

// isAskQuestionRequest reports whether a permission request is really a
// question for the user (Claude Code's AskUserQuestion, or a Pi
// extension_select rendered the same way) rather than a tool permission.
func isAskQuestionRequest(event Event) bool {
	return event.Type == EventPermissionRequest &&
		(event.ToolName == "AskUserQuestion" || event.ToolName == "extension_select") &&
		len(event.Questions) > 0
}

// askUnsolicitedQuestion puts a question that arrives between user turns —
// typically from a background subagent that outlived the turn which started
// it — in front of the user and waits for the answer, as a foreground turn
// does. Unlike a tool permission there is nothing to decide on the user's
// behalf: denying the question only tells the agent the user refused to
// answer, and approve-all cannot help either, since approving the request
// unanswered hands the agent no answers.
//
// The answer arrives through handlePendingPermission, which responds to the
// agent and resolves the request. If ctx is cancelled first (a new foreground
// turn or session cleanup took over the session), a question still pending is
// withdrawn and denied so the agent is not left waiting on a prompt nobody
// owns.
func (e *Engine) askUnsolicitedQuestion(ctx context.Context, state *interactiveState, agentSession AgentSession, p Platform, replyCtx any, event Event) {
	pending := &pendingPermission{
		RequestID:    event.RequestID,
		ToolName:     event.ToolName,
		ToolInput:    event.ToolInputRaw,
		InputPreview: event.ToolInput,
		Questions:    event.Questions,
		Resolved:     make(chan struct{}),
	}
	state.mu.Lock()
	state.pending = pending
	state.mu.Unlock()

	slog.Info("unsolicited: asking user",
		"request_id", event.RequestID,
		"tool", event.ToolName,
		"questions", len(event.Questions))
	e.sendAskQuestionPrompt(p, replyCtx, event.Questions, 0)

	select {
	case <-pending.Resolved:
		slog.Info("unsolicited: question answered", "request_id", event.RequestID)
		return
	case <-ctx.Done():
	}

	state.mu.Lock()
	withdrawn := state.pending == pending
	if withdrawn {
		state.pending = nil
	}
	state.mu.Unlock()
	if !withdrawn {
		// Answered or cleaned up while ctx was being cancelled.
		return
	}
	pending.resolve()
	slog.Info("unsolicited: question withdrawn", "request_id", event.RequestID)
	go func() {
		if err := agentSession.RespondPermission(event.RequestID, PermissionResult{
			Behavior: "deny",
			Message:  "The user did not answer before the conversation moved on.",
		}); err != nil {
			slog.Debug("unsolicited: failed to withdraw question", "error", err)
		}
	}()
}
