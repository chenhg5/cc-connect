package telegram

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/go-telegram/bot/models"
)

// defaultHandlerStallTimeout bounds how long the core message handler may
// run before its session lane moves on.
//
// The lane is what keeps the messages of one chat in the order the user sent
// them, so the budget has to sit above the slowest *legitimate* update. The
// slow part of an update is the core message handler (a built-in command
// such as /usage or /doctor); media downloads are already bounded by the
// HTTP client timeout.
//
// When the budget expires the stuck handler is abandoned — its goroutine
// keeps running, but the lane stops waiting for it — so one stuck command
// cannot hold up the rest of the session forever (issue #2015).
const defaultHandlerStallTimeout = 60 * time.Second

// queuedUpdate is one Telegram update waiting for its session lane.
type queuedUpdate struct {
	ctx    context.Context
	update *models.Update
}

// updateLane serializes the updates of one session.
//
// Telegram delivers updates through a single polling goroutine, so the
// handler used to run inline on it: one stuck built-in command held the only
// update worker and every later update queued behind it until cc-connect was
// restarted (issue #2015). Updates are therefore handed to a lane instead of
// being handled on the polling goroutine.
//
// A lane processes its updates one at a time, which keeps the messages of a
// session in order, while lanes of different sessions run in parallel.
type updateLane struct {
	queue   []queuedUpdate
	running bool
}

// enqueueUpdate hands an update to its session lane and returns immediately.
//
// The polling goroutine must never wait for user-triggered work, so this only
// appends to the lane's queue. The lane worker is started lazily, when the
// first update of a session arrives.
func (p *Platform) enqueueUpdate(ctx context.Context, key string, update *models.Update) {
	p.lanesMu.Lock()
	if p.lanes == nil {
		p.lanes = make(map[string]*updateLane)
	}
	lane := p.lanes[key]
	if lane == nil {
		lane = &updateLane{}
		p.lanes[key] = lane
	}
	lane.queue = append(lane.queue, queuedUpdate{ctx: ctx, update: update})
	start := !lane.running
	if start {
		lane.running = true
	}
	p.lanesMu.Unlock()

	if start {
		go p.runLane(key, lane)
	}
}

// runLane processes the queued updates of one session, one at a time.
func (p *Platform) runLane(key string, lane *updateLane) {
	for {
		p.lanesMu.Lock()
		if len(lane.queue) == 0 {
			// The lane is idle: retire it so a later update starts a fresh
			// worker instead of keeping an empty lane around forever.
			lane.running = false
			if p.lanes[key] == lane {
				delete(p.lanes, key)
			}
			p.lanesMu.Unlock()
			return
		}
		item := lane.queue[0]
		lane.queue = lane.queue[1:]
		p.lanesMu.Unlock()

		p.dispatchUpdate(item.ctx, item.update)
	}
}

// dropPendingLanes discards the updates that are still queued.
//
// Called when the platform stops: the queued updates would be handled by a
// bot that is no longer connected. The lanes themselves are kept — their
// worker retires them once it drains — so that no second worker can start
// for a session that still has an update in flight.
func (p *Platform) dropPendingLanes() {
	p.lanesMu.Lock()
	defer p.lanesMu.Unlock()
	for _, lane := range p.lanes {
		lane.queue = nil
	}
}

// laneKey returns the lane an update belongs to.
//
// The messages of one session share a lane so that they keep their order.
// Callback queries get a lane of their own: they answer a pending
// permission request or run a built-in command, and must not wait behind a
// message of the same session that is still being handled.
func (p *Platform) laneKey(update *models.Update) string {
	switch {
	case update == nil:
		return "misc"
	case update.CallbackQuery != nil:
		return "cb:" + p.callbackSessionKey(update.CallbackQuery)
	case update.Message != nil:
		return "msg:" + p.messageSessionKey(update.Message)
	default:
		return "misc"
	}
}

// messageSessionKey returns the session key of a message, or "" when the
// message cannot be attributed to a session.
func (p *Platform) messageSessionKey(msg *models.Message) string {
	if msg == nil || msg.From == nil {
		return ""
	}
	return p.buildSessionKey(msg.Chat.ID, messageThreadID(msg.Chat, msg.MessageThreadID), msg.From.ID)
}

// callbackSessionKey returns the session key of a callback query.
func (p *Platform) callbackSessionKey(cb *models.CallbackQuery) string {
	if cb == nil || cb.Message.Message == nil {
		return ""
	}
	msg := cb.Message.Message
	return p.buildSessionKey(msg.Chat.ID, callbackThreadID(msg.Chat, msg.MessageThreadID), cb.From.ID)
}

// messageThreadID returns the thread ID that isolates a sub-session for a
// message.
//
// Thread ID is only used when it meaningfully isolates a sub-session:
//   - Forum groups (IsForum=true): Topics feature — thread ID is the topic ID.
//   - Non-group chats (private, channel): thread ID is safe to use since
//     there are no "reply threads" that would accidentally fragment sessions.
//   - Regular groups (IsForum=false): thread replies produce a non-zero
//     MessageThreadID, but using it would split an existing session each time
//     a user replies to a specific message — so we ignore it there.
func messageThreadID(chat models.Chat, messageThreadID int) int {
	isGroup := chat.Type == models.ChatTypeGroup || chat.Type == models.ChatTypeSupergroup
	if chat.IsForum || !isGroup {
		return messageThreadID
	}
	return 0
}

// callbackThreadID returns the thread ID that isolates a sub-session for a
// callback query.
//
// IsForum is not reliably present in the Chat object embedded in a
// CallbackQuery message. Use MessageThreadID directly: non-zero means we're
// in a forum topic, zero means a regular group (no topic isolation needed).
func callbackThreadID(chat models.Chat, messageThreadID int) int {
	isGroupChat := chat.Type == models.ChatTypeGroup || chat.Type == models.ChatTypeSupergroup
	if !isGroupChat || messageThreadID != 0 {
		return messageThreadID
	}
	return 0
}

// callHandler invokes the core message handler with a stall budget.
//
// The handler runs on the session lane worker, so a handler that never
// returns would block every later update of that session. The budget
// abandons a stuck call — its goroutine keeps running — so the lane can move
// on, and a stuck built-in command cannot take the bot down (issue #2015).
//
// A panic in the handler is recovered here as well: the update runs on its
// own goroutine, so an unrecovered panic would take the whole process — and
// with it every other platform — down.
func (p *Platform) callHandler(handler core.MessageHandler, msg *core.Message) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("telegram: panic while handling message",
					"panic", r, "stack", string(debug.Stack()))
			}
		}()
		handler(p, msg)
	}()

	stallTimeout := p.handlerStallTimeout()
	timer := time.NewTimer(stallTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		slog.Warn("telegram: message handler stalled; abandoning it so the session can continue",
			"timeout", stallTimeout,
			"session", msg.SessionKey,
			"message_id", msg.MessageID,
		)
	}
}

// handlerStallTimeout returns the stall budget of the core message handler.
func (p *Platform) handlerStallTimeout() time.Duration {
	if p.stallTimeout > 0 {
		return p.stallTimeout
	}
	return defaultHandlerStallTimeout
}
