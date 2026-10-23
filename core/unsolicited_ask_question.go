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
// agent and resolves the request. While the question waits, a message that
// cannot answer it does not start a turn (see holdForUnsolicitedQuestion), so
// the woken turn keeps the session's events. If ctx is cancelled first anyway
// (session cleanup, or a turn started some other way), a question still
// pending is withdrawn and denied so the agent is not left waiting on a prompt
// nobody owns.
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

	if !claimPending(state, pending) {
		// Being answered, or dropped by session cleanup: whoever claimed the
		// request responds to it.
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

// claimPending takes pending off state if it is still the request waiting
// there. Answering a background question races with withdrawing it; only the
// side that claims the request responds to the agent.
func claimPending(state *interactiveState, pending *pendingPermission) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending != pending {
		return false
	}
	state.pending = nil
	return true
}

// holdForUnsolicitedQuestion handles a message without text (attachments
// only) that arrives while the unsolicited reader waits on a question. Such a
// message cannot answer it, and starting a turn would take the session's
// events from the woken turn: its remaining output, once the question is
// withdrawn, would be read as the new turn's reply. So the user is asked to
// answer first instead. It reports whether msg was handled; a question asked
// by a foreground turn is left to the busy-session queue.
func (e *Engine) holdForUnsolicitedQuestion(p Platform, msg *Message, state *interactiveState, pending *pendingPermission) bool {
	if len(msg.Images) == 0 && len(msg.Files) == 0 {
		return false
	}
	state.mu.Lock()
	held := state.pending == pending && state.unsolicitedCancel != nil
	state.mu.Unlock()
	if !held {
		return false
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAskQuestionAnswerFirst))
	e.sendAskQuestionPrompt(p, msg.ReplyCtx, pending.Questions, pending.CurrentQuestion)
	return true
}
