package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type asyncInputSession struct {
	mu      sync.Mutex
	events  chan Event
	sent    []string
	steered []string
	active  bool
	closed  bool
	err     error
}

type asyncInputPlatform struct {
	stubAskQuestionRichCardPlatform
}

func (*asyncInputPlatform) BuildRichCard(_ CardStatus, _ string, _ []ToolStep, markdown string, _ bool, _ string) string {
	return markdown
}

func (s *asyncInputSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, prompt)
	s.active = true
	return nil
}
func (s *asyncInputSession) SteerUserInput(prompt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if !s.active {
		return false, nil
	}
	s.steered = append(s.steered, prompt)
	return true, nil
}
func (*asyncInputSession) RespondPermission(string, PermissionResult) error {
	return errors.New("async answers must not use permission responses")
}
func (s *asyncInputSession) Events() <-chan Event   { return s.events }
func (*asyncInputSession) CurrentSessionID() string { return "async-thread" }
func (s *asyncInputSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}
func (s *asyncInputSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func waitAsyncInput(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async question journey did not reach expected state")
}

// User actions: start a task, answer its card while work continues, then tap
// the old card again. The duplicate must not become another agent message.
func TestCUJ_AsyncUserInput_CardAnswerWhileWorking(t *testing.T) {
	p := &asyncInputPlatform{}
	p.n = "feishu"
	s := &asyncInputSession{events: make(chan Event, 16)}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{CardMode: "rich", ToolMessages: false, ThinkingMessages: false})
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})
	key := "feishu:chat:user"
	send := func(content string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "feishu", UserID: "user", Content: content, ReplyCtx: "ctx"})
	}
	send("start task")
	waitAsyncInput(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) == 1 })
	s.events <- Event{Type: EventUserInputRequest, RequestID: "q1", Questions: testQuestions()}
	card := waitForSentCard(t, &p.stubCardPlatform)
	if countCardActionValues(card, "askq:cTE:0:") != 3 {
		t.Fatalf("question card lacks scoped option buttons: %#v", card)
	}
	// A final response can still arrive before the question is answered.
	s.events <- Event{Type: EventText, Content: "Independent work continues."}
	send("askq:cTE:0:2")
	waitAsyncInput(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.steered) == 1 })
	s.mu.Lock()
	answer := s.steered[0]
	s.mu.Unlock()
	if !strings.Contains(answer, "Which database?: SQLite") {
		t.Fatalf("steered answer = %q", answer)
	}
	send("askq:cTE:0:2")
	s.events <- Event{Type: EventText, Content: "\nTask completed."}
	s.events <- Event{Type: EventResult, Content: "Task completed.", Done: true}
	waitAsyncInput(t, func() bool {
		for _, msg := range p.getSent() {
			if strings.Contains(msg, "Task completed.") {
				return true
			}
		}
		return false
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steered) != 1 || len(s.sent) != 1 {
		t.Fatalf("duplicate card replayed input: sent=%v, steered=%v", s.sent, s.steered)
	}
}

func TestAsyncUserInput_LateFreeTextAnswerAndStaleCard(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	s := &asyncInputSession{events: make(chan Event, 16)}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{agentSession: s, platform: p}
	key := "plain:user"
	e.interactiveStates[key] = state
	e.showAsyncUserInput(state, p, "ctx", Event{RequestID: "old", Questions: testQuestions()})
	e.showAsyncUserInput(state, p, "ctx", Event{RequestID: "new", Questions: []UserQuestion{{Question: "Your preference?"}}})
	if _, handled := e.handleAsyncUserInput(p, &Message{SessionKey: key}, "askq:b2xk:0:1", key, e.sessions); !handled {
		t.Fatal("stale card was not dropped")
	}
	prompt, handled := e.handleAsyncUserInput(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, "custom answer", key, e.sessions)
	if handled || prompt != "Your preference?: custom answer" {
		t.Fatalf("idle answer = %q, %v; want normal message path", prompt, handled)
	}
}

func TestCUJ_AsyncUserInput_AnswerAfterTurnCompleted(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	s := &asyncInputSession{events: make(chan Event, 16)}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})
	key := "plain:user"
	send := func(content string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "plain", UserID: "user", Content: content, ReplyCtx: "ctx"})
	}
	send("start task")
	waitAsyncInput(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) == 1 })
	s.events <- Event{Type: EventUserInputRequest, RequestID: "late", Questions: testQuestions()}
	s.events <- Event{Type: EventResult, Content: "Independent work completed.", Done: true}
	waitAsyncInput(t, func() bool {
		for _, msg := range p.getSent() {
			if strings.Contains(msg, "Independent work completed.") {
				return true
			}
		}
		return false
	})
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
	send("2")
	waitAsyncInput(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) == 2 })
	s.mu.Lock()
	answer := s.sent[1]
	s.mu.Unlock()
	if answer != "Which database?: SQLite" {
		t.Fatalf("late answer = %q", answer)
	}
	send("askq:bGF0ZQ:0:2")
	s.events <- Event{Type: EventResult, Content: "Answer received.", Done: true}
	waitAsyncInput(t, func() bool {
		for _, msg := range p.getSent() {
			if strings.Contains(msg, "Answer received.") {
				return true
			}
		}
		return false
	})
}

func TestAsyncUserInput_MultipleQuestionsAndRetryAfterFailure(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	s := &asyncInputSession{events: make(chan Event, 16), active: true, err: errors.New("temporary failure")}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{agentSession: s, platform: p}
	key := "plain:user"
	e.interactiveStates[key] = state
	questions := append(testQuestions(), UserQuestion{Question: "Deployment?"})
	e.showAsyncUserInput(state, p, "ctx", Event{RequestID: "multi", Questions: questions})
	msg := &Message{SessionKey: key, ReplyCtx: "ctx"}
	e.handleAsyncUserInput(p, msg, "2", key, e.sessions)
	e.handleAsyncUserInput(p, msg, "local", key, e.sessions)
	if state.pendingUserInput == nil {
		t.Fatal("failed delivery discarded the question")
	}
	s.err = nil
	e.handleAsyncUserInput(p, msg, "local", key, e.sessions)
	if len(s.steered) != 1 || s.steered[0] != "Which database?: SQLite\nDeployment?: local" {
		t.Fatalf("answers = %v", s.steered)
	}
}
