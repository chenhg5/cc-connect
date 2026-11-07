package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

const (
	MsgAgentTitle        MsgKey = "agent_title"
	MsgAgentCurrent      MsgKey = "agent_current"
	MsgAgentUsage        MsgKey = "agent_usage"
	MsgAgentUnknown      MsgKey = "agent_unknown"
	MsgAgentSwitched     MsgKey = "agent_switched"
	MsgAgentSwitchFailed MsgKey = "agent_switch_failed"
	MsgAgentSelect       MsgKey = "agent_select"
)

// AgentChoice is an explicitly configured agent profile. Factories must create
// independent adapters: model, permission and provider changes belong to this
// conversation, not to the project used as the configuration source.
type AgentChoice struct {
	Name string
	Type string
	New  func(workDir string) (Agent, error)
}

// SetAgentChoices is called before Start. The default name describes legacy
// sessions without an explicit choice; no existing session is reset here.
func (e *Engine) SetAgentChoices(defaultName string, choices []AgentChoice) {
	e.agentChoicesMu.Lock()
	defer e.agentChoicesMu.Unlock()
	e.defaultAgentChoice = defaultName
	e.agentChoices = append([]AgentChoice(nil), choices...)
}

func (e *Engine) agentChoiceSnapshot() (string, []AgentChoice) {
	e.agentChoicesMu.Lock()
	defer e.agentChoicesMu.Unlock()
	return e.defaultAgentChoice, append([]AgentChoice(nil), e.agentChoices...)
}

func (e *Engine) choiceAgent(base Agent, profile, interactiveKey, workDir string) (Agent, error) {
	if profile == "" {
		return base, nil
	}
	if workDir == "" {
		if wd, ok := base.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
	}
	e.agentChoicesMu.Lock()
	defer e.agentChoicesMu.Unlock()
	key := interactiveKey + "\x00" + profile
	if agent := e.choiceAgents[key]; agent != nil {
		return agent, nil
	}
	for _, choice := range e.agentChoices {
		if choice.Name != profile {
			continue
		}
		agent, err := choice.New(workDir)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", profile, err)
		}
		if e.choiceAgents == nil {
			e.choiceAgents = make(map[string]Agent)
		}
		e.choiceAgents[key] = agent
		return agent, nil
	}
	return nil, fmt.Errorf("agent profile %q is no longer configured", profile)
}

func (e *Engine) selectedAgent(base Agent, sessions *SessionManager, sessionKey, interactiveKey, workDir string) (Agent, error) {
	s := sessions.GetOrCreateActive(sessionKey)
	profile := s.GetAgentProfile()
	agent, err := e.choiceAgent(base, profile, interactiveKey, workDir)
	if err != nil {
		return nil, err
	}
	if profile != "" && s.GetAgentSessionID() != "" && s.GetAgentType() != "" && s.GetAgentType() != agent.Name() {
		return nil, fmt.Errorf("agent profile %q changed type; use /agent to start a new conversation", profile)
	}
	return agent, nil
}

func (e *Engine) currentAgentChoice(sessionKey string) string {
	_, sessions := e.baseSessionContextForKey(sessionKey)
	if profile := sessions.GetOrCreateActive(sessionKey).GetAgentProfile(); profile != "" {
		return profile
	}
	name, _ := e.agentChoiceSnapshot()
	if name == "" && e.agent != nil {
		name = e.agent.Name()
	}
	return name
}

func parseAgentSwitchArgs(args []string) (string, bool) {
	if len(args) == 1 && !strings.EqualFold(args[0], "switch") && !strings.EqualFold(args[0], "list") {
		return args[0], true
	}
	if len(args) == 2 && strings.EqualFold(args[0], "switch") {
		return args[1], true
	}
	return "", false
}

// Switching is deliberately /new semantics. Native transcripts remain on disk,
// but no old ID or history is injected into the replacement conversation.
func (e *Engine) switchSessionAgent(sessionKey, interactiveKey string, base Agent, sessions *SessionManager, workDir, target string) error {
	_, choices := e.agentChoiceSnapshot()
	name := ""
	for _, choice := range choices {
		if strings.EqualFold(choice.Name, target) {
			name = choice.Name
			break
		}
	}
	if name == "" {
		return fmt.Errorf(e.i18n.T(MsgAgentUnknown), target)
	}
	if current, err := e.selectedAgent(base, sessions, sessionKey, interactiveKey, workDir); err == nil {
		if wd, ok := current.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
	}
	// Validate/build before changing the active session. A missing CLI must
	// leave the current conversation usable.
	nextAgent, err := e.choiceAgent(base, name, interactiveKey, workDir)
	if err != nil {
		return err
	}
	if wd, ok := nextAgent.(WorkDirSwitcher); ok && workDir != "" {
		wd.SetWorkDir(workDir)
	}
	routeLock := e.agentRouteLock(interactiveKey)
	routeLock.Lock()
	defer routeLock.Unlock()
	e.cleanupInteractiveState(interactiveKey)
	old := sessions.GetOrCreateActive(sessionKey)
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	next := sessions.NewSession(sessionKey, "")
	next.SetAgentProfile(name)
	sessions.Save()
	slog.Info("session agent switched", "project", e.name, "session_key", sessionKey, "profile", name)
	return nil
}

func (e *Engine) cmdAgent(p Platform, msg *Message, args []string) {
	if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "list")) {
		card := e.renderAgentCard(msg.SessionKey)
		e.replyWithCard(p, msg.ReplyCtx, card)
		return
	}
	target, ok := parseAgentSwitchArgs(args)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAgentUsage))
		return
	}
	base, sessions, ik, wd, err := e.baseCommandContextWithWorkspace(p, msg)
	if err == nil {
		err = e.switchSessionAgent(msg.SessionKey, ik, base, sessions, wd, target)
	}
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAgentSwitchFailed, err))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAgentSwitched, e.currentAgentChoice(msg.SessionKey)))
}

func (e *Engine) renderAgentCard(sessionKey string) *Card {
	current := e.currentAgentChoice(sessionKey)
	_, choices := e.agentChoiceSnapshot()
	cb := NewCard().Title(e.i18n.T(MsgAgentTitle), "indigo").
		Markdown(e.i18n.Tf(MsgAgentCurrent, current))
	for _, choice := range choices {
		label := choice.Name
		if choice.Name == current {
			label = "✓ " + label
		}
		cb.ListItem(label, e.i18n.T(MsgAgentSelect), "act:/agent switch "+choice.Name)
	}
	return cb.Note(e.i18n.T(MsgAgentUsage)).Buttons(e.cardBackButton()).Build()
}

func (e *Engine) handleAgentCardAction(args, sessionKey string) *Card {
	// Card callbacks must enforce the same disabled-command policy as text.
	userID := extractUserID(sessionKey)
	e.userRolesMu.RLock()
	disabled := e.disabledCmds
	if e.userRoles != nil {
		if role := e.userRoles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds
		}
	}
	blocked := disabled["agent"]
	e.userRolesMu.RUnlock()
	if blocked {
		return e.simpleCard(e.i18n.T(MsgAgentTitle), "red", e.i18n.Tf(MsgCommandDisabled, "/agent"))
	}
	target, ok := parseAgentSwitchArgs(strings.Fields(args))
	if !ok {
		return e.renderAgentCard(sessionKey)
	}
	base, sessions := e.baseSessionContextForKey(sessionKey)
	err := e.switchSessionAgent(sessionKey, e.interactiveKeyForSessionKey(sessionKey), base, sessions, "", target)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgAgentTitle), "red", e.i18n.Tf(MsgAgentSwitchFailed, err))
	}
	return NewCard().Title(e.i18n.T(MsgAgentTitle), "green").
		Markdown(e.i18n.Tf(MsgAgentSwitched, e.currentAgentChoice(sessionKey))).
		Buttons(DefaultBtn(e.i18n.T(MsgAgentTitle), "nav:/agent"), e.cardBackButton()).Build()
}

func (e *Engine) stopChoiceAgents() {
	e.agentChoicesMu.Lock()
	agents := e.choiceAgents
	e.choiceAgents = nil
	e.agentChoicesMu.Unlock()
	for _, agent := range agents {
		if err := agent.Stop(); err != nil {
			slog.Warn("stop selected agent", "agent", agent.Name(), "error", err)
		}
	}
}

// Route locks are scoped to a conversation. Starting a slow CLI in one chat must
// not block agent switching or messages in another chat.
func (e *Engine) agentRouteLock(key string) *sync.RWMutex {
	e.agentChoicesMu.Lock()
	defer e.agentChoicesMu.Unlock()
	if e.agentRouteLocks == nil {
		e.agentRouteLocks = make(map[string]*sync.RWMutex)
	}
	if e.agentRouteLocks[key] == nil {
		e.agentRouteLocks[key] = &sync.RWMutex{}
	}
	return e.agentRouteLocks[key]
}

type unavailableChoiceAgent struct {
	profile string
	err     error
}

func (a *unavailableChoiceAgent) Name() string { return a.profile }
func (a *unavailableChoiceAgent) StartSession(context.Context, string) (AgentSession, error) {
	return nil, a.err
}
func (a *unavailableChoiceAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, a.err
}
func (a *unavailableChoiceAgent) Stop() error { return nil }
