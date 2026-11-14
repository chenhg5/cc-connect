package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type historyProviderStub struct {
	stubAgent
	mu      sync.Mutex
	entries []HistoryEntry
	err     error
	ids     []string
	limits  []int
}

func (a *historyProviderStub) GetSessionHistory(_ context.Context, id string, limit int) ([]HistoryEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, id)
	a.limits = append(a.limits, limit)
	entries := a.entries
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return append([]HistoryEntry(nil), entries...), a.err
}

func (a *historyProviderStub) setEntries(entries []HistoryEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append([]HistoryEntry(nil), entries...)
}

func TestCmdHistory_NonEmptyCacheDoesNotHideAgentTranscript(t *testing.T) {
	for _, cards := range []bool{false, true} {
		name := "text"
		if cards {
			name = "card"
		}
		t.Run(name, func(t *testing.T) {
			agent := &historyProviderStub{entries: []HistoryEntry{
				{Role: "user", Content: "terminal question", Timestamp: time.Now()},
				{Role: "assistant", Content: "terminal answer", Timestamp: time.Now()},
			}}
			plain := &stubPlatformEngine{n: "history"}
			var platform Platform = plain
			cardPlatform := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "cards"}}
			if cards {
				platform = cardPlatform
			}
			e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
			msg := &Message{SessionKey: "test:history", ReplyCtx: "ctx"}
			s := e.sessions.GetOrCreateActive(msg.SessionKey)
			s.SetAgentSessionID("current-thread", "stub")
			s.AddHistory("user", "cached failed request")
			e.cmdHistory(platform, msg, nil)

			var got string
			if cards {
				if len(cardPlatform.repliedCards) != 1 {
					t.Fatalf("card replies = %d, want 1", len(cardPlatform.repliedCards))
				}
				got = cardPlatform.repliedCards[0].RenderText()
			} else {
				sent := plain.getSent()
				if len(sent) != 1 {
					t.Fatalf("text replies = %d, want 1", len(sent))
				}
				got = sent[0]
			}
			if !strings.Contains(got, "terminal question") || !strings.Contains(got, "terminal answer") {
				t.Fatalf("history hides the agent transcript: %q", got)
			}
			if strings.Contains(got, "cached failed request") {
				t.Fatalf("history uses the local cache instead of the agent transcript: %q", got)
			}
			if len(agent.ids) != 1 || agent.ids[0] != "current-thread" || agent.limits[0] != 10 {
				t.Fatalf("transcript requests = %v, limits = %v; want current-thread, 10", agent.ids, agent.limits)
			}
			if s.HistoryLen() != 1 {
				t.Fatal("reading history must not overwrite the local session cache")
			}
		})
	}
}

func TestCmdHistory_AgentTranscriptFallbackAndFailureWithCache(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   string
		err  error
	}{
		{name: "empty transcript", id: "current-thread"},
		{name: "no agent session"},
		{name: "unreadable transcript", id: "current-thread", err: errors.New("private transcript path")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			agent := &historyProviderStub{err: tt.err}
			plain := &stubPlatformEngine{n: "history"}
			e := NewEngine("test", agent, []Platform{plain}, "", LangEnglish)
			msg := &Message{SessionKey: "test:history", ReplyCtx: "ctx"}
			s := e.sessions.GetOrCreateActive(msg.SessionKey)
			s.SetAgentSessionID(tt.id, "stub")
			s.AddHistory("user", "cached question")
			s.AddHistory("assistant", "cached answer")

			e.cmdHistory(plain, msg, []string{"1"})
			sent := plain.getSent()
			if len(sent) != 1 {
				t.Fatalf("replies = %d, want 1", len(sent))
			}
			if tt.err != nil {
				if sent[0] != e.i18n.T(MsgHistoryReadFailed) {
					t.Fatalf("reply = %q, want localized read failure", sent[0])
				}
			} else if !strings.Contains(sent[0], "cached answer") || strings.Contains(sent[0], "cached question") {
				t.Fatalf("reply = %q, want only the last cached entry", sent[0])
			}
			if tt.id == "" && len(agent.ids) != 0 {
				t.Fatal("must not query a transcript before the agent session starts")
			}
		})
	}
}
