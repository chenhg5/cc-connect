package core

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type startupFailurePlatform struct {
	stubPlatformEngine
	replies     map[any][]string
	beforeReply func(any, string)
}

func (p *startupFailurePlatform) record(ctx any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replies == nil {
		p.replies = make(map[any][]string)
	}
	p.replies[ctx] = append(p.replies[ctx], text)
	p.sent = append(p.sent, text)
	return nil
}

func (p *startupFailurePlatform) Reply(_ context.Context, ctx any, text string) error {
	if p.beforeReply != nil {
		p.beforeReply(ctx, text)
	}
	return p.record(ctx, text)
}

func (p *startupFailurePlatform) Send(_ context.Context, ctx any, text string) error {
	return p.record(ctx, text)
}

func (p *startupFailurePlatform) repliesFor(ctx any) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.replies[ctx]...)
}

type blockedResumeAgent struct {
	stubAgent
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   []string
	sent    []string
}

func (a *blockedResumeAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.calls = append(a.calls, id)
	first := len(a.calls) == 1
	a.mu.Unlock()
	if first {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, errors.New("selected transcript unavailable")
	}
	return &startupRecordingSession{agent: a, events: make(chan Event, 4), id: id}, nil
}

type startupRecordingSession struct {
	stubAgentSession
	agent  *blockedResumeAgent
	events chan Event
	id     string
}

func (s *startupRecordingSession) CurrentSessionID() string { return s.id }
func (s *startupRecordingSession) Events() <-chan Event     { return s.events }
func (s *startupRecordingSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.agent.mu.Lock()
	s.agent.sent = append(s.agent.sent, prompt)
	s.agent.mu.Unlock()
	s.events <- Event{Type: EventResult, Content: "retry complete", Done: true}
	return nil
}

// Queue before the startup goroutine acquires interactiveMu: StartSession
// currently holds that mutex, so queueing during the call itself waits for it.
// Both schedules must terminate every accepted message instead of replaying it.
func testCUJResumeFailureTerminatesQueuedMessages(t *testing.T) {
	p := &startupFailurePlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	a := &blockedResumeAgent{started: make(chan struct{}), release: make(chan struct{})}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:alice"
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("old-id", a.Name())
	session.AddHistory("assistant", "saved answer")
	queued := &Message{SessionKey: key, Platform: "test", UserID: "alice", Content: "queued before failure", MessageID: "second", ReplyCtx: "second"}
	e.ReceiveMessage(p, &Message{
		SessionKey: key, Platform: "test", UserID: "alice", Content: "first attempt", MessageID: "first", ReplyCtx: "first",
		OnAccepted: func() {
			// The foreground turn owns the busy lock but has not started yet.
			done := make(chan struct{})
			go func() { e.ReceiveMessage(p, queued); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("second message did not queue before startup")
			}
		},
	})
	select {
	case <-a.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first message did not reach StartSession")
	}
	if got := p.repliesFor("second"); len(got) != 1 || !strings.Contains(got[0], e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("second message was not acknowledged as queued: %v", got)
	}
	close(a.release)
	waitAsyncInput(t, func() bool { return !session.Busy() })
	terminal := p.repliesFor("second")
	if len(terminal) != 2 || !strings.Contains(terminal[1], "selected transcript unavailable") || !strings.Contains(terminal[1], "/new") {
		t.Errorf("queued sender did not receive a terminal startup failure: %v", terminal)
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	state.mu.Lock()
	pending := len(state.pendingMessages)
	state.mu.Unlock()
	e.interactiveMu.Unlock()
	if pending != 0 {
		t.Errorf("failed startup retained %d queued messages", pending)
	}
	if session.GetAgentSessionID() != "old-id" || len(session.GetHistory(0)) != 1 {
		t.Fatal("failed startup changed the saved conversation or recorded unsent messages")
	}
	// User action 3: retry the selected conversation once the backend recovers.
	switchUserSend(e, p, "later retry")
	waitAsyncInput(t, func() bool {
		return !session.Busy() && strings.Contains(strings.Join(p.getSent(), "\n"), "retry complete")
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if !reflect.DeepEqual(a.calls, []string{"old-id", "old-id"}) || len(a.sent) != 1 || !strings.Contains(a.sent[0], "later retry") {
		t.Fatalf("retry replayed a failed queued message: calls=%v sent=%v", a.calls, a.sent)
	}
}

func testCUJNewAfterFailedResume(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := newSwitchFeedbackAgent()
	a.failID = "old-id"
	store := filepath.Join(t.TempDir(), "sessions.json")
	e := NewEngine("test", a, []Platform{p}, store, LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:alice"
	old := e.sessions.GetOrCreateActive(key)
	old.SetAgentSessionID("old-id", a.Name())
	old.AddHistory("user", "original question")
	old.AddHistory("assistant", "original answer")
	wantHistory := old.GetHistory(0)
	switchUserSend(e, p, "resume old")
	waitAsyncInput(t, func() bool { return !old.Busy() && len(p.getSent()) > 0 })
	switchUserSend(e, p, "/new")
	loaded := NewSessionManager(store)
	if active := loaded.GetOrCreateActive(key); active.ID == old.ID || active.GetAgentSessionID() != "" || len(active.GetHistory(0)) != 0 {
		t.Fatal("/new did not persist a separate, empty conversation")
	}
	var retained *Session
	for _, s := range loaded.ListSessions(key) {
		if s.ID == old.ID {
			retained = s
		}
	}
	if retained == nil || retained.GetAgentSessionID() != "old-id" || len(retained.GetHistory(0)) != len(wantHistory) {
		t.Fatalf("/new destroyed the saved ID or history: %+v", retained)
	}
	for i, entry := range retained.GetHistory(0) {
		want := wantHistory[i]
		if entry.Role != want.Role || entry.Content != want.Content || !entry.Timestamp.Equal(want.Timestamp) {
			t.Fatalf("/new changed saved history entry %d: %+v", i, entry)
		}
	}
	a.mu.Lock()
	a.failID = ""
	a.mu.Unlock()
	switchUserSend(e, p, "/switch old-id")
	switchUserSend(e, p, "resume after new")
	waitAsyncInput(t, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "fresh reply") && !old.Busy() })
	if got := a.startedIDs(); !reflect.DeepEqual(got, []string{"old-id", "old-id"}) {
		t.Fatalf("switching back did not resume the retained ID: %v", got)
	}
}

func TestResumeFailure_RejectsQueueArrivalAfterStartupBlocked(t *testing.T) {
	p := &startupFailurePlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	a := &blockedResumeAgent{started: make(chan struct{}), release: make(chan struct{})}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	failureReply := make(chan struct{})
	releaseReply := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReply) }) }
	t.Cleanup(func() { release(); _ = e.Stop() })
	p.beforeReply = func(ctx any, _ string) {
		if ctx == "first" {
			close(failureReply)
			<-releaseReply
		}
	}
	const key = "test:alice"
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("old-id", a.Name())
	e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", Content: "first attempt", ReplyCtx: "first"})
	select {
	case <-a.started:
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not start")
	}
	queued := make(chan struct{})
	arriving := make(chan struct{})
	go func() {
		close(arriving)
		switchUserSend(e, p, "arrived during startup")
		close(queued)
	}()
	<-arriving
	close(a.release)
	select {
	case <-failureReply:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground did not reach its failure reply")
	}
	select {
	case <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent message did not receive its terminal reply")
	}
	if got := p.repliesFor("chat"); len(got) != 1 || !strings.Contains(got[0], "selected transcript unavailable") {
		t.Fatalf("message arriving after failure was acknowledged as queued: %v", got)
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	state.mu.Lock()
	pending := len(state.pendingMessages)
	state.mu.Unlock()
	e.interactiveMu.Unlock()
	if pending != 0 {
		t.Fatal("blocked startup accepted a message after the failure drain")
	}
	release()
	waitAsyncInput(t, func() bool { return !session.Busy() })
}

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

type deadlineSwitchHistoryAgent struct {
	*switchFeedbackAgent
	budget time.Duration
}

func (a *deadlineSwitchHistoryAgent) GetSessionHistory(ctx context.Context, _ string, _ int) ([]HistoryEntry, error) {
	deadline, _ := ctx.Deadline()
	a.budget = time.Until(deadline)
	return nil, context.DeadlineExceeded
}

func TestSwitchPreview_SlowBackendHasShortDeadlineAndKeepsSelection(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &deadlineSwitchHistoryAgent{switchFeedbackAgent: newSwitchFeedbackAgent()}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	switchUserSend(e, p, "/switch old-id")
	if a.budget <= 0 || a.budget > time.Second {
		t.Fatalf("switch preview can delay confirmation for %s; want at most 1s", a.budget)
	}
	if e.sessions.GetOrCreateActive("test:alice").GetAgentSessionID() != "old-id" || !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgSwitchPreviewUnavailable)) {
		t.Fatal("preview timeout prevented switch confirmation or changed its target")
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
