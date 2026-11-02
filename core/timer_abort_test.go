package core

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hungTimerSession never emits a result; Close is safe to race with the
// turn's Alive checks.
type hungTimerSession struct {
	events    chan Event
	closed    chan struct{}
	closeOnce sync.Once
	dead      atomic.Bool
}

func newHungTimerSession() *hungTimerSession {
	return &hungTimerSession{events: make(chan Event), closed: make(chan struct{})}
}

func (s *hungTimerSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	return nil
}
func (s *hungTimerSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *hungTimerSession) Events() <-chan Event                             { return s.events }
func (s *hungTimerSession) CurrentSessionID() string                         { return "hung" }
func (s *hungTimerSession) Alive() bool                                      { return !s.dead.Load() }
func (s *hungTimerSession) Close() error {
	s.closeOnce.Do(func() {
		s.dead.Store(true)
		close(s.closed)
	})
	return nil
}

// waitTimerRunTracked blocks until the timer run of jobID is tracked, so the
// test aborts a run that is actually in flight.
func waitTimerRunTracked(t *testing.T, e *Engine, jobID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := e.timerRuns.Load(jobID); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timer run was never tracked")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runHungTimerJob(t *testing.T, sessionMode string) (*stubCronReplyTargetPlatform, *hungTimerSession) {
	t.Helper()
	store, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agentSession := newHungTimerSession()
	e := NewEngine("test", &resultAgent{session: agentSession}, []Platform{platform}, "", LangEnglish)
	t.Cleanup(e.cancel)
	e.timerScheduler = NewTimerScheduler(store)

	job := &TimerJob{
		ID:          "hung1",
		SessionKey:  "discord:channel-1:user-1",
		Prompt:      "never finishes",
		Mute:        true,
		SessionMode: sessionMode,
	}

	done := make(chan error, 1)
	go func() { done <- e.ExecuteTimerJob(job) }()

	waitTimerRunTracked(t, e, job.ID)
	if !e.AbortTimerRun(job.ID) {
		t.Fatal("AbortTimerRun() = false for a running timer job")
	}

	select {
	case <-agentSession.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent session was not closed after abort")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteTimerJob did not return after abort")
	}
	return platform, agentSession
}

// A timer run whose agent never reports a result must not leave the agent
// process behind once the scheduler gives up on it (e.g. after an upstream
// 502 the turn never completed and the CLI lingered after "job timed out").
func TestAbortTimerRun_ClosesHungSession(t *testing.T) {
	runHungTimerJob(t, "new_per_run")
}

// In reuse mode the run borrows the chat's own interactive key; aborting it
// must stop the turn without reporting a session reset into that chat.
func TestAbortTimerRun_ReuseModeStopsSilently(t *testing.T) {
	platform, _ := runHungTimerJob(t, "")
	platform.mu.Lock()
	defer platform.mu.Unlock()
	for _, msg := range platform.sent {
		if strings.Contains(msg, "session reset") {
			t.Fatalf("abort delivered %q to the chat", msg)
		}
	}
}

// The run is tracked before its turn registers interactive state; an abort
// landing in between must still stop the turn once it registers.
func TestAbortTimerRun_WaitsForStateRegistration(t *testing.T) {
	agentSession := newHungTimerSession()
	e := NewEngine("test", &resultAgent{session: agentSession}, nil, "", LangEnglish)
	defer e.cancel()

	defer e.trackTimerRun("late1", "late-key")()
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.interactiveMu.Lock()
		e.interactiveStates["late-key"] = &interactiveState{agentSession: agentSession}
		e.interactiveMu.Unlock()
	}()

	if !e.AbortTimerRun("late1") {
		t.Fatal("AbortTimerRun() = false for a run whose state registered late")
	}
	select {
	case <-agentSession.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent session was not closed after abort")
	}
}

func TestAbortTimerRun_UnknownRun(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newControllableSession("x")}, nil, "", LangEnglish)
	defer e.cancel()
	if e.AbortTimerRun("missing") {
		t.Fatal("AbortTimerRun() = true for a run that was never started")
	}
}
