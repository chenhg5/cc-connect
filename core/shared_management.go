package core

import "fmt"

func (e *Engine) switchSharedManagementSession(sourceKey, targetID string) (*Session, error) {
	agent, sessions := e.sessionContextForKey(sourceKey)
	var targetSession *Session
	for _, candidate := range sessions.ListSessions(sourceKey) {
		if candidate.ID == targetID || candidate.GetName() == targetID {
			targetSession = candidate
			break
		}
	}
	if targetSession == nil {
		return nil, fmt.Errorf("session %q not found", targetID)
	}
	threadID := targetSession.GetAgentSessionID()
	key := e.interactiveKeyForSessionKey(sourceKey)
	if threadID == "" {
		s, err := sessions.SwitchSession(sourceKey, targetID)
		if err == nil {
			e.cleanupInteractiveState(key)
		}
		return s, err
	}
	target, err := e.resolveSendTarget(sourceKey, false)
	if err != nil {
		return nil, err
	}
	if target.platform == nil {
		return nil, fmt.Errorf("session has no reply platform")
	}
	attacher, ok := agent.(AgentSessionAttacher)
	if !ok {
		return nil, fmt.Errorf("session does not support shared attachment")
	}
	as, err := attacher.AttachSession(e.ctx, threadID)
	if err != nil {
		return nil, err
	}
	shared, ok := as.(SharedAgentSession)
	if !ok {
		_ = as.Close()
		return nil, fmt.Errorf("attached session is not shared")
	}
	s, err := sessions.SwitchSession(sourceKey, targetID)
	if err != nil {
		_ = as.Close()
		return nil, err
	}
	e.cleanupInteractiveState(key)
	state := &interactiveState{agentSession: as, busySession: s, agent: agent, platform: target.platform, replyCtx: target.replyCtx, shared: sharedSessionState{sessionKey: sourceKey}, eventsNeedResync: false}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	e.hooks.Emit(HookEvent{Event: HookEventSessionStarted, SessionKey: sourceKey, Platform: target.platform.Name()})
	e.startSharedReader(state, shared, s, sessions, key)
	return s, nil
}
