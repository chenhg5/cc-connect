package core

import (
	"context"
	"testing"
)

type stubRenamerAgent struct {
	stubAgent
	names map[string]string
}

func (a *stubRenamerAgent) GetSessionDisplayName(_ context.Context, sessionID string) (string, error) {
	if a.names == nil {
		return "", nil
	}
	return a.names[sessionID], nil
}

func (a *stubRenamerAgent) SetSessionDisplayName(_ context.Context, sessionID, name string) error {
	if a.names == nil {
		a.names = make(map[string]string)
	}
	a.names[sessionID] = name
	return nil
}

func TestSyncSessionNamesFromAgent_ImportsTerminalRename(t *testing.T) {
	agent := &stubRenamerAgent{
		names: map[string]string{
			"sess-1": "Terminal Title",
		},
	}
	e := NewEngine("test", agent, nil, t.TempDir()+"/sessions.json", LangEnglish)
	sm := e.sessions

	e.syncSessionNamesFromAgent(agent, sm, []AgentSessionInfo{{ID: "sess-1"}})

	if got := sm.GetSessionName("sess-1"); got != "Terminal Title" {
		t.Fatalf("GetSessionName() = %q, want %q", got, "Terminal Title")
	}
}

func TestSyncSessionNamesFromAgent_SkipsDefaultPlaceholder(t *testing.T) {
	agent := &stubRenamerAgent{
		names: map[string]string{
			"sess-1": "New Agent",
		},
	}
	e := NewEngine("test", agent, nil, t.TempDir()+"/sessions.json", LangEnglish)
	sm := e.sessions
	sm.SetSessionName("sess-1", "Feishu Name")

	e.syncSessionNamesFromAgent(agent, sm, []AgentSessionInfo{{ID: "sess-1"}})

	if got := sm.GetSessionName("sess-1"); got != "Feishu Name" {
		t.Fatalf("GetSessionName() = %q, want preserved %q", got, "Feishu Name")
	}
}

func TestCmdName_ExportsToAgentRenamer(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubRenamerAgent{}
	e := NewEngine("test", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.SetAgentSessionID("sess-export", "cursor")

	e.cmdName(p, msg, []string{"My Feishu Name"})

	if got := agent.names["sess-export"]; got != "My Feishu Name" {
		t.Fatalf("agent name = %q, want %q", got, "My Feishu Name")
	}
	if got := e.sessions.GetSessionName("sess-export"); got != "My Feishu Name" {
		t.Fatalf("session_names = %q, want %q", got, "My Feishu Name")
	}
}
