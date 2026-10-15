package core

import (
	"context"
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
