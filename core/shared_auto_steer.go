package core

import (
	"strings"
	"time"
)

// Read only local runtime state; no daemon RPC or task presentation is started
// while another client owns the active turn.
func (e *Engine) waitSharedTurnReady(state *interactiveState, as SharedAgentSession, generation uint64) bool {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state.mu.Lock()
		valid := state.agentSession == as && !state.stopped && state.sharedStopGeneration == generation
		state.mu.Unlock()
		if !valid || !as.Alive() || e.ctx.Err() != nil {
			return false
		}
		if as.RuntimeState().TurnID == "" {
			return true
		}
		select {
		case <-e.ctx.Done():
			return false
		case <-state.stopSignal():
			return false
		case <-ticker.C:
		}
	}
}

// Pending approvals/questions and explicit commands are handled before this
// path. Never turn a failed steer into a new turn: completion can race input.
func (e *Engine) handleSharedBusyMessage(p Platform, msg *Message, agent Agent, session *Session, sessions *SessionManager, key string) bool {
	policy, ok := agent.(AgentAutoSteer)
	if !ok || strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
		return false
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	as, ok := state.agentSession.(SharedAgentSession)
	state.mu.Unlock()
	if !ok {
		return false
	}
	runtime := as.RuntimeState()
	if runtime.TurnID == "" {
		return false
	}
	if !policy.AutoSteerBusyMessages() {
		if !e.queueMessageForBusySession(p, msg, key) {
			return false
		}
		e.startSharedQueueDrain(state, as, agent, session, sessions, key)
		return true
	}
	// The current steer interface carries text only. Do not silently drop media
	// or queue text the sender expected to insert into the current turn.
	if len(msg.Images) > 0 || len(msg.Files) > 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedSteerTextOnly))
		return true
	}
	steerer, ok := as.(AgentSessionSteerer)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedUnsupported))
		return true
	}
	if err := steerer.Steer(runtime.TurnID, msg.Content); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	session.TouchUserActivity()
	e.noteUserMessageAccepted(key, msg.UserMessageTimeMs)
	runMessageAccepted(msg)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedSteerAccepted))
	return true
}

// Keep queued messages in the existing queue while waiting for an external
// turn. Stop/detach/switch can discard them through the same cleanup path as
// locally initiated turns. The session lock permits only one drain worker.
func (e *Engine) startSharedQueueDrain(state *interactiveState, as SharedAgentSession, agent Agent, session *Session, sessions *SessionManager, key string) {
	gen, locked := session.TryLock()
	if !locked {
		return // The existing foreground/drain already owns this queue.
	}
	go func() {
		defer func() {
			if locked {
				session.Unlock(gen)
			}
		}()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			state.mu.Lock()
			valid := state.agentSession == as && !state.stopped
			pending := len(state.pendingMessages) > 0
			workspace := state.workspaceDir
			if !valid || !pending || !as.Alive() || e.ctx.Err() != nil {
				// Release while holding state.mu so a concurrent enqueue can
				// acquire the session lock and start a replacement drainer.
				session.Unlock(gen)
				locked = false
				state.mu.Unlock()
				return
			}
			state.mu.Unlock()
			if as.RuntimeState().TurnID == "" {
				locked = false // drainOrphanedQueue takes ownership of the lock.
				e.drainOrphanedQueue(session, sessions, key, agent, workspace, gen)
				return
			}
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
