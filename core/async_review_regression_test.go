package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type reviewSlowCloseSession struct {
	*asyncInputSession
	entered, release chan struct{}
	once             sync.Once
	closeErr         error
}

func (s *reviewSlowCloseSession) CurrentSessionID() string { return "newer-thread" }
func (s *reviewSlowCloseSession) Close() error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	if s.closeErr != nil {
		return s.closeErr
	}
	return s.asyncInputSession.Close()
}

func TestAsyncReview_FreeTextFindsUniqueInactiveQuestion(t *testing.T) {
	for _, text := range []string{"/answer SQLite", "askq:cTE:0:2"} {
		t.Run(text, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			next := &asyncInputSession{events: make(chan Event, 8)}
			e := NewEngine("test", &controllableAgent{nextSession: next}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			original := e.sessions.GetOrCreateActive("test:user")
			original.SetAgentSessionID("async-thread", "test")
			original.AsyncQuestion = savedTestQuestion()
			e.sessions.NewSession("test:user", "newer")
			e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: text, ReplyCtx: "ctx"})
			waitAsyncInput(t, func() bool { next.mu.Lock(); defer next.mu.Unlock(); return len(next.sent) == 1 })
			if e.sessions.ActiveSessionID("test:user") != original.ID {
				t.Fatal("wrong answer conversation")
			}
			next.events <- Event{Type: EventResult, Content: "answer applied", Done: true}
		})
	}
}

func TestAsyncReview_RestartNormalizesSending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("test:user")
	s.AsyncQuestion = savedTestQuestion()
	s.AsyncQuestion.Status = "sending"
	sm.Save()
	restored := NewSessionManager(path)
	if got := restored.FindByID(s.ID).asyncQuestion().Status; got != "unknown" {
		t.Fatalf("restored status=%q, want unknown", got)
	}
}

func TestAsyncReview_FreeTextDoesNotGuessBetweenConversations(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &stubStartSessionAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	first := e.sessions.GetOrCreateActive("test:user")
	first.AsyncQuestion = savedTestQuestion()
	second := e.sessions.NewSession("test:user", "second")
	second.AsyncQuestion = savedTestQuestion()
	second.AsyncQuestion.ID = "other"
	current := e.sessions.NewSession("test:user", "current")
	e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "/answer SQLite", ReplyCtx: "ctx"})
	if e.sessions.ActiveSessionID("test:user") != current.ID || !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgAnswerAmbiguous)) {
		t.Fatal("ambiguous answer silently routed", p.getSent())
	}
	if len(a.calls) != 0 {
		t.Fatal("ambiguous answer started model")
	}
}
func TestAsyncReview_FreeTextNeverCrossesUserNamespace(t *testing.T) {
	sm := NewSessionManager("")
	s := sm.GetOrCreateActive("other:user")
	s.AsyncQuestion = savedTestQuestion()
	sm.GetOrCreateActive("test:user")
	target, ambiguous := findFreeTextQuestionSession(sm, "test:user")
	if target != nil || ambiguous {
		t.Fatal("question leaked across users")
	}
}

func TestAsyncReview_FailedCloseNeverResumesAnswer(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &stubStartSessionAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	original := e.sessions.GetOrCreateActive("test:user")
	original.SetAgentSessionID("original", "stub")
	original.AsyncQuestion = savedTestQuestion()
	newer := e.sessions.NewSession("test:user", "newer")
	release := make(chan struct{})
	close(release)
	old := &reviewSlowCloseSession{asyncInputSession: &asyncInputSession{events: make(chan Event, 8)}, entered: make(chan struct{}), release: release, closeErr: errors.New("process still alive")}
	e.interactiveStates["test:user"] = &interactiveState{agentSession: old, platform: p, replyCtx: "ctx", busySession: newer}
	e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "askq:cTE:0:2", ReplyCtx: "ctx"})
	waitAsyncInput(t, func() bool { return original.asyncQuestion().Status == "open" && !original.Busy() })
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) != 0 || original.GetAgentSessionID() != "original" {
		t.Fatal("unsafe resume or blank fallback")
	}
}
func TestAsyncReview_RestartPersistsHonestDeliveryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("test:user")
	s.AsyncQuestion = savedTestQuestion()
	s.AsyncQuestion.Status = "sending"
	sm.Save()
	_ = NewSessionManager(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved sessionSnapshot
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Sessions[s.ID].AsyncQuestion.Status != "unknown" {
		t.Fatal("disk still claims a live submission after restart")
	}
}

type reviewQuestionOwnerPlatform struct {
	stubPlatformEngine
	owns func(string, string) bool
}

func (p *reviewQuestionOwnerPlatform) SetAsyncQuestionOwner(owns func(string, string) bool) {
	p.owns = owns
}
func TestAsyncReview_PlatformOwnerUsesPersistedQuestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("test:user")
	s.AsyncQuestion = savedTestQuestion()
	sm.NewSession("test:user", "newer")
	sm.Save()
	p := &reviewQuestionOwnerPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubStartSessionAgent{}, []Platform{p}, path, LangEnglish)
	e.onPlatformReady(p)
	if p.owns == nil || !p.owns("test:user", "cTE") || p.owns("other:user", "cTE") || p.owns("test:user", "missing") {
		t.Fatal("platform ownership is not bound to the persisted user/question")
	}
}

func TestAsyncReview_CancelledAnswerCannotAdoptReplacementState(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &stubStartSessionAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	session := e.sessions.GetOrCreateActive("test:user")
	session.SetAgentSessionID("original", "stub")
	admitted := &interactiveState{platform: p, replyCtx: "ctx"}
	admitted.markStopped()
	e.interactiveStates["test:user"] = &interactiveState{platform: p, replyCtx: "ctx"}
	state := e.getOrCreateInteractiveStateWith("test:user", p, "ctx", session, e.sessions, nil, "", &answerDelivery{state: admitted})
	if state.agentSession != nil || len(a.calls) != 0 {
		t.Fatal("canceled answer adopted a replacement execution state")
	}
}
