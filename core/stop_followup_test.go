package core

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stopTestSession stays silent on the first turn until it is interrupted, so a
// follow-up can be sent while the stop is still unwinding.
type stopTestSession struct {
	mu          sync.Mutex
	prompts     []string
	events      chan Event
	interruptCh chan struct{}
	once        sync.Once
	alive       atomic.Bool
}

func newStopTestSession() *stopTestSession {
	s := &stopTestSession{events: make(chan Event, 8), interruptCh: make(chan struct{})}
	s.alive.Store(true)
	return s
}

func (s *stopTestSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	if strings.Contains(prompt, "LONG") {
		go func() {
			<-s.interruptCh
			s.events <- Event{Type: EventResult, Content: "", Done: true}
		}()
		return nil
	}
	s.events <- Event{Type: EventResult, Content: "pong", Done: true}
	return nil
}

func (s *stopTestSession) Interrupt(string) error {
	s.once.Do(func() { close(s.interruptCh) })
	return nil
}

func (s *stopTestSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *stopTestSession) Events() <-chan Event                            { return s.events }
func (s *stopTestSession) CurrentSessionID() string                        { return "stop-test" }
func (s *stopTestSession) Alive() bool                                     { return s.alive.Load() }
func (s *stopTestSession) Close() error                                    { s.alive.Store(false); return nil }

func (s *stopTestSession) promptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

func TestFollowUpAfterStopIsProcessed(t *testing.T) {
	sess := newStopTestSession()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangChinese)
	t.Cleanup(e.cancel)

	first := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "LONG task", ReplyCtx: "ctx"}
	e.handleMessage(p, first)
	time.Sleep(150 * time.Millisecond)

	iKey := e.interactiveKeyForSessionKey(first.SessionKey)
	if !e.interruptInteractiveTurn(iKey, "test stop", true, true) {
		t.Fatal("interruptInteractiveTurn() = false; the turn was not interruptible")
	}
	time.Sleep(250 * time.Millisecond)

	second := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "follow up", ReplyCtx: "ctx"}
	e.handleMessage(p, second)
	time.Sleep(250 * time.Millisecond)

	if got := sess.promptCount(); got != 2 {
		t.Fatalf("session got %d prompts, want 2 (the follow-up must reach the agent)", got)
	}
}

// The user often types the follow-up while the stop is still unwinding. That
// message lands in the queue and must still be processed.
func TestFollowUpSentDuringStopUnwindIsProcessed(t *testing.T) {
	sess := newStopTestSession()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangChinese)
	t.Cleanup(e.cancel)

	first := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "LONG task", ReplyCtx: "ctx"}
	e.handleMessage(p, first)
	time.Sleep(150 * time.Millisecond)

	iKey := e.interactiveKeyForSessionKey(first.SessionKey)
	if !e.interruptInteractiveTurn(iKey, "test stop", true, true) {
		t.Fatal("interruptInteractiveTurn() = false")
	}

	// Immediately follow up: the turn is still unwinding, so this cannot take
	// the normal path.
	second := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "follow up", ReplyCtx: "ctx"}
	e.handleMessage(p, second)

	time.Sleep(500 * time.Millisecond)

	if got := sess.promptCount(); got != 2 {
		t.Fatalf("session got %d prompts, want 2 (follow-up lost during stop unwind)", got)
	}
}

// If the CLI never honours the interrupt, the watchdog must recover the session
// and still answer the follow-up that was typed while stopping.
func TestInterruptWatchdogRedispatchesFollowUp(t *testing.T) {
	old := interruptWatchdogTimeout
	interruptWatchdogTimeout = 200 * time.Millisecond
	t.Cleanup(func() { interruptWatchdogTimeout = old })

	sess := newStopTestSession()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangChinese)
	t.Cleanup(e.cancel)

	first := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "LONG task", ReplyCtx: "ctx"}
	e.handleMessage(p, first)
	time.Sleep(120 * time.Millisecond)

	iKey := e.interactiveKeyForSessionKey(first.SessionKey)
	if !e.interruptInteractiveTurn(iKey, "test stop", true, true) {
		t.Fatal("interruptInteractiveTurn() = false")
	}

	second := &Message{SessionKey: "test:chat:user", Platform: "test", UserID: "u", Content: "follow up", ReplyCtx: "ctx"}
	e.handleMessage(p, second)

	time.Sleep(900 * time.Millisecond)

	if got := sess.promptCount(); got < 2 {
		t.Fatalf("session got %d prompts, want the follow-up to be re-dispatched", got)
	}
}
