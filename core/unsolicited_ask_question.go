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

// unsolicitedQuestions puts the questions that arrive between user turns —
// typically in a turn a finished background subagent woke — in front of the
// user, as a foreground turn does. Unlike a tool permission there is nothing
// to decide on the user's behalf: denying a question only tells the agent the
// user refused to answer, and approve-all cannot help either, since approving
// the request unanswered hands the agent no answers.
//
// It never blocks the reader: a question is shown and left pending, and the
// reader keeps relaying every other event while the user thinks. The answer
// arrives through handlePendingPermission, which responds to the agent and
// resolves the request; the reader then shows the next question. Questions
// are shown one at a time, since a second prompt would replace state.pending
// and orphan the first. While one waits, a message that cannot answer it
// does not start a turn (see holdForUnsolicitedQuestion). Questions still
// unanswered when the reader exits are withdrawn.
//
// Only the reader goroutine calls its methods.
type unsolicitedQuestions struct {
	e            *Engine
	state        *interactiveState
	agentSession AgentSession
	current      *pendingPermission // shown, waiting for its answer
	queued       []Event            // asked while current waits
}

func (e *Engine) newUnsolicitedQuestions(state *interactiveState, agentSession AgentSession) *unsolicitedQuestions {
	return &unsolicitedQuestions{e: e, state: state, agentSession: agentSession}
}

// ask shows event's question, or queues it while another one waits.
func (q *unsolicitedQuestions) ask(event Event) {
	if q.current != nil {
		q.queued = append(q.queued, event)
		slog.Info("unsolicited: question queued",
			"request_id", event.RequestID,
			"waiting_on", q.current.RequestID)
		return
	}
	q.show(event)
}

func (q *unsolicitedQuestions) show(event Event) {
	pending := &pendingPermission{
		RequestID:    event.RequestID,
		ToolName:     event.ToolName,
		ToolInput:    event.ToolInputRaw,
		InputPreview: event.ToolInput,
		Questions:    event.Questions,
		Resolved:     make(chan struct{}),
	}
	q.state.mu.Lock()
	q.state.pending = pending
	p, replyCtx := q.state.platform, q.state.replyCtx
	q.state.mu.Unlock()
	q.current = pending

	slog.Info("unsolicited: asking user",
		"request_id", event.RequestID,
		"tool", event.ToolName,
		"questions", len(event.Questions))
	q.e.sendAskQuestionPrompt(p, replyCtx, event.Questions, 0)
}

// answered is closed once the shown question is resolved; nil (never ready in
// a select) while none is shown.
func (q *unsolicitedQuestions) answered() <-chan struct{} {
	if q.current == nil {
		return nil
	}
	return q.current.Resolved
}

// next finishes the resolved question and, unless ctx is done, shows the
// next queued one.
func (q *unsolicitedQuestions) next(ctx context.Context) {
	slog.Info("unsolicited: question answered", "request_id", q.current.RequestID)
	q.current = nil
	if len(q.queued) == 0 || ctx.Err() != nil {
		return
	}
	event := q.queued[0]
	q.queued = q.queued[1:]
	q.show(event)
}

// withdraw denies the questions still unanswered when the reader exits (a
// new turn or session cleanup took over the session, or the agent went away),
// so the agent is not left waiting on prompts nobody owns.
func (q *unsolicitedQuestions) withdraw() {
	var ids []string
	if q.current != nil && claimPending(q.state, q.current) {
		q.current.resolve()
		ids = append(ids, q.current.RequestID)
	}
	for _, event := range q.queued {
		ids = append(ids, event.RequestID)
	}
	q.current, q.queued = nil, nil
	if len(ids) == 0 {
		return
	}
	slog.Info("unsolicited: questions withdrawn", "request_ids", ids)
	// RespondPermission may make a slow adapter call, and the reader is on
	// its way out (stopUnsolicitedReader waits for it with a bound), so the
	// denials are sent off the reader goroutine.
	agentSession := q.agentSession
	go func() {
		for _, id := range ids {
			if err := agentSession.RespondPermission(id, PermissionResult{
				Behavior: "deny",
				Message:  "The user did not answer before the conversation moved on.",
			}); err != nil {
				slog.Debug("unsolicited: failed to withdraw question", "request_id", id, "error", err)
			}
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
