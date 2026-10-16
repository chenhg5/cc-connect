package core

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ---- maomao fork: retry and cool down when the model is at capacity ----
//
// 用户 2026-10-16：「遇到这种报错就重试，三次都这样的话，冷却10分钟，每次冷却时间翻倍，
// 然后冷却完了记得要那个尝试」。A turn that fails with the provider's "model is at
// capacity" error is re-sent right away, up to capacityAttempts sends in total.
// If all fail, the engine cools down (capacityCooldownBase, doubling after each
// cooldown that ends in failure again, reset by any turn that gets through) and
// re-delivers the message when the cooldown ends. Messages arriving while
// cooling down wait for the end of the cooldown instead of hitting the model.

const capacityAttempts = 3 // 用户原话「三次都这样的话」

var capacityCooldownBase = 10 * time.Minute // 用户原话「冷却10分钟」；var only so tests can shorten it

// isModelCapacityError matches codex's "Selected model is at capacity. Please
// try a different model." turn failure.
func isModelCapacityError(msg string) bool {
	return strings.Contains(msg, "at capacity")
}

// capacityWait returns how long the current cooldown still runs (0 = none).
func (e *Engine) capacityWait() time.Duration {
	e.capMu.Lock()
	defer e.capMu.Unlock()
	if d := time.Until(e.capUntil); d > 0 {
		return d
	}
	return 0
}

// capacityStartCooldown starts the next cooldown: base, then doubling.
func (e *Engine) capacityStartCooldown() time.Duration {
	e.capMu.Lock()
	defer e.capMu.Unlock()
	d := capacityCooldownBase
	for i := 0; i < e.capRound && d < 1<<40; i++ { // guard against overflow only
		d *= 2
	}
	e.capRound++
	e.capUntil = time.Now().Add(d)
	return d
}

// capacityReset ends the doubling once a turn got through.
func (e *Engine) capacityReset() {
	e.capMu.Lock()
	e.capRound = 0
	e.capMu.Unlock()
}

// runTurnWithCapacityRetry runs one turn: send + event loop, re-sending up to
// capacityAttempts times while the turn fails with a capacity error. When it
// gives up, or when a cooldown is already running, it hands the message to
// defer_ to be re-delivered after the cooldown. send must tolerate a nil
// session (return an error).
func (e *Engine) runTurnWithCapacityRetry(state *interactiveState, session *Session, sessions *SessionManager, key, msgID string, turnStart time.Time, stopTyping func(), replyCtx any, send func(as AgentSession) error, defer_ func(wait time.Duration, cooling bool)) {
	if wait := e.capacityWait(); wait > 0 {
		if stopTyping != nil {
			stopTyping()
		}
		defer_(wait, true)
		return
	}
	for attempt := 1; ; attempt++ {
		state.mu.Lock()
		as := state.agentSession
		state.capacityFailed = false
		state.mu.Unlock()
		if attempt > 1 && as != nil {
			drainEvents(as.Events())
		}
		sendDone := make(chan error, 1)
		go func() { sendDone <- send(as) }()
		e.processInteractiveEvents(state, session, sessions, key, msgID, turnStart, stopTyping, sendDone, replyCtx)
		stopTyping = nil // owned (and stopped) by the first event loop

		state.mu.Lock()
		failed := state.capacityFailed
		state.capacityFailed = false
		alive := state.agentSession != nil && state.agentSession.Alive()
		state.mu.Unlock()
		if !failed {
			e.capacityReset()
			return
		}
		if attempt >= capacityAttempts || !alive {
			break
		}
		slog.Warn("model at capacity, retrying turn", "session_key", key, "attempt", attempt+1)
	}
	d := e.capacityStartCooldown()
	slog.Warn("model at capacity after retries, cooling down", "session_key", key, "cooldown", d)
	defer_(d, false)
}

// scheduleCapacityRedelivery tells the user and re-delivers msg once the
// cooldown ends, the same way an incoming message is dispatched (after the gate).
func (e *Engine) scheduleCapacityRedelivery(p Platform, msg Message, agent Agent, sessions *SessionManager, interactiveKey, workspaceDir string, wait time.Duration, cooling bool) {
	mins := int((wait + time.Minute - 1) / time.Minute)
	if cooling {
		e.send(p, msg.ReplyCtx, fmt.Sprintf("⏳ 模型满载冷却中，这条 %d 分钟后再处理。", mins))
	} else {
		e.send(p, msg.ReplyCtx, fmt.Sprintf("⏳ 模型满载（at capacity），连试 %d 次都失败，冷却 %d 分钟后自动重试这条。", capacityAttempts, mins))
	}
	msg.UserMessageTimeMs = 0 // a re-delivery is never "stale"
	time.AfterFunc(wait, func() {
		if e.ctx.Err() != nil {
			return
		}
		m := msg
		e.redeliverMessage(p, &m, agent, sessions, interactiveKey, workspaceDir)
	})
}

// redeliverMessage mirrors the tail of message dispatch: run now if the
// session is free, otherwise queue behind the running turn.
func (e *Engine) redeliverMessage(p Platform, msg *Message, agent Agent, sessions *SessionManager, interactiveKey, workspaceDir string) {
	session := sessions.GetOrCreateActive(msg.SessionKey)
	e.ensureInteractiveStateForQueueing(interactiveKey, p, msg.ReplyCtx)
	if !session.TryLock() {
		if e.queueMessageForBusySession(p, msg, interactiveKey) {
			if session.TryLock() {
				go e.drainOrphanedQueue(session, sessions, interactiveKey, agent, workspaceDir)
			}
			return
		}
		slog.Warn("capacity re-delivery: session busy and queue unavailable, dropping", "session_key", msg.SessionKey)
		e.send(p, msg.ReplyCtx, "⚠️ 冷却后重试这条时会话忙且排不上队，这条没能重试。")
		return
	}
	slog.Info("capacity re-delivery", "session_key", msg.SessionKey)
	go e.processInteractiveMessageWith(p, msg, session, agent, sessions, interactiveKey, workspaceDir, msg.SessionKey)
}

// queuedToMessage rebuilds the Message a queued entry came from.
func queuedToMessage(q queuedMessage) Message {
	return Message{
		MessageID:    q.messageID,
		ReplyCtx:     q.replyCtx,
		Content:      q.content,
		Images:       q.images,
		Files:        q.files,
		FromVoice:    q.fromVoice,
		UserID:       q.userID,
		UserName:     q.userName,
		TurnCtx:      q.turnCtx,
		TurnCtxQuery: q.turnCtxQuery,
		Platform:     q.msgPlatform,
		SessionKey:   q.msgSessionKey,
		ChannelKey:   q.channelKey,
	}
}
