package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type switchTestAgent struct {
	stubAgent
	mu                             sync.Mutex
	name, dir, model, mode, effort string
	delayMs                        int
	starts                         []string
	native                         []*switchTestSession
}

type switchTestSession struct {
	*cujAgentSession
	id string
}

func (s *switchTestSession) CurrentSessionID() string { return s.id }
func (a *switchTestAgent) Name() string               { return a.name }
func (a *switchTestAgent) GetWorkDir() string         { a.mu.Lock(); defer a.mu.Unlock(); return a.dir }
func (a *switchTestAgent) SetWorkDir(v string)        { a.mu.Lock(); defer a.mu.Unlock(); a.dir = v }
func (a *switchTestAgent) GetModel() string           { a.mu.Lock(); defer a.mu.Unlock(); return a.model }
func (a *switchTestAgent) SetModel(v string)          { a.mu.Lock(); defer a.mu.Unlock(); a.model = v }
func (a *switchTestAgent) AvailableModels(context.Context) []ModelOption {
	return []ModelOption{{Name: a.name + "/new-model"}}
}
func (a *switchTestAgent) GetMode() string  { return a.mode }
func (a *switchTestAgent) SetMode(v string) { a.mode = v }
func (a *switchTestAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "yolo", Name: "YOLO"}}
}
func (a *switchTestAgent) GetReasoningEffort() string          { return a.effort }
func (a *switchTestAgent) SetReasoningEffort(v string)         { a.effort = v }
func (a *switchTestAgent) AvailableReasoningEfforts() []string { return []string{"high"} }
func (a *switchTestAgent) StartSession(_ context.Context, resume string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, resume)
	s := &switchTestSession{cujAgentSession: newCUJAgentSession(), id: fmt.Sprintf("%s-%d", a.name, len(a.starts))}
	s.reply = "ANSWER:" + a.name + ":" + a.model
	s.delayMs = a.delayMs
	a.native = append(a.native, s)
	return s, nil
}

func switchTestReceive(e *Engine, p Platform, key, content string) {
	e.ReceiveMessage(p, &Message{SessionKey: key, Platform: p.Name(), UserID: extractUserID(key), Content: content, ReplyCtx: key})
}

func switchTestWait(t *testing.T, e *Engine, p *stubPlatformEngine, key, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, sm := e.sessionContextForKey(key)
		if strings.Contains(strings.Join(p.getSent(), "\n"), want) && !sm.GetOrCreateActive(key).Busy() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing visible result %q: %v", want, p.getSent())
}

func switchTestEngine(t *testing.T) (*Engine, *switchTestAgent) {
	t.Helper()
	base := &switchTestAgent{name: "base", dir: t.TempDir(), model: "base-model"}
	e := NewEngine("switch-test", base, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	e.SetAgentChoices("base", []AgentChoice{
		{Name: "base", Type: "base", New: func(wd string) (Agent, error) {
			return &switchTestAgent{name: "base", dir: wd, model: "base-model"}, nil
		}},
		{Name: "second", Type: "second", New: func(wd string) (Agent, error) {
			return &switchTestAgent{name: "second", dir: wd, model: "second-model"}, nil
		}},
		{Name: "third", Type: "third", New: func(wd string) (Agent, error) {
			return &switchTestAgent{name: "third", dir: wd, model: "third-model"}, nil
		}},
		{Name: "broken", Type: "broken", New: func(string) (Agent, error) { return nil, errors.New("CLI missing") }},
	})
	t.Cleanup(func() { _ = e.Stop() })
	return e, base
}

func TestAgentSwitchFailureAndDisabledCardsPreserveConversation(t *testing.T) {
	e, _ := switchTestEngine(t)
	p := &stubPlatformEngine{n: "test"}
	key := "test:chat:user"
	s := e.sessions.GetOrCreateActive(key)
	s.SetAgentSessionID("original", "base")
	s.AddHistory("user", "keep this")
	for _, cmd := range []string{"/agent missing", "/agent broken", "/agent switch", "/agent list extra"} {
		switchTestReceive(e, p, key, cmd)
		if e.sessions.ActiveSessionID(key) != s.ID || s.GetAgentSessionID() != "original" || s.HistoryLen() != 1 {
			t.Fatalf("%s destroyed the current conversation", cmd)
		}
	}
	e.disabledCmds = map[string]bool{"agent": true}
	switchTestReceive(e, p, key, "/agent second")
	card := e.handleCardNav("act:/agent switch second", key)
	if !strings.Contains(card.RenderText(), "disabled") || e.sessions.ActiveSessionID(key) != s.ID {
		t.Fatal(card.RenderText())
	}
}

func TestAgentSelectionSurvivesRestart(t *testing.T) {
	e, _ := switchTestEngine(t)
	p := &stubPlatformEngine{n: "test"}
	key := "test:chat:user"
	switchTestReceive(e, p, key, "/agent second")
	switchTestReceive(e, p, key, "task before restart")
	switchTestWait(t, e, p, key, "ANSWER:second")
	sid := e.sessions.GetOrCreateActive(key).GetAgentSessionID()
	e.sessions.Save()
	reloaded := NewEngine("switch-test", &switchTestAgent{name: "base"}, nil, e.sessions.StorePath(), LangEnglish)
	defer func() { _ = reloaded.Stop() }()
	_, choices := e.agentChoiceSnapshot()
	reloaded.SetAgentChoices("base", choices)
	s := reloaded.sessions.GetOrCreateActive(key)
	if s.GetAgentProfile() != "second" || s.GetAgentSessionID() != sid {
		t.Fatalf("restart invalidated selected agent: %+v", s)
	}
	p.clearSent()
	switchTestReceive(reloaded, p, key, "continue after restart")
	switchTestWait(t, reloaded, p, key, "ANSWER:second")
	a, _ := reloaded.sessionContextForKey(key)
	chosen := a.(*switchTestAgent)
	chosen.mu.Lock()
	defer chosen.mu.Unlock()
	if len(chosen.starts) != 1 || chosen.starts[0] != sid {
		t.Fatalf("restart did not resume selected native session: %v", chosen.starts)
	}
}

func TestAgentSwitchCancelsActiveTurnAndKeepsDirectory(t *testing.T) {
	e, base := switchTestEngine(t)
	base.delayMs = 200
	p := &stubPlatformEngine{n: "test"}
	key := "test:chat:user"
	switchTestReceive(e, p, key, "slow old task")
	deadline := time.Now().Add(3 * time.Second)
	for {
		base.mu.Lock()
		started := len(base.native) > 0
		base.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old native session did not start")
		}
		time.Sleep(time.Millisecond)
	}
	switchTestReceive(e, p, key, "/agent second")
	selected, _ := e.sessionContextForKey(key)
	wd := t.TempDir()
	selected.(*switchTestAgent).SetWorkDir(wd)
	switchTestReceive(e, p, key, "/agent third")
	selected, _ = e.sessionContextForKey(key)
	if selected.(*switchTestAgent).GetWorkDir() != wd {
		t.Fatal("switch lost the current directory")
	}
	switchTestReceive(e, p, key, "fresh new task")
	switchTestWait(t, e, p, key, "ANSWER:third")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(strings.Join(p.getSent(), "\n"), "ANSWER:base") {
		t.Fatal("canceled agent replied after the switch")
	}
	base.mu.Lock()
	old := base.native[0]
	base.mu.Unlock()
	if old.Alive() {
		t.Fatal("old native session was not closed")
	}
}

func TestAgentProfileUnavailableNeverFallsBackToDefault(t *testing.T) {
	e, base := switchTestEngine(t)
	p := &stubPlatformEngine{n: "test"}
	key := "test:chat:user"
	s := e.sessions.GetOrCreateActive(key)
	s.SetAgentProfile("removed")
	s.SetAgentSessionID("old-id", "second")
	switchTestReceive(e, p, key, "must not reach default")
	e.handleCardNav("act:/model switch accidentally-global", key)
	if base.GetModel() != "base-model" {
		t.Fatal("missing profile changed default model")
	}
	base.mu.Lock()
	count := len(base.starts)
	base.mu.Unlock()
	if count != 0 {
		t.Fatal("missing profile ran the default CLI")
	}
	s.SetAgentProfile("third")
	if _, err := e.selectedAgent(base, e.sessions, key, key, ""); err == nil {
		t.Fatal("native type change reused an incompatible session")
	}
	switchTestReceive(e, p, key, "/agent third")
	if e.sessions.GetOrCreateActive(key).GetAgentSessionID() != "" {
		t.Fatal("explicit switch did not recover with /new semantics")
	}
}
