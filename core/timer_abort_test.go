package core

import (
	"context"
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

// In reuse mode a user message queued behind the timer turn sits on the chat's
// own interactive state; aborting the turn must tell that user their message
// was dropped instead of discarding it without a word.
func TestAbortTimerRun_ReuseModeNotifiesQueuedMessage(t *testing.T) {
	platform := &stubPlatformEngine{n: "test"}
	agentSession := newHungTimerSession()
	e := NewEngine("test", &resultAgent{session: agentSession}, []Platform{platform}, "", LangEnglish)
	defer e.cancel()

	e.interactiveMu.Lock()
	e.interactiveStates["chat-key"] = &interactiveState{
		agentSession:    agentSession,
		platform:        platform,
		replyCtx:        "ctx",
		pendingMessages: []queuedMessage{{platform: platform, replyCtx: "ctx", content: "queued question"}},
	}
	e.interactiveMu.Unlock()
	defer e.trackTimerRun("reuse1", "chat-key")()

	if !e.AbortTimerRun("reuse1") {
		t.Fatal("AbortTimerRun() = false for a registered turn")
	}
	sent := platform.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], errTimerRunAborted.Error()) {
		t.Fatalf("expected one dropped-message notice mentioning %q, got %v", errTimerRunAborted, sent)
	}
	for _, msg := range sent {
		if strings.Contains(msg, "session reset") {
			t.Fatalf("abort delivered %q to the chat", msg)
		}
	}
}

// slowStartAgent hands out its session only after delay, holding the engine's
// interactiveMu across the spawn like a real agent process start does.
type slowStartAgent struct {
	resultAgent
	delay time.Duration
}

func (a *slowStartAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	time.Sleep(a.delay)
	return a.resultAgent.StartSession(ctx, id)
}

// The abort cannot stop a turn before its agent has started; when the start
// takes longer than the poll window it must still stop the run afterwards.
func TestAbortTimerRun_StopsRunAfterSlowAgentStart(t *testing.T) {
	store, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agentSession := newHungTimerSession()
	agent := &slowStartAgent{resultAgent: resultAgent{session: agentSession}, delay: 4 * timerAbortRegisterGrace}
	e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	t.Cleanup(e.cancel)
	e.timerScheduler = NewTimerScheduler(store)

	job := &TimerJob{ID: "slow1", SessionKey: "discord:channel-1:user-1", Prompt: "never finishes", Mute: true, SessionMode: "new_per_run"}
	done := make(chan error, 1)
	go func() { done <- e.ExecuteTimerJob(job) }()

	waitTimerRunTracked(t, e, job.ID)
	if !e.AbortTimerRun(job.ID) {
		t.Fatal("AbortTimerRun() = false for a run whose agent started slowly")
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
}

// A failed abort must not forget the run, so a later abort can still stop it.
func TestAbortTimerRun_KeepsRunTrackedWhenNothingStopped(t *testing.T) {
	agentSession := newHungTimerSession()
	e := NewEngine("test", &resultAgent{session: agentSession}, nil, "", LangEnglish)
	defer e.cancel()
	defer e.trackTimerRun("never1", "never-key")()

	if e.AbortTimerRun("never1") {
		t.Fatal("AbortTimerRun() = true although no turn was registered")
	}
	if _, ok := e.timerRuns.Load("never1"); !ok {
		t.Fatal("run was forgotten after an abort that stopped nothing")
	}
}
