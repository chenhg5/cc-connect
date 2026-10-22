package core

import (
	"errors"
	"log/slog"
	"strings"
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
	if errors.Is(err, ErrSteerRejected) || (err == nil && !steered) {
		return false
	}
	// The delivery attempt is consumed even if its result is unknown. A timeout
	// may follow server acceptance; queueing it again could execute it twice.
	session.TouchUserActivity()
	e.noteUserMessageAccepted(key, msg.UserMessageTimeMs)
	runMessageAccepted(msg)
	if err != nil {
		slog.Warn("busy steering delivery unconfirmed; not retrying", "error", err, "session", key)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageSteerUnconfirmed))
		return true
	}
	session.AddHistory("user", msg.Content)
	sessions.Save()
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageSteered))
	return true
}
