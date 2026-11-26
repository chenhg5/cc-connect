package core

import (
	"context"
	"path/filepath"
	"testing"
)

type switchHistoryAgent struct{ stubAgent }

func (*switchHistoryAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return []AgentSessionInfo{{ID: "gemini-session", Summary: "previous chat", MessageCount: 2}}, nil
}

func (*switchHistoryAgent) GetSessionHistory(context.Context, string, int) ([]HistoryEntry, error) {
	return []HistoryEntry{{Role: "user", Content: "old prompt"}, {Role: "assistant", Content: "old answer"}}, nil
}

func TestSwitchHydratesAgentHistoryForCurrent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &switchHistoryAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	msg := &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "/switch 1", ReplyCtx: "ctx"}
	e.ReceiveMessage(p, msg)
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	if got := s.HistoryLen(); got != 2 {
		t.Fatalf("history after /switch = %d, want 2", got)
	}
	e.ReceiveMessage(p, msg)
	if got := s.HistoryLen(); got != 2 {
		t.Fatalf("history after repeated /switch = %d, want 2 without duplicates", got)
	}
}
