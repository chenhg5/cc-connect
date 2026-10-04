package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Relay retains a separate conversation, but approvals/questions are delivered
// to the source chat and resolved using their request token. There is still one
// daemon event reader; timeout leaves that reader alive until the turn ends.
func (e *Engine) handleSharedRelay(ctx context.Context, agent Agent, sessions *SessionManager, relayKey, sourceKey, message string) (string, error) {
	target, err := e.resolveSendTarget(sourceKey, false)
	if err != nil {
		return "", fmt.Errorf("relay reply target: %w", err)
	}
	if target.platform == nil {
		return "", fmt.Errorf("relay source has no reply platform")
	}
	session := sessions.GetOrCreateActive(relayKey)
	gen, locked := session.TryLock()
	if !locked {
		return "", fmt.Errorf("relay conversation is busy")
	}
	var override Agent
	if agent != e.agent {
		override = agent
	}
	state := e.getOrCreateInteractiveStateWith(relayKey, target.platform, target.replyCtx, session, sessions, override, sourceKey)
	state.mu.Lock()
	as, _ := state.agentSession.(SharedAgentSession)
	state.sharedSessionKey = sourceKey
	state.mu.Unlock()
	if as == nil {
		session.Unlock(gen)
		return "", fmt.Errorf("start shared relay session failed")
	}
	if as.RuntimeState().TurnID != "" {
		session.Unlock(gen)
		return "", fmt.Errorf("shared relay turn is active")
	}
	lifetime, cancel := context.WithCancel(e.ctx)
	f := &sharedForeground{ctx: lifetime, cancel: cancel, events: make(chan Event, 128)}
	state.mu.Lock()
	state.sharedForeground = f
	state.mu.Unlock()
	cleanup := func() { cancel(); e.cleanupInteractiveState(relayKey, state); session.Unlock(gen) }
	e.startSharedReader(state, as, session, sessions, relayKey)
	session.AddHistory("user", message)
	sessions.Save()
	prompt := message
	sendDone := make(chan error, 1)
	go func() {
		var id string
		var sendErr error
		if sender, ok := as.(AgentTurnSender); ok {
			id, sendErr = sender.SendTurn(prompt, "", nil, nil)
		} else {
			f.bind("")
			sendErr = as.Send(prompt, "", nil, nil)
		}
		for _, event := range f.bind(id) {
			e.handleSharedEvent(state, as, session, sessions, event)
		}
		sendDone <- sendErr
	}()
	return e.collectSharedRelay(ctx, state, session, sessions, f, sendDone, cleanup)
}

func (e *Engine) collectSharedRelay(ctx context.Context, state *interactiveState, session *Session, sessions *SessionManager, f *sharedForeground, sendDone <-chan error, cleanup func()) (string, error) {
	var parts []string
	for {
		select {
		case sendErr := <-sendDone:
			sendDone = nil
			if sendErr != nil {
				cleanup()
				return "", fmt.Errorf("send shared relay message: %w", sendErr)
			}
		case <-ctx.Done():
			go e.finishSharedRelayInBackground(session, sessions, f, sendDone, cleanup)
			return relayPartialResponseOrError(ctx.Err(), parts, "", e.name)
		case <-e.ctx.Done():
			cleanup()
			return "", e.ctx.Err()
		case event := <-f.events:
			switch event.Type {
			case EventText:
				if event.Content != "" {
					parts = append(parts, event.Content)
				}
			case EventToolResult:
				out := event.ToolResult
				if out == "" {
					out = event.Content
				}
				if out != "" {
					parts = append(parts, fmt.Sprintf(e.i18n.T(MsgToolResult), event.ToolName, out))
				}
			case EventResult:
				response := event.Content
				if response == "" {
					response = strings.Join(parts, "")
				}
				if response == "" {
					response = e.i18n.T(MsgEmptyResponse)
				}
				session.AddHistory("assistant", response)
				sessions.Save()
				cleanup()
				return response, nil
			case EventError:
				cleanup()
				return "", event.Error
			}
		}
	}
}

// Token lookup is restricted to the same source conversation. A relay card
// cannot answer an approval in another user's chat or a different workspace.
func (e *Engine) sharedRequestForSource(sourceKey, token string) (*interactiveState, SharedAgentSession) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	var found *interactiveState
	var session SharedAgentSession
	for _, state := range e.interactiveStates {
		state.mu.Lock()
		as, ok := state.agentSession.(SharedAgentSession)
		match := ok && state.sharedSessionKey == sourceKey && state.pending != nil && (token == "" || state.pending.ActionToken == token)
		state.mu.Unlock()
		if match {
			if found != nil {
				return nil, nil
			}
			found, session = state, as
		}
	}
	return found, session
}

func (e *Engine) finishSharedRelayInBackground(session *Session, sessions *SessionManager, f *sharedForeground, sendDone <-chan error, cleanup func()) {
	defer cleanup()
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case err := <-sendDone:
			sendDone = nil
			if err != nil {
				return
			}
		case event := <-f.events:
			if event.Type == EventResult {
				if event.Content != "" {
					session.AddHistory("assistant", event.Content)
					sessions.Save()
				}
				return
			}
			if event.Type == EventError {
				return
			}
		case <-timer.C:
			return
		case <-e.ctx.Done():
			return
		}
	}
}
