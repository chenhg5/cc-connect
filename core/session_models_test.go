package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sessionModelRecordingAgent records the session model in effect at each StartSession.
type sessionModelRecordingAgent struct {
	stubAgent
	mu      sync.Mutex
	current string
	started []string
}

func (a *sessionModelRecordingAgent) SetSessionModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = model
}

func (a *sessionModelRecordingAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, a.current)
	return &stubAgentSession{}, nil
}

func TestSessionModels_AppliedPerSessionKeyBeforeStart(t *testing.T) {
	p := &stubPlatformEngine{n: "discord"}
	agent := &sessionModelRecordingAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSessionModels(map[string]string{
		" discord:outing ": " gpt-6-luna ",
		"discord:blank":    "  ",
	})

	for _, key := range []string{"discord:outing", "discord:work", "discord:blank"} {
		e.getOrCreateInteractiveStateWith(key, p, "ctx", &Session{}, e.sessions, nil, "")
	}
	// Workspace mode: the interactive key differs, the cc session key is what is mapped.
	e.getOrCreateInteractiveStateWith("/ws:discord:outing", p, "ctx", &Session{}, e.sessions, nil, "discord:outing")

	want := []string{"gpt-6-luna", "", "", "gpt-6-luna"}
	if len(agent.started) != len(want) {
		t.Fatalf("started %d sessions, want %d: %q", len(agent.started), len(want), agent.started)
	}
	for i := range want {
		if agent.started[i] != want[i] {
			t.Fatalf("session %d started with model %q, want %q (all: %q)", i, agent.started[i], want[i], agent.started)
		}
	}
}

type sessionInstrRecordingAgent struct {
	stubAgent
	mu      sync.Mutex
	current string
	started []string
}

func (a *sessionInstrRecordingAgent) SetSessionInstructions(path, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = path + "=" + text
}

func (a *sessionInstrRecordingAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, a.current)
	return &stubAgentSession{}, nil
}

func TestSessionInstructions_AppliedPerSessionKeyBeforeStart(t *testing.T) {
	p := &stubPlatformEngine{n: "discord"}
	agent := &sessionInstrRecordingAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	persona := filepath.Join(t.TempDir(), "persona.md")
	if err := os.WriteFile(persona, []byte("\n你是龙娘。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.SetSessionInstructions(map[string]string{
		" discord:houzhai ": " " + persona + " ",
		"discord:blank":     "  ",
	})
	// The text is held from load time: deleting the file later changes nothing.
	if err := os.Remove(persona); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"discord:houzhai", "discord:work", "discord:blank"} {
		e.getOrCreateInteractiveStateWith(key, p, "ctx", &Session{}, e.sessions, nil, "")
	}
	want := []string{persona + "=你是龙娘。", "=", "="}
	if strings.Join(agent.started, "|") != strings.Join(want, "|") {
		t.Fatalf("started with %q, want %q", agent.started, want)
	}
}
