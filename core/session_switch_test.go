package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type switchFeedbackAgent struct {
	stubAgent
	mu         sync.Mutex
	list       []AgentSessionInfo
	history    map[string][]HistoryEntry
	historyErr error
	failID     string
	calls      []string
}

func (a *switchFeedbackAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AgentSessionInfo(nil), a.list...), nil
}

func (a *switchFeedbackAgent) GetSessionHistory(_ context.Context, id string, limit int) ([]HistoryEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries := a.history[id]
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return append([]HistoryEntry(nil), entries...), a.historyErr
}

type switchResultSession struct {
	*resultAgentSession
	id string
}

func (s *switchResultSession) CurrentSessionID() string { return s.id }

func (a *switchFeedbackAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, id)
	if id == a.failID && id != "" {
		return nil, errors.New("session is busy in another window")
	}
	if id == "" {
		id = "explicit-fresh"
	}
	return &switchResultSession{resultAgentSession: newResultAgentSession("fresh reply"), id: id}, nil
}

func (a *switchFeedbackAgent) startedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func newSwitchFeedbackAgent() *switchFeedbackAgent {
	return &switchFeedbackAgent{
		list:    []AgentSessionInfo{{ID: "old-id", Summary: "Old project", MessageCount: 2}, {ID: "other-id", Summary: "Other project"}},
		history: map[string][]HistoryEntry{"old-id": {{Role: "assistant", Content: "latest backend answer", Timestamp: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}}},
	}
}

func switchUserSend(e *Engine, p Platform, text string) {
	e.ReceiveMessage(p, &Message{SessionKey: "test:alice", UserID: "alice", Platform: "test", Content: text, MessageID: text, ReplyCtx: "chat"})
}

// Extends CUJ-B3: select a known conversation, fail its resume, retry, then
// explicitly choose /new. Only the last action permits a fresh conversation.
func testCUJSwitchResumeFailure(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := newSwitchFeedbackAgent()
	a.failID = "old-id"
	store := filepath.Join(t.TempDir(), "sessions.json")
	e := NewEngine("test", a, []Platform{p}, store, LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	old := e.sessions.GetOrCreateActive("test:alice")
	old.SetAgentSessionID("old-id", a.Name())
	old.AddHistory("assistant", "local old answer")
	e.sessions.NewSession("test:alice", "other")

	switchUserSend(e, p, "/list")
	switchUserSend(e, p, "/switch old-id")
	waitAsyncInput(t, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "latest backend answer") })
	if len(a.startedIDs()) != 0 {
		t.Fatal("preview must not start an agent or send a prompt")
	}
	p.clearSent()
	switchUserSend(e, p, "continue old")
	waitAsyncInput(t, func() bool { return !old.Busy() && len(p.getSent()) > 0 })
	visible := strings.Join(p.getSent(), "\n")
	if !strings.Contains(visible, "this message was not sent") || !strings.Contains(visible, "busy in another window") || !strings.Contains(visible, "/new") {
		t.Fatalf("failed resume silently changed context instead of explaining the failure: %s", visible)
	}
	if got := a.startedIDs(); len(got) != 1 || got[0] != "old-id" {
		t.Fatalf("failed resume started another conversation: %v", got)
	}
	if old.GetAgentSessionID() != "old-id" || len(old.GetHistory(0)) != 1 {
		t.Fatal("failed resume changed the selected ID or recorded an unsent message")
	}
	loaded := NewSessionManager(store).GetOrCreateActive("test:alice")
	if loaded.GetAgentSessionID() != "old-id" {
		t.Fatal("saved selection was lost across reload")
	}
	switchUserSend(e, p, "retry old")
	waitAsyncInput(t, func() bool { return len(a.startedIDs()) == 2 && !old.Busy() })
	if got := a.startedIDs(); got[1] != "old-id" {
		t.Fatalf("retry silently changed context: %v", got)
	}
	switchUserSend(e, p, "/new")
	waitAsyncInput(t, func() bool { return e.sessions.GetOrCreateActive("test:alice").ID != old.ID })
	switchUserSend(e, p, "fresh task")
	waitAsyncInput(t, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "fresh reply") })
	if got := a.startedIDs(); len(got) != 3 || got[2] != "" {
		t.Fatalf("explicit /new did not start a fresh conversation: %v", got)
	}
	if old.GetAgentSessionID() != "old-id" || len(old.GetHistory(0)) != 1 {
		t.Fatal("explicit /new destroyed the old conversation")
	}
}

type switchFeedbackPlatform struct {
	stubCardPlatform
	navigate CardNavigationHandler
}

func (p *switchFeedbackPlatform) SetCardNavigationHandler(h CardNavigationHandler) { p.navigate = h }

func testCUJSwitchStableCard(t *testing.T) {
	p := &switchFeedbackPlatform{}
	p.n = "test"
	a := newSwitchFeedbackAgent()
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.initPlatformCapabilities(p)
	switchUserSend(e, p, "/list")
	p.mu.Lock()
	list := p.repliedCards[len(p.repliedCards)-1]
	p.mu.Unlock()
	button, ok := findCardAction(list, "act:/switch --id old-id")
	if !ok {
		t.Fatal("session list button must bind a stable backend ID")
	}
	// Another window updates list ordering before the user clicks the card.
	a.mu.Lock()
	a.list[0], a.list[1] = a.list[1], a.list[0]
	a.mu.Unlock()
	confirmation := p.navigate(button.Value, "test:alice")
	if confirmation == nil || !strings.Contains(confirmation.RenderText(), "latest backend answer") || !strings.Contains(confirmation.RenderText(), "Old project") {
		t.Fatalf("card switch omits target confirmation and latest message: %+v", confirmation)
	}
	if e.sessions.GetOrCreateActive("test:alice").GetAgentSessionID() != "old-id" {
		t.Fatal("list reorder changed the selected conversation")
	}
	switchUserSend(e, p, "/history")
	p.mu.Lock()
	history := p.repliedCards[len(p.repliedCards)-1].RenderText()
	p.mu.Unlock()
	if !strings.Contains(history, "latest backend answer") || len(a.startedIDs()) != 0 {
		t.Fatalf("switch preview and /history disagree or started an agent: %s", history)
	}
}

func TestSwitchPreview_UnavailableEmptyAndBoundedUnicode(t *testing.T) {
	for _, mode := range []string{"unavailable", "empty", "long"} {
		t.Run(mode, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			a := newSwitchFeedbackAgent()
			switch mode {
			case "unavailable":
				a.historyErr = errors.New("transcript is unavailable")
			case "empty":
				a.history = nil
			case "long":
				a.history["old-id"] = []HistoryEntry{{Role: "user", Content: strings.Repeat("苹果", 1000)}}
			}
			e := NewEngine("test", a, []Platform{p}, "", LangChinese)
			defer e.cancel()
			switchUserSend(e, p, "/switch old-id")
			text := strings.Join(p.getSent(), "\n")
			if e.sessions.GetOrCreateActive("test:alice").GetAgentSessionID() != "old-id" || !strings.Contains(text, e.i18n.T(MsgSwitchResumePending)) {
				t.Fatalf("preview failure changed selection or implied successful backend resume: %s", text)
			}
			switch mode {
			case "unavailable":
				if !strings.Contains(text, e.i18n.T(MsgSwitchPreviewUnavailable)) {
					t.Fatal(text)
				}
			case "empty":
				if !strings.Contains(text, e.i18n.T(MsgSwitchPreviewEmpty)) {
					t.Fatal(text)
				}
			case "long":
				if !utf8.ValidString(text) || strings.Count(text, "苹果") != 300 || !strings.Contains(text, "👤") {
					t.Fatal("preview was not bounded without corrupting Unicode")
				}
			}
		})
	}
}

func TestUnsafeResume_BlocksRetriesAndAllowsExplicitNew(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &stubStartSessionAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	old := &Session{AgentSessionID: "possibly-live"}
	e.markUnsafeSessionID("test:alice", "possibly-live")
	for range 2 {
		state := e.getOrCreateInteractiveStateWith("test:alice", p, "chat", old, e.sessions, nil, "")
		if state.agentSession != nil || !strings.Contains(state.startupError, "not sent") || old.GetAgentSessionID() != "possibly-live" {
			t.Fatal("unsafe resume lost its selection or allowed a retry")
		}
	}
	if len(a.calls) != 0 {
		t.Fatal("unsafe resume started a process")
	}
	e.getOrCreateInteractiveStateWith("test:alice", p, "chat", &Session{}, e.sessions, nil, "")
	if len(a.calls) != 1 || a.calls[0] != "" {
		t.Fatal("explicit new conversation was blocked")
	}
	state := e.getOrCreateInteractiveStateWith("test:alice", p, "chat", old, e.sessions, nil, "")
	if state.agentSession != nil || len(a.calls) != 1 {
		t.Fatal("creating another conversation erased the unsafe guard for the old one")
	}
}

func TestSwitchCard_ExactIDDoesNotMatchIndexOrPrefix(t *testing.T) {
	for _, id := range []string{"1", "old-id"} {
		t.Run(id, func(t *testing.T) {
			a := newSwitchFeedbackAgent()
			a.list = []AgentSessionInfo{{ID: id + "0", Summary: "Wrong conversation"}, {ID: id, Summary: "Selected conversation"}}
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			defer e.cancel()
			card := e.handleCardNav("act:/switch --id "+id, "test:alice")
			if e.sessions.GetOrCreateActive("test:alice").GetAgentSessionID() != id || !strings.Contains(card.RenderText(), "Selected conversation") {
				t.Fatal("stable ID was interpreted as a list number or ID prefix")
			}
		})
	}
}
