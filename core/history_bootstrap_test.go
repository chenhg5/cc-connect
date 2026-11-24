package core

import (
	"strings"
	"testing"
	"time"
)

func TestCUJ_B10_AgentSwitchBootstrapsHistory(t *testing.T) {
	env := newCUJEnv(t)
	key := "test:alice"
	session := env.engine.sessions.GetOrCreateActive(key)
	session.AddHistory("user", "previous user context")
	session.AddHistory("assistant", "previous answer context")
	session.SetAgentSessionID("old-native-session", "old-agent")
	env.engine.sessions.InvalidateForAgent(env.agent.Name())

	env.userSends("alice", "continue the work")
	env.waitFor("first switched turn", 2*time.Second, func() bool {
		env.agent.mu.Lock()
		defer env.agent.mu.Unlock()
		return len(env.agent.sessions) > 0 && len(env.agent.sessions[0].getSentPrompts()) > 0
	})
	env.agent.mu.Lock()
	firstSession := env.agent.sessions[0]
	env.agent.mu.Unlock()
	first := firstSession.getSentPrompts()[0]
	if !strings.Contains(first, "previous user context") || !strings.Contains(first, "previous answer context") {
		t.Fatalf("first new-agent prompt lost prior context: %q", first)
	}
	if strings.Count(first, "continue the work") != 1 {
		t.Fatalf("current turn duplicated in prompt: %q", first)
	}
	env.waitFor("bootstrap marker", 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.HistoryBootstrappedAgentType == env.agent.Name()
	})
	env.userSends("alice", "another request")
	env.waitFor("second switched turn", 2*time.Second, func() bool {
		return len(firstSession.getSentPrompts()) >= 2
	})
	if second := firstSession.getSentPrompts()[1]; strings.Contains(second, "prior conversation") {
		t.Fatalf("history injected again into same native conversation: %q", second)
	}
}

func TestHistoryBootstrapOptionsAndPersistence(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	sessions := NewSessionManager(path)
	session := sessions.GetOrCreateActive("test:user")
	session.AddHistory("user", "earliest")
	session.AddHistory("assistant", strings.Repeat("x", 200))
	session.AddHistory("user", "current")
	session.SetAgentSessionID("old-native", "old-agent")
	sessions.InvalidateForAgent("new-agent")
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	e.SetHistoryBootstrap(false, 1, 10)
	if prompt, ok := e.historyBootstrapPrompt(session, "new-agent", "current"); ok || prompt != "current" {
		t.Fatalf("disabled bootstrap changed prompt: %q", prompt)
	}
	e.SetHistoryBootstrap(true, 1, 10)
	prompt, ok := e.historyBootstrapPrompt(session, "new-agent", "current")
	if !ok || strings.Contains(prompt, "earliest") || strings.Count(prompt, "x") > 40 {
		t.Fatalf("entry/token cap failed: %q", prompt)
	}
	session.MarkHistoryBootstrapped("new-agent")
	sessions.Save()
	reloaded := NewSessionManager(path).GetOrCreateActive("test:user")
	if _, ok := e.historyBootstrapPrompt(reloaded, "new-agent", "current"); ok {
		t.Fatal("bootstrap marker was not persisted")
	}
	reloaded.SetAgentSessionID("new-native", "new-agent")
	manager := NewSessionManager("")
	manager.sessions[reloaded.ID] = reloaded
	manager.InvalidateForAgent("third-agent")
	if _, ok := e.historyBootstrapPrompt(reloaded, "third-agent", "current"); !ok {
		t.Fatal("agent switch did not reset bootstrap marker")
	}
}

func TestHistoryBootstrapDoesNotRepeatOnSameAgentResume(t *testing.T) {
	session := &Session{}
	session.AddHistory("user", "older prompt")
	session.SetAgentSessionID("first-native", "same-agent")
	session.SetAgentSessionID("resumed-native", "same-agent")
	session.AddHistory("user", "current prompt")
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	if prompt, ok := e.historyBootstrapPrompt(session, "same-agent", "current prompt"); ok || prompt != "current prompt" {
		t.Fatalf("ordinary same-agent resume repeated history: %q", prompt)
	}
}
