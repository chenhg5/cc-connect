package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The daemon reader owns transport events. A foreground route feeds the same
// presentation loop as stdio without handing off or draining the transport.
type sharedForeground struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	events   chan Event
	bound    bool
	terminal bool
	sendErr  error
	turnID   string
	pending  []Event
	parent   *interactiveState
	view     *interactiveState
}

func (f *sharedForeground) route(event Event) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctx.Err() != nil || f.terminal {
		return false
	}
	if !f.bound {
		f.pending = append(f.pending, event)
		return true
	}
	if f.turnID != "" && event.TurnID != "" && event.TurnID != f.turnID {
		return false
	}
	f.terminal = event.Type == EventResult && event.Done || event.Type == EventError
	f.copySideText(event)
	select {
	case f.events <- event:
	case <-f.ctx.Done():
	}
	return true
}

func (f *sharedForeground) bind(id string) []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound, f.turnID = true, id
	var external []Event
	for _, event := range f.pending {
		if f.terminal || (id != "" && event.TurnID != "" && event.TurnID != id) {
			external = append(external, event)
			continue
		}
		f.terminal = event.Type == EventResult && event.Done || event.Type == EventError
		f.copySideText(event)
		select {
		case f.events <- event:
		case <-f.ctx.Done():
		}
	}
	f.pending = nil
	return external
}

func (f *sharedForeground) copySideText(event Event) {
	if event.Type != EventResult || f.parent == nil || f.view == nil {
		return
	}
	f.parent.mu.Lock()
	text := f.parent.sideText
	f.parent.mu.Unlock()
	f.view.mu.Lock()
	f.view.sideText = text
	f.view.mu.Unlock()
}

func (e *Engine) processSharedMessageWith(p Platform, msg *Message, session *Session, agent Agent, sessions *SessionManager, key, workspace string, lockGen uint64) {
	defer session.Unlock(lockGen)
	if workspace != "" && e.workspacePool != nil {
		ws := e.workspacePool.GetOrCreate(workspace)
		ws.BeginTurn()
		defer ws.EndTurn()
	}
	var override Agent
	if agent != e.agent {
		override = agent
	}
	state := e.getOrCreateInteractiveStateWith(key, p, msg.ReplyCtx, session, sessions, override, msg.SessionKey)
	state.mu.Lock()
	as, _ := state.agentSession.(SharedAgentSession)
	generation := state.sharedStopGeneration
	state.workspaceDir = workspace
	state.sharedSessionKey = msg.SessionKey
	state.mu.Unlock()
	if as == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFailedToStartAgentSession))
		return
	}
	e.startSharedReader(state, as, session, sessions, key)
	if _, queuePolicy := agent.(AgentAutoSteer); !queuePolicy && as.RuntimeState().TurnID != "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedBusy))
		return
	}
	for {
		if !e.waitSharedTurnReady(state, as, generation) {
			return
		}
		if !e.runSharedForeground(state, as, p, msg, session, sessions, key, lockGen) {
			continue // This input was not accepted. Retain it across the new turn.
		}
		state.mu.Lock()
		if state.stopped || !as.Alive() {
			state.mu.Unlock()
			e.notifyDroppedQueuedMessages(state, fmt.Errorf("shared observer detached"))
			return
		}
		for len(state.pendingMessages) > 0 && e.isQueuedUserMessageStaleForDrainLocked(state, state.pendingMessages[0].userMessageTimeMs) {
			state.pendingMessages = state.pendingMessages[1:]
		}
		if len(state.pendingMessages) == 0 {
			// Close the queue/unlock race while holding state.mu.
			session.Unlock(lockGen)
			state.mu.Unlock()
			return
		}
		state.mu.Unlock()
		if !e.waitSharedTurnReady(state, as, generation) {
			return
		}
		state.mu.Lock()
		if state.stopped || state.agentSession != as || len(state.pendingMessages) == 0 {
			state.mu.Unlock()
			return
		}
		q := state.pendingMessages[0]
		state.pendingMessages = state.pendingMessages[1:]
		state.mu.Unlock()
		p = q.platform
		msg = &Message{SessionKey: q.msgSessionKey, Platform: q.msgPlatform, UserID: q.userID, UserName: q.userName, Content: q.content, MessageID: q.messageID, ReplyCtx: q.replyCtx, Images: q.images, Files: q.files, FromVoice: q.fromVoice, UserMessageTimeMs: q.userMessageTimeMs, ChannelKey: q.channelKey}
	}
}

// false means busy without accepting the input; the drain retains and retries it.
func (e *Engine) runSharedForeground(state *interactiveState, as SharedAgentSession, p Platform, msg *Message, session *Session, sessions *SessionManager, key string, lockGen uint64) bool {
	if as.RuntimeState().TurnID != "" {
		return false
	}
	ctx, cancel := context.WithCancel(e.ctx)
	f := &sharedForeground{ctx: ctx, cancel: cancel, events: make(chan Event, 128)}
	state.mu.Lock()
	oldPlatform, oldReply := state.platform, state.replyCtx
	state.platform, state.replyCtx = p, msg.ReplyCtx
	state.fromVoice, state.currentMessageID = msg.FromVoice, msg.MessageID
	state.currentTurnUserMessageTimeMs = msg.UserMessageTimeMs
	state.sharedForeground = f
	state.mu.Unlock()
	state.stopSignal()
	state.mu.Lock()
	view := &interactiveState{agentSession: as, agent: state.agent, platform: p, replyCtx: msg.ReplyCtx, workspaceDir: state.workspaceDir, stopCh: state.stopCh, eventsNeedResync: false, sharedRuntime: state, fromVoice: msg.FromVoice, currentTurnUserMessageTimeMs: msg.UserMessageTimeMs}
	state.mu.Unlock()
	f.parent, f.view = state, view
	defer func() {
		cancel()
		state.mu.Lock()
		if state.sharedForeground == f {
			state.sharedForeground = nil
			// A muted scheduled turn must not mute the persistent observer.
			if _, muted := p.(*mutePlatform); muted {
				if previous, ok := oldPlatform.(*mutePlatform); ok {
					oldPlatform = previous.Platform
				}
				state.platform, state.replyCtx = oldPlatform, oldReply
			}
		}
		state.mu.Unlock()
	}()
	e.i18n.DetectAndSet(msg.Content)
	if msg.UserID != "cron" && msg.UserID != "timer" && msg.UserID != "heartbeat" {
		session.TouchUserActivity()
	}
	prompt := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	sendDone := make(chan error, 1)
	go func() {
		var id string
		var err error
		if sender, ok := as.(AgentTurnSender); ok {
			id, err = sender.SendTurn(prompt, msg.MessageID, msg.Images, msg.Files)
			if err == nil {
				session.AddHistory("user", msg.Content)
				sessions.Save()
			}
		} else {
			// Legacy Send can emit an immediate result before returning.
			session.AddHistory("user", msg.Content)
			sessions.Save()
			f.bind("")
			err = as.Send(prompt, msg.MessageID, msg.Images, msg.Files)
		}
		f.mu.Lock()
		f.sendErr = err
		f.mu.Unlock()
		view.mu.Lock()
		view.sharedTurnID = id
		view.mu.Unlock()
		state.mu.Lock()
		replayEvents := state.sharedReplayEvents
		state.mu.Unlock()
		for _, event := range f.bind(id) {
			// Foreign/late events must use the same per-turn presentation router,
			// not the plain-text fallback, once the new turn ID is known.
			select {
			case replayEvents <- event:
			case <-ctx.Done():
				return
			case <-state.stopSignal():
				return
			}
		}
		sendDone <- err
	}()
	var stopTyping func()
	if ti, ok := p.(TypingIndicator); ok {
		stopTyping = ti.StartTyping(e.ctx, msg.ReplyCtx)
	}
	e.processInteractiveEvents(view, session, sessions, key, msg.MessageID, time.Now(), stopTyping, sendDone, msg.ReplyCtx, lockGen, f.events)
	view.mu.Lock()
	completed := view.lastCompletedUserMessageTimeMs
	view.mu.Unlock()
	state.mu.Lock()
	if completed > state.lastCompletedUserMessageTimeMs {
		state.lastCompletedUserMessageTimeMs = completed
	}
	state.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	return !errors.Is(f.sendErr, ErrAgentTurnBusy)
}

func (e *Engine) startSharedExternalPresentation(state *interactiveState, as SharedAgentSession, session *Session, sessions *SessionManager, key, turnID string) *sharedForeground {
	ctx, cancel := context.WithCancel(e.ctx)
	f := &sharedForeground{ctx: ctx, cancel: cancel, events: make(chan Event, 128), bound: true, turnID: turnID}
	state.stopSignal()
	state.mu.Lock()
	view := &interactiveState{agentSession: as, agent: state.agent, platform: state.platform, replyCtx: state.replyCtx, workspaceDir: state.workspaceDir, stopCh: state.stopCh, eventsNeedResync: false, sharedRuntime: state, sharedTurnID: turnID}
	state.mu.Unlock()
	var ws *workspaceState
	if view.workspaceDir != "" && e.workspacePool != nil {
		ws = e.workspacePool.GetOrCreate(view.workspaceDir)
		ws.BeginTurn()
	}
	go func() {
		defer cancel()
		if ws != nil {
			defer ws.EndTurn()
		}
		var stopTyping func()
		if ti, ok := view.platform.(TypingIndicator); ok {
			stopTyping = ti.StartTyping(e.ctx, view.replyCtx)
		}
		e.processInteractiveEvents(view, session, sessions, key, "", time.Now(), stopTyping, nil, view.replyCtx, 0, f.events)
	}()
	return f
}
