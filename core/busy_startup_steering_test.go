package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type startupSteeringSession struct {
	*receiptAgentSession
	mu     sync.Mutex
	steers []string
	active bool
}

func (s *startupSteeringSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	_ = s.queuingAgentSession.Send(prompt, id, images, files)
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	s.events <- Event{Type: EventText, Content: "working"}
	return nil
}
func (s *startupSteeringSession) SteerUserInput(prompt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return false, nil
	}
	s.steers = append(s.steers, prompt)
	return true, nil
}

type startupSteeringAgent struct {
	stubAgent
	mu       sync.Mutex
	sessions []*startupSteeringSession
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (a *startupSteeringAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	if a.entered != nil {
		a.once.Do(func() { close(a.entered) })
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &startupSteeringSession{receiptAgentSession: &receiptAgentSession{queuingAgentSession: newQueuingSession(fmt.Sprintf("shared-%d", len(a.sessions)))}}
	a.sessions = append(a.sessions, s)
	return s, nil
}
func (a *startupSteeringAgent) count() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.sessions) }
func TestBusySteering_ConcurrentStartupSteersBothDistinctInputs(t *testing.T) {
	native := &stubPlatformEngine{n: "native"}
	web := &stubPlatformEngine{n: "web"}
	a := &startupSteeringAgent{entered: make(chan struct{}), release: make(chan struct{})}
	e := NewEngine("shared", a, []Platform{native, web}, "", LangEnglish)
	e.SetBusyMessageMode("steer")
	t.Cleanup(func() { _ = e.Stop() })
	key := "native:room:user"
	session := e.sessions.GetOrCreateActive(key)
	e.ReceiveMessage(native, &Message{SessionKey: key, Platform: "native", MessageID: "native-1", Content: "same content", ReplyCtx: "native"})
	<-a.entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ReceiveMessage(web, &Message{SessionKey: key, Platform: "web", MessageID: "web-1", Content: "same content", ReplyCtx: "web"})
	}()
	close(a.release)
	<-done
	waitAsyncInput(t, func() bool {
		if a.count() != 1 {
			return false
		}
		a.mu.Lock()
		s := a.sessions[0]
		a.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.steers) == 1
	})
	a.mu.Lock()
	s := a.sessions[0]
	a.mu.Unlock()
	s.sendMu.Lock()
	sends := len(s.sendCalls)
	s.sendMu.Unlock()
	if sends != 1 {
		t.Fatal("double start", sends)
	}
	waitAsyncInput(t, func() bool { return len(session.GetHistory(0)) >= 2 })
	h := session.GetHistory(0)
	if h[0].Content != "same content" || h[1].Content != "same content" {
		t.Fatal("equal inputs deduplicated", h)
	}
	s.events <- Event{Type: EventResult, Content: "completed", Done: true}
	waitAsyncInput(t, func() bool { return !session.Busy() })
}
func TestBusySteering_DifferentConversationsRunIndependently(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &startupSteeringAgent{}
	e := NewEngine("shared", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	for _, key := range []string{"test:first", "test:second"} {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", Content: "same content", ReplyCtx: key})
	}
	waitAsyncInput(t, func() bool { return a.count() == 2 })
	first := e.sessions.GetOrCreateActive("test:first")
	second := e.sessions.GetOrCreateActive("test:second")
	if first.ID == second.ID || !first.Busy() || !second.Busy() {
		t.Fatal("distinct conversations interfered")
	}
	a.mu.Lock()
	running := append([]*startupSteeringSession(nil), a.sessions...)
	a.mu.Unlock()
	for _, s := range running {
		s.events <- Event{Type: EventResult, Content: "done", Done: true}
	}
	waitAsyncInput(t, func() bool { return !first.Busy() && !second.Busy() })
}

type recallDuringSteer struct {
	*startupSteeringSession
	callback func()
}

func (s *recallDuringSteer) SteerUserInput(text string) (bool, error) { s.callback(); return true, nil }
func TestBusySteering_RecallDuringQueuedSteerDoesNotRemoveNextAttachment(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetBusyMessageMode("steer")
	session := e.sessions.GetOrCreateActive("test:user")
	state := &interactiveState{platform: p, pendingMessages: []queuedMessage{{queueID: 1, allowSteer: true, content: "first", platform: p}, {queueID: 2, allowSteer: true, content: "file", platform: p, files: []FileAttachment{{FileName: "data.xlsx"}}}}}
	state.agentSession = &recallDuringSteer{callback: func() { state.mu.Lock(); state.pendingMessages = state.pendingMessages[1:]; state.mu.Unlock() }}
	e.steerQueuedInputs(state, session, e.sessions, "test:user")
	if len(state.pendingMessages) != 1 || state.pendingMessages[0].queueID != 2 {
		t.Fatal("next attachment disappeared")
	}
}

func TestBusySteering_UnconfirmedQueuedSteerIsNotReplayed(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetBusyMessageMode("steer")
	session := e.sessions.GetOrCreateActive("test:user")
	sender := &busySteeringSession{queuingAgentSession: newQueuingSession("thread")}
	sender.err = errors.New("acknowledgement timed out")
	state := &interactiveState{agentSession: sender, platform: p, pendingMessages: []queuedMessage{{queueID: 1, allowSteer: true, content: "add totals", platform: p}}}
	e.steerQueuedInputs(state, session, e.sessions, "test:user")
	e.steerQueuedInputs(state, session, e.sessions, "test:user")
	if len(sender.steered) != 1 || len(state.pendingMessages) != 0 || len(session.GetHistory(0)) != 2 {
		t.Fatal("uncertain delivery replayed or lost")
	}
	if len(p.getSent()) != 1 || p.getSent()[0] != e.i18n.T(MsgMessageSteerUnconfirmed) {
		t.Fatal(p.getSent())
	}
	_ = sender.Close()
}

func TestBusySteering_ExplicitRejectionIsNotRetriedOnLaterEvents(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetBusyMessageMode("steer")
	session := e.sessions.GetOrCreateActive("test:user")
	sender := &busySteeringSession{queuingAgentSession: newQueuingSession("thread"), err: ErrSteerRejected}
	state := &interactiveState{agentSession: sender, platform: p, pendingMessages: []queuedMessage{{queueID: 1, allowSteer: true, content: "supplement", platform: p}}}
	e.steerQueuedInputs(state, session, e.sessions, "test:user")
	sender.err = nil
	e.steerQueuedInputs(state, session, e.sessions, "test:user")
	if len(sender.steered) != 1 || len(state.pendingMessages) != 1 || state.pendingMessages[0].allowSteer {
		t.Fatal("explicit rejection retried")
	}
	_ = sender.Close()
}
