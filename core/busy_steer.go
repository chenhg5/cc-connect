package core

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

const busySteerAckTimeout = 30 * time.Second

// SetBusyMessageMode is safe during configuration reload. In-flight sends
// retain their ownership; disabling steer only stops new native dispatch.
func (e *Engine) SetBusyMessageMode(mode string) {
	enabled := strings.EqualFold(strings.TrimSpace(mode), "steer")
	e.busyMessageSteer.Store(enabled)
	if !enabled {
		return
	}
	type wake struct {
		state   *interactiveState
		session *Session
		key     string
	}
	var pending []wake
	e.interactiveMu.Lock()
	for _, state := range e.interactiveStates {
		if state == nil {
			continue
		}
		state.mu.Lock()
		if state.busySession != nil && len(state.pendingMessages) > 0 {
			pending = append(pending, wake{state, state.busySession, state.pendingMessages[0].msgSessionKey})
		}
		state.mu.Unlock()
	}
	e.interactiveMu.Unlock()
	for _, item := range pending {
		_, sessions := e.sessionContextForKey(item.key)
		e.kickBusySteerDispatcher(item.state, item.session, sessions)
	}
}

func busySteerEligible(msg *Message) bool {
	return msg.Audio == nil && msg.Location == nil && !msg.FromVoice &&
		(strings.TrimSpace(msg.Content) != "" || len(msg.Images) > 0 || len(msg.Files) > 0)
}

func nativeBusySender(as AgentSession) (BusySteerSender, bool) {
	sender, ok := as.(BusySteerSender)
	return sender, ok && sender.SupportsBusySteer()
}

// enqueueBusyInput records one admission. Waiting for startup/another ACK is
// temporary: an eligible head is dispatched as soon as the sender is ready.
func (e *Engine) enqueueBusyInput(p Platform, msg *Message, key string, preferSteer bool, notice MsgKey, session *Session, sessions *SessionManager) bool {
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	if state == nil {
		e.interactiveMu.Unlock()
		return false
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	if (state.stopped && !state.restartQueuedOnExit) || state.steerUncertain {
		state.mu.Unlock()
		return false
	}
	if e.isStaleUserMessageLocked(state, msg.UserMessageTimeMs) {
		snap := userMessageWatermarkSnapshotLocked(state)
		state.mu.Unlock()
		e.logStaleUserMessageDropped("reject_before_queue", msg, key, snap)
		return true
	}
	depth := len(state.pendingMessages)
	if state.steerPending != nil {
		depth++
	}
	if depth >= e.maxQueuedMessages {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgQueueFull, depth))
		return true
	}
	q := queuedBusyInput(p, msg)
	q.steerWhenReady = preferSteer && busySteerEligible(msg) && !state.stopped && !state.restartQueuedOnExit &&
		session != nil && session.Busy() &&
		(state.steerStarting || (!state.steerAdmissionClosed && !state.steerHandoff))
	if state.agentSession != nil {
		if _, native := nativeBusySender(state.agentSession); !native || !state.agentSession.Alive() {
			q.steerWhenReady = false
		}
	}
	if q.steerWhenReady {
		notice = MsgSteerWaiting
	}
	accepted := make(chan struct{})
	q.accepted = accepted
	receipt := make(chan struct{})
	q.receiptSent = receipt
	state.pendingMessages = append(state.pendingMessages, q)
	busySession := state.busySession
	state.mu.Unlock()
	if busySession != nil {
		busySession.TouchUserActivity()
	}
	runMessageAccepted(msg)
	if q.steerWhenReady {
		close(accepted)
		// Receipt IO is independent of native dispatch. Final feedback waits for
		// this receipt, but the next file/text need not wait for platform latency.
		go func() { e.reply(p, msg.ReplyCtx, e.i18n.T(notice)); close(receipt) }()
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(notice))
		close(receipt)
		close(accepted)
	}
	if session != nil && sessions != nil {
		e.kickBusySteerDispatcher(state, session, sessions)
	}
	return true
}

// A single dispatcher owns native writes for each interactive state. It checks
// readiness and claims the head atomically, then does all IO outside state.mu.
func (e *Engine) kickBusySteerDispatcher(state *interactiveState, session *Session, sessions *SessionManager) {
	state.mu.Lock()
	if state.steerDispatcherRunning || state.stopped || state.steerUncertain ||
		state.steerAdmissionClosed || state.steerHandoff || state.agentSession == nil ||
		len(state.pendingMessages) == 0 || !state.pendingMessages[0].steerWhenReady || !e.busyMessageSteer.Load() {
		state.mu.Unlock()
		return
	}
	state.steerDispatcherRunning = true
	state.mu.Unlock()
	go e.dispatchBusySteerQueue(state, session, sessions)
}

func (e *Engine) dispatchBusySteerQueue(state *interactiveState, session *Session, sessions *SessionManager) {
	for {
		state.mu.Lock()
		if state.stopped || state.steerUncertain || state.steerHandoff || state.steerAdmissionClosed ||
			state.steerPending != nil || len(state.pendingMessages) == 0 || !state.pendingMessages[0].steerWhenReady ||
			!e.busyMessageSteer.Load() || !session.Busy() {
			state.steerDispatcherRunning = false
			state.mu.Unlock()
			return
		}
		q := state.pendingMessages[0]
		as := state.agentSession
		sender, native := nativeBusySender(as)
		if as == nil || !as.Alive() || !native {
			state.pendingMessages[0].steerWhenReady = false
			state.steerDispatcherRunning = false
			state.mu.Unlock()
			e.replyBusyInput(q, MsgSteerFallbackQueue)
			return
		}
		// Admission callbacks must complete before sending; platform receipt
		// IO is independent and must not delay native dispatch.
		if q.accepted != nil {
			select {
			case <-q.accepted:
			default:
				state.steerDispatcherRunning = false
				state.mu.Unlock()
				return
			}
		}
		state.pendingMessages = state.pendingMessages[1:]
		done := make(chan struct{})
		state.steerPending = done
		state.steerInputTimeMs = q.userMessageTimeMs
		if state.stopCh == nil {
			state.stopCh = make(chan struct{})
		}
		stop := state.stopCh
		state.mu.Unlock()
		e.deliverBusyInput(state, session, sessions, as, sender, q, done, stop, "")
	}
}

func (e *Engine) deliverBusyInput(state *interactiveState, session *Session, sessions *SessionManager, as AgentSession, sender BusySteerSender, q queuedMessage, done chan struct{}, stop <-chan struct{}, successNotice MsgKey) {
	ctx, cancel := context.WithTimeout(e.ctx, busySteerAckTimeout)
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	prompt := e.buildSenderPrompt(q.content, q.userID, q.userName, q.msgPlatform, q.msgSessionKey, q.channelKey)
	result, err := sender.SendBusySteer(ctx, prompt, q.messageID, q.images, q.files)
	cancel()
	var rejected *SteerNotAcceptedError
	definite := errors.As(err, &rejected)
	unknown := !definite && (err != nil || (result.Disposition != SteerQueued && result.Disposition != SteerStarted && result.Disposition != SteerHandled))
	state.mu.Lock()
	stopped := state.stopped || state.agentSession != as
	if !stopped {
		q.steerWhenReady = false
		switch {
		case definite:
			state.pendingMessages = append([]queuedMessage{q}, state.pendingMessages...)
		case unknown:
			state.steerUncertain = true
			state.steerAdmissionClosed = true
		case result.Disposition == SteerStarted && !result.ContinuesTurn:
			q.alreadySent = true
			state.pendingMessages = append([]queuedMessage{q}, state.pendingMessages...)
			state.steerAdmissionClosed = true
		}
		if !definite {
			if q.userMessageTimeMs > state.currentTurnUserMessageTimeMs && (result.Disposition != SteerStarted || result.ContinuesTurn) {
				state.currentTurnUserMessageTimeMs = q.userMessageTimeMs
			}
			session.AddHistory("user", q.content)
		}
	}
	state.mu.Unlock()
	if !stopped && !definite {
		sessions.Save()
	}
	state.mu.Lock()
	state.steerPending = nil
	state.steerInputTimeMs = 0
	close(done)
	state.mu.Unlock()
	if stopped {
		e.replyBusyInput(q, MsgPsSendFailed)
	} else if definite {
		e.replyBusyInput(q, MsgSteerFallbackQueue)
	} else if unknown {
		slog.Warn("busy-steer: delivery unconfirmed; automatic input paused", "session", q.msgSessionKey, "error", err)
		e.dropQueueForUncertainSteer(state)
		e.replyBusyInput(q, MsgSteerDeliveryUnknown)
	} else if successNotice != "" && (result.Disposition != SteerStarted || result.ContinuesTurn) {
		e.replyBusyInput(q, successNotice)
	} else {
		e.replyBusyInput(q, busySteerReplyKey(result.Disposition))
	}
}

func (e *Engine) waitBusyInputAccepted(state *interactiveState, q queuedMessage) bool {
	if q.accepted == nil {
		return true
	}
	select {
	case <-q.accepted:
		return true
	case <-state.stopSignal():
		return false
	case <-e.ctx.Done():
		return false
	}
}

func queuedBusyInput(p Platform, msg *Message) queuedMessage {
	return queuedMessage{
		messageID: msg.MessageID, platform: p, replyCtx: msg.ReplyCtx,
		content: msg.Content, images: msg.Images, files: msg.Files, fromVoice: msg.FromVoice, silentEmpty: msg.SilentEmpty,
		userID: msg.UserID, userName: msg.UserName, msgPlatform: msg.Platform,
		msgSessionKey: msg.SessionKey, channelKey: msg.ChannelKey, userMessageTimeMs: msg.UserMessageTimeMs,
	}
}

func (e *Engine) finishBusySteerAdmission(state *interactiveState) {
	state.mu.Lock()
	state.steerStarting = false
	state.steerAdmissionClosed = true
	done := state.steerPending
	state.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-state.stopSignal():
		case <-e.ctx.Done():
		}
	}
	var fallback []queuedMessage
	state.mu.Lock()
	if !state.stopped && !state.steerUncertain {
		for index := range state.pendingMessages {
			if state.pendingMessages[index].steerWhenReady {
				fallback = append(fallback, state.pendingMessages[index])
				state.pendingMessages[index].steerWhenReady = false
			}
		}
	}
	state.mu.Unlock()
	for _, input := range fallback {
		e.replyBusyInput(input, MsgSteerFallbackQueue)
	}
}

func (e *Engine) rejectUncertainSteer(p Platform, msg *Message, key string) bool {
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	uncertain := false
	if state != nil {
		state.mu.Lock()
		uncertain = state.steerUncertain && !state.stopped
		state.mu.Unlock()
	}
	e.interactiveMu.Unlock()
	if uncertain {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerInputPaused))
	}
	return uncertain
}

func (e *Engine) dropQueueForUncertainSteer(state *interactiveState) bool {
	state.mu.Lock()
	uncertain := state.steerUncertain
	var pending []queuedMessage
	if uncertain {
		pending = state.pendingMessages
		state.pendingMessages = nil
	}
	state.mu.Unlock()
	for _, q := range pending {
		e.replyBusyInput(q, MsgSteerQueueNotDelivered)
	}
	return uncertain
}

func busySteerReplyKey(d SteerDisposition) MsgKey {
	switch d {
	case SteerQueued:
		return MsgSteerQueued
	case SteerStarted:
		return MsgSteerStarted
	case SteerHandled:
		return MsgSteerHandled
	default:
		return MsgSteerDeliveryUnknown
	}
}

func (e *Engine) sealBusySteerHandoff(state *interactiveState) {
	state.mu.Lock()
	state.steerHandoff = true
	state.mu.Unlock()
	e.finishBusySteerAdmission(state)
}

func (e *Engine) replyBusyInput(q queuedMessage, notice MsgKey) {
	go func() {
		if q.receiptSent != nil {
			select {
			case <-q.receiptSent:
			case <-e.ctx.Done():
				return
			}
		}
		e.reply(q.platform, q.replyCtx, e.i18n.T(notice))
	}()
}
