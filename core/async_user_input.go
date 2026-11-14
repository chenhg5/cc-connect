package core

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// AsyncQuestion belongs to a conversation, not one execution turn. New questions
// replace it. Completed and uncertain submissions remain deduplication records.
type AsyncQuestion struct {
	ID        string         `json:"id"`
	Questions []UserQuestion `json:"questions"`
	Answers   []string       `json:"answers"`
	Current   int            `json:"current"`
	Status    string         `json:"status"` // open, sending, submitted, unknown
}

func cloneAsyncQuestion(q *AsyncQuestion) *AsyncQuestion {
	if q == nil {
		return nil
	}
	c := *q
	c.Answers = append([]string(nil), q.Answers...)
	c.Questions = append([]UserQuestion(nil), q.Questions...)
	for i := range c.Questions {
		c.Questions[i].Options = append([]UserQuestionOption(nil), q.Questions[i].Options...)
	}
	return &c
}
func (s *Session) asyncQuestion() *AsyncQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAsyncQuestion(s.AsyncQuestion)
}

func (e *Engine) showAsyncUserInput(state *interactiveState, p Platform, replyCtx any, event Event, managers ...*SessionManager) {
	if event.RequestID == "" || len(event.Questions) == 0 {
		return
	}
	state.mu.Lock()
	session := state.busySession
	state.mu.Unlock()
	if session == nil {
		return
	}
	sm := e.sessions
	if len(managers) > 0 {
		sm = managers[0]
	}
	q := &AsyncQuestion{ID: base64.RawURLEncoding.EncodeToString([]byte(event.RequestID)), Questions: event.Questions, Answers: make([]string, len(event.Questions)), Status: "open"}
	session.mu.Lock()
	session.AsyncQuestion = cloneAsyncQuestion(q)
	session.mu.Unlock()
	sm.Save()
	e.sendAskQuestionPrompt(p, replyCtx, q.Questions, 0, q.ID)
}

// Find only within this platform/user conversation namespace.
func findAsyncQuestionSession(sm *SessionManager, key, content string) *Session {
	parts := strings.Split(content, ":")
	if len(parts) != 4 || parts[0] != "askq" {
		return nil
	}
	for _, session := range sm.ListSessions(key) {
		if q := session.asyncQuestion(); q != nil && q.ID == parts[1] {
			return session
		}
	}
	return nil
}

// Runs under admissionMu. Old cards remain bound to their original conversation.
func (e *Engine) selectAsyncQuestionSession(p Platform, msg *Message, content, key string, sm *SessionManager) bool {
	target := findAsyncQuestionSession(sm, msg.SessionKey, content)
	if isAsyncAnswer(content) {
		var ambiguous bool
		target, ambiguous = findFreeTextQuestionSession(sm, msg.SessionKey)
		if ambiguous {
			e.answerFeedback(p, msg, nil, MsgAnswerAmbiguous)
			return false
		}
	}
	if target == nil || target.ID == sm.ActiveSessionID(msg.SessionKey) {
		return true
	}
	q := target.asyncQuestion()
	if q.Status != "open" {
		notice := MsgMessageSteerUnconfirmed
		if q.Status == "submitted" {
			notice = MsgAnswerSubmitted
		}
		e.answerFeedback(p, msg, q, notice)
		return false
	}
	active := sm.FindByID(sm.ActiveSessionID(msg.SessionKey))
	if active != nil && active.Busy() {
		e.answerFeedback(p, msg, q, MsgPreviousProcessing)
		return false
	}
	e.answerFeedback(p, msg, q, MsgAnswerSwitching)
	e.retireAsyncQuestionSession(key)
	if _, err := sm.SwitchSession(msg.SessionKey, target.ID); err != nil {
		e.answerFeedback(p, msg, q, MsgAskQuestionStale)
		return false
	}
	return true
}

func (e *Engine) answerFeedback(p Platform, msg *Message, q *AsyncQuestion, key MsgKey) {
	text := e.i18n.T(key)
	color := "orange"
	if key == MsgAnswerSubmitted {
		color = "green"
	}
	card := NewCard().Title(text, color)
	if q != nil && q.Current >= 0 && q.Current < len(q.Questions) {
		card.Markdown(q.Questions[q.Current].Question)
		if q.Current < len(q.Answers) {
			card.Markdown(q.Answers[q.Current])
		}
	}
	if msg.OnAnswerStatus != nil {
		msg.OnAnswerStatus(card.Build())
	} else {
		e.reply(p, msg.ReplyCtx, text)
	}
}

// Only Send/steer acknowledgement is success; admission to a queue isn't.
type answerDelivery struct {
	once     sync.Once
	finishFn func(error)
	admitted bool              // admission goroutine only
	state    *interactiveState // captured before launching an idle answer worker
}

func (d *answerDelivery) finish(err error) {
	if d != nil {
		d.once.Do(func() { d.finishFn(err) })
	}
}

func isAsyncAnswer(content string) bool {
	return content == "/answer" || strings.HasPrefix(content, "/answer ")
}

func (e *Engine) handleAsyncUserInput(p Platform, msg *Message, content, key string, sessions *SessionManager) (string, bool) {
	parts := strings.Split(content, ":")
	callback := len(parts) == 4 && parts[0] == "askq"
	explicit := isAsyncAnswer(content)
	if !callback && !explicit {
		return content, false
	}
	if explicit {
		content = strings.TrimSpace(strings.TrimPrefix(content, "/answer"))
	}
	session := sessions.FindByID(sessions.ActiveSessionID(msg.SessionKey))
	if session == nil {
		e.answerFeedback(p, msg, nil, MsgAskQuestionStale)
		return content, true
	}
	session.mu.Lock()
	q := cloneAsyncQuestion(session.AsyncQuestion)
	if q == nil || q.Current < 0 || q.Current >= len(q.Questions) || len(q.Answers) != len(q.Questions) || (callback && parts[1] != q.ID) {
		session.mu.Unlock()
		e.answerFeedback(p, msg, nil, MsgAskQuestionStale)
		return content, true
	}
	if callback {
		index, ie := strconv.Atoi(parts[2])
		option, oe := strconv.Atoi(parts[3])
		if ie != nil || oe != nil || index != q.Current || option < 1 || option > len(q.Questions[index].Options) {
			session.mu.Unlock()
			e.answerFeedback(p, msg, nil, MsgAskQuestionStale)
			return content, true
		}
		content = strconv.Itoa(option)
	}
	if q.Status != "open" {
		session.mu.Unlock()
		notice := MsgMessageSteerUnconfirmed
		if q.Status == "submitted" {
			notice = MsgAnswerSubmitted
		}
		e.answerFeedback(p, msg, q, notice)
		return content, true
	}
	if content == "" || len(msg.Images) > 0 || len(msg.Files) > 0 {
		session.mu.Unlock()
		e.answerFeedback(p, msg, q, MsgPsSendFailed)
		return content, true
	}
	q.Answers[q.Current] = e.resolveAskQuestionAnswer(q.Questions[q.Current], content)
	if q.Current+1 < len(q.Questions) {
		display := cloneAsyncQuestion(q)
		q.Current++
		session.AsyncQuestion = q
		session.mu.Unlock()
		sessions.Save()
		e.answerFeedback(p, msg, display, MsgAnswerRecorded)
		e.sendAskQuestionPrompt(p, msg.ReplyCtx, q.Questions, q.Current, q.ID)
		return content, true
	}
	q.Status = "sending"
	session.AsyncQuestion = q
	session.mu.Unlock()
	sessions.Save()
	var lines []string
	for i, question := range q.Questions {
		lines = append(lines, question.Question+": "+q.Answers[i])
	}
	prompt := strings.Join(lines, "\n")
	delivery := &answerDelivery{finishFn: func(err error) {
		status, notice := "submitted", MsgAnswerSubmitted
		if err != nil {
			status, notice = "unknown", MsgMessageSteerUnconfirmed
			if errors.Is(err, ErrSteerRejected) {
				status, notice = "open", MsgPsSendFailed
			}
		}
		session.mu.Lock()
		if current := session.AsyncQuestion; current != nil && current.ID == q.ID {
			current.Status = status
		}
		session.mu.Unlock()
		sessions.Save()
		e.answerFeedback(p, msg, q, notice)
	}}
	e.answerFeedback(p, msg, q, MsgAnswerSubmitting)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	var agentSession AgentSession
	if state != nil {
		state.mu.Lock()
		if !state.stopped {
			agentSession = state.agentSession
		}
		state.mu.Unlock()
	}
	if sender, ok := agentSession.(UserInputSteerer); ok {
		steered, err := sender.SteerUserInput(prompt)
		if err != nil || steered {
			if steered || !errors.Is(err, ErrSteerRejected) {
				session.AddHistory("user", prompt)
				if err != nil {
					session.AddHistory("assistant", e.i18n.T(MsgMessageSteerUnconfirmed))
				}
				sessions.Save()
			}
			delivery.finish(err)
			return prompt, true
		}
	}
	// The click is a new instruction in the same conversation; don't replay the
	// old task. Reuse normal atomic admission and lifecycle for idle continuation.
	session.MarkExplicitlyActivated()
	msg.answerDelivery = delivery
	previous := msg.OnAccepted
	msg.OnAccepted = func() {
		delivery.admitted = true
		if previous != nil {
			previous()
		}
	}
	return prompt, false
}

// Free text carries no question ID. Prefer the active question (including its
// deduplication record); otherwise switch only when exactly one open question
// exists in this user's conversation namespace.
func findFreeTextQuestionSession(sm *SessionManager, key string) (*Session, bool) {
	if active := sm.FindByID(sm.ActiveSessionID(key)); active != nil && active.asyncQuestion() != nil {
		return active, false
	}
	var target *Session
	for _, session := range sm.ListSessions(key) {
		if q := session.asyncQuestion(); q != nil && q.Status == "open" {
			if target != nil {
				return nil, true
			}
			target = session
		}
	}
	return target, false
}

// Detach/register atomically while admission is serialized, then retire the
// reader and process outside the admission gate. The next worker waits through
// awaitSessionClose before resuming the question's original model thread.
func (e *Engine) retireAsyncQuestionSession(key string) {
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	if state == nil {
		e.interactiveMu.Unlock()
		return
	}
	state.markStopped()
	state.mu.Lock()
	old, p, replyCtx := state.agentSession, state.platform, state.replyCtx
	state.agentSession = nil
	if state.agentSessionIdleCancel != nil {
		state.agentSessionIdleCancel()
		state.agentSessionIdleCancel = nil
	}
	pending := state.pending
	state.pending = nil
	state.mu.Unlock()
	finish := e.beginSessionClose(key)
	delete(e.interactiveStates, key)
	e.interactiveMu.Unlock()
	go func() {
		defer finish()
		e.stopUnsolicitedReader(state)
		if pending != nil {
			pending.resolve()
		}
		e.notifyDroppedQueuedMessages(state, ErrSteerRejected)
		e.closeAgentSession(key, old, p, replyCtx)
		if old != nil && old.Alive() {
			e.markUnsafeResume(key)
		}
	}()
}
