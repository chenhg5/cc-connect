package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type presetTestAgent struct {
	stubAgent
	options []PresetOption
}

func (a *presetTestAgent) AvailablePresets(context.Context) []PresetOption {
	return append([]PresetOption(nil), a.options...)
}

type presetStarterTestAgent struct {
	presetTestAgent
	startedPreset string
}

func (a *presetStarterTestAgent) StartSessionWithPreset(_ context.Context, _ string, preset string) (AgentSession, error) {
	a.startedPreset = preset
	return &stubAgentSession{}, nil
}

func TestCmdPreset_ListsAndSelectsBlankSession(t *testing.T) {
	agent := &presetTestAgent{options: []PresetOption{
		{ID: "standard", Name: "Standard", Description: "full surface", Default: true},
		{ID: "minimal", Name: "Minimal", Description: "small surface"},
	}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	if !e.handleCommand(p, msg, "/preset") {
		t.Fatal("/preset should be handled")
	}
	sent := p.getSent()
	if len(sent) != 1 || sent[0] == "" {
		t.Fatalf("list reply = %#v", sent)
	}
	if len(sent) == 1 && !strings.HasPrefix(sent[0], "Available presets:") {
		t.Fatalf("list reply = %q, want available preset list", sent[0])
	}

	p.clearSent()
	if !e.handleCommand(p, msg, "/preset minimal") {
		t.Fatal("/preset minimal should be handled")
	}
	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if got := session.GetAgentPreset(); got != "minimal" {
		t.Fatalf("AgentPreset = %q, want minimal", got)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] == "" {
		t.Fatalf("switch reply = %#v", sent)
	}
}

func TestCmdPreset_UsesCardOnCardPlatform(t *testing.T) {
	agent := &presetTestAgent{options: []PresetOption{
		{ID: "standard", Name: "Standard", Default: true},
		{ID: "minimal", Name: "Minimal"},
	}}
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:chat:user1", ReplyCtx: "ctx"}

	if !e.handleCommand(p, msg, "/preset") {
		t.Fatal("/preset should be handled")
	}
	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	var foundSelect bool
	for _, element := range p.repliedCards[0].Elements {
		if _, ok := element.(CardSelect); ok {
			foundSelect = true
			break
		}
	}
	if !foundSelect {
		t.Fatalf("preset card elements = %#v, want CardSelect", p.repliedCards[0].Elements)
	}
}

func TestHandleCardNav_PresetSwitchesBlankSession(t *testing.T) {
	agent := &presetTestAgent{options: []PresetOption{
		{ID: "standard", Default: true},
		{ID: "minimal"},
	}}
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	sessionKey := "feishu:chat:user1"

	card := e.handleCardNav("act:/preset minimal", sessionKey)
	if card == nil {
		t.Fatal("expected preset result card")
	}
	if got := e.sessions.GetOrCreateActive(sessionKey).GetAgentPreset(); got != "minimal" {
		t.Fatalf("AgentPreset = %q, want minimal", got)
	}
	if text := card.RenderText(); !strings.Contains(text, "Preset switched to `minimal`") {
		t.Fatalf("result card = %q", text)
	}
}

func TestCmdPreset_RejectsStartedSession(t *testing.T) {
	agent := &presetTestAgent{options: []PresetOption{
		{ID: "standard", Default: true},
		{ID: "minimal"},
	}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.AddHistory("user", "already started")

	if !e.handleCommand(p, msg, "/preset minimal") {
		t.Fatal("/preset minimal should be handled")
	}
	if got := session.GetAgentPreset(); got != "" {
		t.Fatalf("AgentPreset = %q after locked switch, want empty", got)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] == "" {
		t.Fatalf("locked reply = %#v", sent)
	}
}

func TestInteractiveStartPassesPendingPreset(t *testing.T) {
	agent := &presetStarterTestAgent{presetTestAgent: presetTestAgent{options: []PresetOption{{ID: "minimal"}}}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	session := &Session{AgentPreset: "minimal"}

	e.getOrCreateInteractiveStateWith("test:user1", p, "ctx", session, e.sessions, nil, "")
	if agent.startedPreset != "minimal" {
		t.Fatalf("started preset = %q, want minimal", agent.startedPreset)
	}
}

func TestSessionManager_PersistsAgentPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	manager := NewSessionManager(path)
	session := manager.GetOrCreateActive("test:user1")
	session.SetAgentPreset("minimal")
	manager.Save()

	reloaded := NewSessionManager(path)
	got := reloaded.GetOrCreateActive("test:user1").GetAgentPreset()
	if got != "minimal" {
		t.Fatalf("reloaded AgentPreset = %q, want minimal", got)
	}
}
