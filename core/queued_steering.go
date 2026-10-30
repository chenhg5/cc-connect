package core

import (
	"crypto/rand"
	"log/slog"
	"sync"
	"time"
)

// Shared by queue copies and button callbacks. Serialize updates so a delayed
// Keep response cannot overwrite the status after automatic queue delivery.
type queuedChoiceReceipt struct {
	mu               sync.Mutex
	replyCtx         any
	processing       bool
	title, detail    MsgKey
	color            string
	synced, retrying bool
	// Immutable ownership and display data, retained briefly after dequeue so
	// an old callback can repair the card without submitting input again.
	platform                    Platform
	userID, sessionKey, content string
}

// Bound memory independently of message history. Pending entries also own their
// receipt, so evicting an old lookup never prevents automatic status updates.
func (e *Engine) rememberQueuedChoice(id string, receipt *queuedChoiceReceipt) {
	if id == "" {
		return
	}
	e.queuedChoicesMu.Lock()
	defer e.queuedChoicesMu.Unlock()
	if e.queuedChoices == nil {
		e.queuedChoices = make(map[string]*queuedChoiceReceipt)
	}
	if _, exists := e.queuedChoices[id]; exists {
		return
	}
	if len(e.queuedChoiceOrder) >= 512 {
		delete(e.queuedChoices, e.queuedChoiceOrder[0])
		e.queuedChoiceOrder = e.queuedChoiceOrder[1:]
	}
	e.queuedChoices[id] = receipt
	e.queuedChoiceOrder = append(e.queuedChoiceOrder, id)
}

func newQueuedChoiceReceipt(p Platform, msg *Message, queued queuedMessage) *queuedChoiceReceipt {
	user, session := queued.userID, queued.msgSessionKey
	if user == "" {
		user = msg.UserID
	}
	if session == "" {
		session = msg.SessionKey
	}
	return &queuedChoiceReceipt{platform: p, userID: user, sessionKey: session, content: truncateIf(queued.content, 240)}
}

func newQueuedMessageID() string { return rand.Text() }

func (e *Engine) sendQueuedMessageChoice(p Platform, replyCtx any, queued queuedMessage) *queuedChoiceReceipt {
	card := NewCard().Title(e.i18n.T(MsgQueueChoiceTitle), "blue").
		Markdown(e.i18n.T(MsgMessageQueued)).
		Markdown(truncateIf(queued.content, 240)).
		Buttons(
			PrimaryBtn(e.i18n.T(MsgQueueSteerButton), "cmd:/ps --queued "+queued.queueID),
		).Build()
	if sender, ok := p.(TrackedCardSender); ok {
		if err := e.waitOutgoing(p); err != nil {
			slog.Warn("queued choice card send cancelled", "error", err)
			return nil
		}
		ctx, err := sender.SendTrackedCard(e.ctx, replyCtx, e.renderCardForPlatform(p, card))
		if err != nil {
			slog.Warn("queued choice card send failed", "error", err)
			e.reply(p, replyCtx, e.i18n.T(MsgMessageQueued))
			return nil
		}
		if ctx != nil {
			receipt := newQueuedChoiceReceipt(p, &Message{}, queued)
			receipt.replyCtx = ctx
			e.rememberQueuedChoice(queued.queueID, receipt)
			return receipt
		}
		return nil
	}
	e.sendWithCard(p, replyCtx, card)
	return nil
}

func (e *Engine) markQueuedMessageProcessing(queued queuedMessage) {
	if queued.choiceReceipt != nil {
		e.updateQueuedMessageChoice(queued.platform, &Message{}, queued, MsgQueueProcessing, MsgQueueProcessing, "green")
	}
}

// Only call after claiming the author's choice. Rejected or duplicate clicks
// must not overwrite an in-flight or completed card with a different status.
func (e *Engine) updateQueuedMessageChoice(p Platform, msg *Message, queued queuedMessage, title, detail MsgKey, color string) bool {
	receipt := queued.choiceReceipt
	if receipt == nil {
		receipt = newQueuedChoiceReceipt(p, msg, queued)
		receipt.replyCtx = msg.ReplyCtx
		e.rememberQueuedChoice(queued.queueID, receipt)
	}
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if receipt.processing && title != MsgQueueProcessing {
		return true
	}
	if title == MsgQueueProcessing {
		receipt.processing = true
	}
	if receipt.title != title || receipt.detail != detail || receipt.color != color {
		receipt.title, receipt.detail, receipt.color = title, detail, color
		receipt.synced = false
	}
	return e.syncQueuedChoiceLocked(receipt)
}

// receipt.mu serializes both callback and automatic updates. Retries always
// render the latest desired state, never the snapshot from a failed request.
func (e *Engine) syncQueuedChoiceLocked(receipt *queuedChoiceReceipt) bool {
	if receipt.synced {
		return true
	}
	updater, ok := receipt.platform.(CardActionUpdater)
	if !ok || receipt.replyCtx == nil {
		return false
	}
	card := NewCard().Title(e.i18n.T(receipt.title), receipt.color).
		Markdown(e.i18n.T(receipt.detail)).Markdown(receipt.content).Build()
	if err := e.waitOutgoing(receipt.platform); err != nil {
		return false
	}
	updated, err := updater.UpdateActionCard(e.ctx, receipt.replyCtx, e.renderCardForPlatform(receipt.platform, card))
	if err != nil {
		slog.Warn("queued choice card update failed", "error", err)
		if !receipt.retrying && e.ctx.Err() == nil {
			receipt.retrying = true
			go e.retryQueuedChoice(receipt)
		}
		return false
	}
	receipt.synced = updated
	return updated
}

func (e *Engine) retryQueuedChoice(receipt *queuedChoiceReceipt) {
	delays := []time.Duration{250 * time.Millisecond, time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second}
	for i, delay := range delays {
		timer := time.NewTimer(delay)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			receipt.mu.Lock()
			receipt.retrying = false
			receipt.mu.Unlock()
			return
		case <-timer.C:
		}
		receipt.mu.Lock()
		done := e.syncQueuedChoiceLocked(receipt)
		if done || i == len(delays)-1 {
			receipt.retrying = false
		}
		receipt.mu.Unlock()
		if done {
			return
		}
	}
}

func (e *Engine) closeQueuedMessageCards(queued []queuedMessage) {
	for _, q := range queued {
		if q.choiceReceipt != nil {
			e.updateQueuedMessageChoice(q.platform, &Message{}, q, MsgQueueRemoved, MsgQueueRemoved, "grey")
		}
	}
}

// A callback may arrive after drain, cancellation, eviction, or restart. Only
// repair the receipt; queue absence must never cause input to be resubmitted.
func (e *Engine) handleExpiredQueuedChoice(p Platform, msg *Message, id string) {
	e.queuedChoicesMu.Lock()
	receipt := e.queuedChoices[id]
	e.queuedChoicesMu.Unlock()
	if receipt != nil {
		if msg.UserID == "" || receipt.userID != msg.UserID || receipt.sessionKey != msg.SessionKey || receipt.platform != p {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQueueChoiceExpired))
			return
		}
		receipt.mu.Lock()
		detail := receipt.detail
		if detail != "" {
			e.syncQueuedChoiceLocked(receipt)
		}
		receipt.mu.Unlock()
		if detail != "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(detail))
			return
		}
	}
	// For cards from an earlier process, only an adapter-validated card
	// callback can update the clicked bot message. Typed commands cannot.
	e.finishQueuedMessageChoice(p, msg, queuedMessage{}, MsgQueueChoiceTitle, MsgQueueChoiceExpired, "grey")
}

func (e *Engine) finishQueuedMessageChoice(p Platform, msg *Message, queued queuedMessage, title, detail MsgKey, color string) {
	if !e.updateQueuedMessageChoice(p, msg, queued, title, detail, color) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(detail))
	}
}

// Remove the message before calling the adapter so result handling cannot also
// drain it. Only a confirmed non-delivery (false, nil) returns it to the queue;
// an error may mean the remote accepted input but its acknowledgement was lost.
func (e *Engine) handleQueuedMessageChoice(p Platform, msg *Message, id string, keep bool) {
	key := e.interactiveKeyForSessionKey(msg.SessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	if state == nil {
		e.interactiveMu.Unlock()
		e.handleExpiredQueuedChoice(p, msg, id)
		return
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	index := -1
	for i, queued := range state.pendingMessages {
		if queued.queueID != id {
			continue
		}
		if msg.UserID == "" || queued.userID != msg.UserID {
			state.mu.Unlock()
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQueueChoiceExpired))
			return
		}
		if !queued.queueChoiceClosed {
			index = i
		}
		break
	}
	if index < 0 || state.stopped {
		state.mu.Unlock()
		e.handleExpiredQueuedChoice(p, msg, id)
		return
	}
	if state.pendingMessages[index].choiceReceipt == nil {
		receipt := newQueuedChoiceReceipt(p, msg, state.pendingMessages[index])
		receipt.replyCtx = msg.ReplyCtx
		state.pendingMessages[index].choiceReceipt = receipt
		e.rememberQueuedChoice(id, receipt)
	}
	if keep {
		state.pendingMessages[index].queueChoiceClosed = true
		queued := state.pendingMessages[index]
		state.mu.Unlock()
		e.finishQueuedMessageChoice(p, msg, queued, MsgQueueKept, MsgMessageQueued, "blue")
		return
	}
	sender, ok := state.agentSession.(UserInputSteerer)
	if !ok || state.queueSteering {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageQueued))
		return
	}
	queued := state.pendingMessages[index]
	queued.queueChoiceClosed = true
	predecessors := make(map[string]bool, index)
	for _, earlier := range state.pendingMessages[:index] {
		predecessors[earlier.queueID] = true
	}
	state.pendingMessages = append(state.pendingMessages[:index], state.pendingMessages[index+1:]...)
	state.queueSteering = true
	defer func() {
		state.mu.Lock()
		state.queueSteering = false
		state.mu.Unlock()
	}()
	session := state.busySession
	workspace := state.workspaceDir
	state.mu.Unlock()
	e.updateQueuedMessageChoice(p, msg, queued, MsgQueueSteerPending, MsgQueueSteerPending, "blue")

	agent, sessions := e.sessionContextForKey(msg.SessionKey)
	if session == nil {
		session = sessions.GetOrCreateActive(msg.SessionKey)
	}
	prompt := e.buildSenderPrompt(queued.content, queued.userID, queued.userName, queued.msgPlatform, queued.msgSessionKey, queued.channelKey)
	steered, err := sender.SteerUserInput(prompt)
	if steered || err != nil {
		session.AddHistory("user", queued.content)
		sessions.Save()
		if err != nil {
			slog.Warn("queued message steering unconfirmed; not replaying", "error", err)
			e.finishQueuedMessageChoice(p, msg, queued, MsgQueueChoiceTitle, MsgQueueSteerUnconfirmed, "orange")
		} else {
			e.finishQueuedMessageChoice(p, msg, queued, MsgQueueSteerSent, MsgPsSent, "green")
		}
		return
	}
	if !e.restoreQueuedMessage(key, state, queued, predecessors) {
		e.finishQueuedMessageChoice(p, msg, queued, MsgQueueChoiceTitle, MsgQueueChoiceExpired, "grey")
		return
	}
	e.finishQueuedMessageChoice(p, msg, queued, MsgQueueKept, MsgMessageQueued, "blue")
	if lockGen, locked := session.TryLock(); locked {
		go e.drainOrphanedQueue(session, sessions, key, agent, workspace, lockGen)
	}
}

func (e *Engine) restoreQueuedMessage(key string, state *interactiveState, queued queuedMessage, predecessors map[string]bool) bool {
	e.interactiveMu.Lock()
	if e.interactiveStates[key] != state {
		e.interactiveMu.Unlock()
		return false
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	defer state.mu.Unlock()
	if state.stopped {
		return false
	}
	// Earlier entries may have drained while the adapter was responding. Insert
	// after surviving predecessors, before later entries and newly queued input.
	index := 0
	for i, pending := range state.pendingMessages {
		if predecessors[pending.queueID] {
			index = i + 1
		}
	}
	state.pendingMessages = append(state.pendingMessages, queuedMessage{})
	copy(state.pendingMessages[index+1:], state.pendingMessages[index:])
	state.pendingMessages[index] = queued
	return true
}

// Used by silent session transitions: retire receipts without extra messages.
func (e *Engine) discardQueuedMessages(state *interactiveState) {
	state.mu.Lock()
	pending := state.pendingMessages
	state.pendingMessages = nil
	state.mu.Unlock()
	e.closeQueuedMessageCards(pending)
}
