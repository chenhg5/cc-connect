package core

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
)

// steerMessageForBusySession returns false only when normal queueing is safe.
func (e *Engine) steerMessageForBusySession(p Platform, msg *Message, key string, session *Session, sessions *SessionManager) bool {
	if strings.TrimSpace(msg.Content) == "" || len(msg.Images) != 0 || len(msg.Files) != 0 {
		return false
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	if state == nil {
		e.interactiveMu.Unlock()
		return false
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	if len(state.pendingMessages) > 0 {
		state.mu.Unlock()
		return false
	}
	if e.isStaleUserMessageLocked(state, msg.UserMessageTimeMs) {
		snapshot := userMessageWatermarkSnapshotLocked(state)
		state.mu.Unlock()
		e.logStaleUserMessageDropped("reject_before_steer", msg, key, snapshot)
		return true
	}
	agentSession := state.agentSession
	state.mu.Unlock()
	if agentSession == nil || !agentSession.Alive() {
		return false
	}
	steerer, ok := agentSession.(UserInputSteerer)
	if !ok {
		return false
	}
	prompt := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	steered, err := steerer.SteerUserInput(prompt)
	if errors.Is(err, ErrSteerRejected) {
		msg.steerRejected = true
	}
	if errors.Is(err, ErrSteerRejected) || (err == nil && !steered) {
		return false
	}
	// The delivery attempt is consumed even if its result is unknown. A timeout
	// may follow server acceptance; queueing it again could execute it twice.
	session.TouchUserActivity()
	msg.answerDelivery.finish(err)
	e.noteUserMessageAccepted(key, msg.UserMessageTimeMs)
	runMessageAccepted(msg)
	if err != nil {
		session.AddHistory("user", msg.Content)
		session.AddHistory("assistant", e.i18n.T(MsgMessageSteerUnconfirmed))
		sessions.Save()
		slog.Warn("busy steering delivery unconfirmed; not retrying", "error", err, "session", key)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageSteerUnconfirmed))
		return true
	}
	session.AddHistory("user", msg.Content)
	sessions.Save()
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageSteered))
	return true
}

// Messages accepted during startup are offered to the active turn once it
// emits events. Attachments and definitively rejected inputs retain FIFO queueing.
func (e *Engine) steerQueuedInputs(state *interactiveState, session *Session, sessions *SessionManager, key string) {
	if !e.steerBusyMessages.Load() {
		return
	}
	gate, _ := e.admissionMu.LoadOrStore(key, &sync.Mutex{})
	if !gate.(*sync.Mutex).TryLock() {
		return
	}
	defer gate.(*sync.Mutex).Unlock()
	for {
		state.mu.Lock()
		sender, ok := state.agentSession.(UserInputSteerer)
		if !ok || state.stopped || len(state.pendingMessages) == 0 {
			state.mu.Unlock()
			return
		}
		msg := state.pendingMessages[0]
		if !msg.allowSteer || len(msg.images) > 0 || len(msg.files) > 0 || strings.TrimSpace(msg.content) == "" {
			state.mu.Unlock()
			return
		}
		state.mu.Unlock()
		prompt := e.buildSenderPrompt(msg.content, msg.userID, msg.userName, msg.msgPlatform, msg.msgSessionKey, msg.channelKey)
		accepted, err := sender.SteerUserInput(prompt)
		if errors.Is(err, ErrSteerRejected) {
			state.mu.Lock()
			for i, pending := range state.pendingMessages {
				if pending.queueID == msg.queueID {
					state.pendingMessages[i].allowSteer = false
					break
				}
			}
			state.mu.Unlock()
			return
		}
		if err == nil && !accepted {
			return
		}
		state.mu.Lock()
		for i, pending := range state.pendingMessages {
			if pending.queueID == msg.queueID {
				state.pendingMessages = append(state.pendingMessages[:i], state.pendingMessages[i+1:]...)
				break
			}
		}
		state.mu.Unlock()
		msg.answerDelivery.finish(err)
		session.AddHistory("user", msg.content)
		notice := e.i18n.T(MsgMessageSteered)
		if err != nil {
			notice = e.i18n.T(MsgMessageSteerUnconfirmed)
			session.AddHistory("assistant", notice)
		}
		sessions.Save()
		e.reply(msg.platform, msg.replyCtx, notice)
	}
}
