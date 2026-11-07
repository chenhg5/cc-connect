package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAsyncAnswer_RecognizedBeforeUnknownCommandFallback(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &stubStartSessionAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangChinese)
	s := e.sessions.GetOrCreateActive("test:user")
	s.AsyncQuestion = savedTestQuestion()
	s.AsyncQuestion.Status = "submitted"
	for _, text := range []string{"/answer 方形", "/answer"} {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: text, ReplyCtx: "ctx"})
	}
	if len(a.calls) != 0 || strings.Contains(strings.Join(p.getSent(), "\n"), fmt.Sprintf(e.i18n.T(MsgUnknownCommand), "/answer")) {
		t.Fatal("answer falsely reported as unknown/forwarded", p.getSent())
	}
}

func savedTestQuestion() *AsyncQuestion {
	return &AsyncQuestion{ID: "cTE", Questions: testQuestions(), Answers: make([]string, len(testQuestions())), Status: "open"}
}

func TestAsyncQuestion_PersistsAcrossRestartAndBindsOriginalSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(path)
	original := sm.GetOrCreateActive("test:user")
	original.AsyncQuestion = savedTestQuestion()
	original.SetAgentSessionID("original-thread", "test")
	newer := sm.NewSession("test:user", "newer")
	sm.Save()
	restored := NewSessionManager(path)
	q := restored.FindByID(original.ID).asyncQuestion()
	if q == nil || q.ID != "cTE" || q.Status != "open" {
		t.Fatal(q)
	}
	q.Questions[0].Options[0].Label = "changed clone"
	if restored.FindByID(original.ID).asyncQuestion().Questions[0].Options[0].Label == "changed clone" {
		t.Fatal("shallow question snapshot")
	}
	target := findAsyncQuestionSession(restored, "test:user", "askq:cTE:0:2")
	if target == nil || target.ID != original.ID {
		t.Fatal("answer lost original conversation")
	}
	_ = newer
	if findAsyncQuestionSession(restored, "other:user", "askq:cTE:0:2") != nil {
		t.Fatal("cross-user question lookup")
	}
}

type delayedAnswerSession struct {
	*asyncInputSession
	entered chan struct{}
	release chan struct{}
	sendErr error
}

func (s *delayedAnswerSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	close(s.entered)
	<-s.release
	if s.sendErr != nil {
		return s.sendErr
	}
	return s.asyncInputSession.Send(prompt, id, images, files)
}

func TestAsyncAnswer_CardSuccessWaitsForDeliveryAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
		key    MsgKey
	}{
		{"accepted", nil, "submitted", MsgAnswerSubmitted},
		{"rejected", ErrSteerRejected, "open", MsgPsSendFailed},
		{"unknown", errors.New("lost acknowledgement"), "unknown", MsgMessageSteerUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			s := &delayedAnswerSession{asyncInputSession: &asyncInputSession{events: make(chan Event, 8)}, entered: make(chan struct{}), release: make(chan struct{}), sendErr: tc.err}
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			session := e.sessions.GetOrCreateActive("test:user")
			session.AsyncQuestion = savedTestQuestion()
			session.SetAgentSessionID("async-thread", "test")
			var mu sync.Mutex
			var feedback []string
			callback := func(card *Card) { mu.Lock(); feedback = append(feedback, card.RenderText()); mu.Unlock() }
			e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "askq:cTE:0:2", ReplyCtx: "ctx", OnAnswerStatus: callback})
			<-s.entered
			mu.Lock()
			before := strings.Join(feedback, "\n")
			mu.Unlock()
			if strings.Contains(before, e.i18n.T(MsgAnswerSubmitted)) || session.asyncQuestion().Status != "sending" {
				t.Fatal("success before RPC acknowledgement", before)
			}
			close(s.release)
			waitAsyncInput(t, func() bool { return session.asyncQuestion().Status == tc.status })
			waitAsyncInput(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return strings.Contains(strings.Join(feedback, "\n"), e.i18n.T(tc.key))
			})
			if tc.err == nil {
				e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "askq:cTE:0:2", ReplyCtx: "ctx", OnAnswerStatus: callback})
				s.mu.Lock()
				n := len(s.sent)
				s.mu.Unlock()
				if n != 1 {
					t.Fatal("duplicate answer sent", n)
				}
				s.events <- Event{Type: EventResult, Content: "Answer applied", Done: true}
			}
		})
	}
}

func TestAsyncQuestion_RestartNeverReplaysUncertainAnswer(t *testing.T) {
	for _, status := range []string{"sending", "unknown", "submitted"} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			sm := NewSessionManager(path)
			session := sm.GetOrCreateActive("test:user")
			session.AsyncQuestion = savedTestQuestion()
			session.AsyncQuestion.Status = status
			session.AsyncQuestion.Answers[0] = "SQLite"
			sm.Save()
			p := &stubPlatformEngine{n: "test"}
			a := &stubStartSessionAgent{}
			e := NewEngine("test", a, []Platform{p}, path, LangEnglish)
			e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "askq:cTE:0:2", ReplyCtx: "ctx"})
			if len(a.calls) != 0 {
				t.Fatal("restart replayed answer")
			}
		})
	}
}

type questionResumeAgent struct {
	stubStartSessionAgent
	valid bool
}

func (a *questionResumeAgent) ValidateSessionID(context.Context, string) bool { return a.valid }

func TestAsyncAnswer_ResumeFailureNeverFallsBackToBlankThread(t *testing.T) {
	for _, kind := range []string{"resume-error", "invalid", "unsafe"} {
		t.Run(kind, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			a := &questionResumeAgent{valid: kind != "invalid", stubStartSessionAgent: stubStartSessionAgent{failIDs: map[string]error{"original": errors.New("cannot resume")}}}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			session := e.sessions.GetOrCreateActive("test:user")
			session.SetAgentSessionID("original", "stub")
			session.AsyncQuestion = savedTestQuestion()
			if kind == "unsafe" {
				e.markUnsafeResume("test:user")
			}
			e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", Content: "askq:cTE:0:2", ReplyCtx: "ctx"})
			waitAsyncInput(t, func() bool { return session.asyncQuestion().Status == "open" && !session.Busy() })
			if session.GetAgentSessionID() != "original" {
				t.Fatal("answer rebound to blank thread")
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			for _, id := range a.calls {
				if id == "" {
					t.Fatal("blank fallback")
				}
			}
			if kind != "resume-error" && len(a.calls) != 0 {
				t.Fatal("unsafe or invalid resume attempted")
			}
		})
	}
}
