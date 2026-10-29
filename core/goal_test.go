package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type goalTestAgent struct {
	stubAgent
	session *goalTestSession
	starts  []string
}

func TestGoalCommandValidation(t *testing.T) {
	for _, text := range []string{"set", "pause extra", "resume extra", "clear extra", "budget", "budget 0", "budget -1", "budget 1.5", "budget 9223372036854775808", "set Task --budget", "set Task --budget 4 --budget 5", "set " + strings.Repeat("字", 4001)} {
		if _, err := parseGoalCommand(strings.Fields(text)); err == nil {
			t.Errorf("accepted invalid command %q", text)
		}
	}
	for _, text := range []string{"", "show", "pause", "resume", "clear", "budget none", "budget 1", "Finish the task", "set Fix tests --budget 40000", "edit New objective"} {
		if _, err := parseGoalCommand(strings.Fields(text)); err != nil {
			t.Errorf("rejected %q: %v", text, err)
		}
	}
}

type goalResumeFailAgent struct {
	stubAgent
	starts int
}

func (*goalResumeFailAgent) SupportsGoals() bool { return true }
func (a *goalResumeFailAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.starts++
	return nil, errors.New("thread unavailable")
}

func TestGoalResumeFailurePreservesThread(t *testing.T) {
	env := newCUJEnv(t)
	a := &goalResumeFailAgent{}
	env.engine.agent = a
	session := env.engine.sessions.GetOrCreateActive("test:goal-user")
	session.SetAgentSessionID("saved-goal-thread", a.Name())
	// The command is invoked synchronously here so the failure is fully observed.
	env.engine.cmdGoal(env.plat, &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}, nil)
	if a.starts != 1 || session.GetAgentSessionID() != "saved-goal-thread" {
		t.Fatalf("failed resume starts=%d saved=%q", a.starts, session.GetAgentSessionID())
	}
	if !strings.Contains(strings.Join(env.plat.getSent(), "\n"), env.engine.i18n.T(MsgFailedToStartAgentSession)) {
		t.Fatal("resume failure was hidden")
	}
}

func TestGoalNativeStatusDeliveryAndIdleProtection(t *testing.T) {
	env := newCUJEnv(t)
	s := &goalTestSession{cujAgentSession: newCUJAgentSession(), goal: &Goal{Status: "active", Objective: "Keep working"}}
	env.engine.agent = &goalTestAgent{session: s}
	env.engine.agentSessionIdleTimeoutNanos.Store(int64(10 * time.Millisecond))
	env.engine.cmdGoal(env.plat, &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}, nil)
	t.Cleanup(func() { env.engine.cleanupInteractiveState("test:goal-user") })
	time.Sleep(30 * time.Millisecond)
	if !s.Alive() {
		t.Fatal("idle timer closed an active goal")
	}
	s.events <- Event{Type: EventGoal, Goal: &Goal{Status: "budgetLimited", Objective: "Keep working", TokensUsed: 500}}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(env.plat.getSent(), "\n"), "budgetLimited") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("native status not delivered: %v", env.plat.getSent())
}

func (*goalTestAgent) SupportsGoals() bool { return true }
func (a *goalTestAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.starts = append(a.starts, id)
	return a.session, nil
}

func (s *goalTestSession) CancelTurn() error {
	if s.cancelErr != nil {
		return s.cancelErr
	}
	_, err := s.SetGoal(GoalUpdate{Status: "paused"})
	return err
}

type goalRecoveryPlatform struct{ stubPlatformEngine }

func (*goalRecoveryPlatform) ReconstructReplyCtx(key string) (any, error) { return key, nil }

func TestGoalRestartRecoveryAndStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	p := &goalRecoveryPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	s := &goalTestSession{cujAgentSession: newCUJAgentSession()}
	a := &goalTestAgent{session: s}
	e := NewEngine("goal", a, []Platform{p}, path, LangEnglish)
	msg := &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}
	e.cmdGoal(p, msg, []string{"set", "Persisted objective"})
	if !e.sessions.GetOrCreateActive(msg.SessionKey).GetResumeGoal() {
		t.Fatal("active continuation intent not saved")
	}
	e.cleanupInteractiveState(msg.SessionKey)
	s2 := &goalTestSession{cujAgentSession: newCUJAgentSession(), goal: &Goal{Status: "active", Objective: "Persisted objective"}}
	a2 := &goalTestAgent{session: s2}
	e2 := NewEngine("goal", a2, []Platform{p}, path, LangEnglish)
	t.Cleanup(func() { e2.cleanupInteractiveState(msg.SessionKey) })
	e2.restoreNativeGoals(p)
	if len(a2.starts) != 1 || a2.starts[0] != "cuj-agent-session" {
		t.Fatalf("recovery started %v, want saved thread", a2.starts)
	}
	e2.cmdStop(p, msg)
	g, _ := s2.GetGoal()
	if g.Status != "paused" || e2.sessions.GetOrCreateActive(msg.SessionKey).GetResumeGoal() {
		t.Fatal("stop did not revoke continuation intent")
	}
	s3 := &goalTestSession{cujAgentSession: newCUJAgentSession()}
	a3 := &goalTestAgent{session: s3}
	e3 := NewEngine("goal", a3, []Platform{p}, path, LangEnglish)
	e3.restoreNativeGoals(p)
	if len(a3.starts) != 0 {
		t.Fatal("stopped goal restarted automatically")
	}
}

type goalTestSession struct {
	*cujAgentSession
	goalMu    sync.Mutex
	goal      *Goal
	cancelErr error
}

func (s *goalTestSession) GetGoal() (*Goal, error) {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	if s.goal == nil {
		return nil, nil
	}
	g := *s.goal
	return &g, nil
}
func (s *goalTestSession) SetGoal(u GoalUpdate) (*Goal, error) {
	s.goalMu.Lock()
	if s.goal == nil {
		s.goal = &Goal{ThreadID: "goal-thread", Status: "active"}
	}
	if u.Objective != nil {
		s.goal.Objective = *u.Objective
	}
	if u.Status != "" {
		s.goal.Status = u.Status
	}
	if u.TokenBudget != nil {
		s.goal.TokenBudget = u.TokenBudget
	}
	if u.ClearTokenBudget {
		s.goal.TokenBudget = nil
	}
	s.goalMu.Unlock()
	return s.GetGoal()
}
func (s *goalTestSession) ClearGoal() error {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	s.goal = nil
	return nil
}
func (s *goalTestSession) GoalActive() bool {
	g, _ := s.GetGoal()
	return g != nil && g.Status == "active"
}

func TestCUJ_C_GoalLifecycle(t *testing.T) {
	env := newCUJEnv(t)
	s := &goalTestSession{cujAgentSession: newCUJAgentSession()}
	env.engine.agent = &goalTestAgent{session: s}
	t.Cleanup(func() { env.engine.cleanupInteractiveState("test:goal-user") })
	for _, step := range []struct{ command, visible string }{
		{"/goal set Finish migration --budget 40000", "Finish migration"},
		{"/goal pause", "paused"},
		{"/goal edit Finish migration and tests", "Finish migration and tests"},
		{"/goal budget 50000", "50000"},
		{"/goal resume", "active"},
		{"/goal", "Finish migration and tests"},
		{"/goal clear", "cleared"},
	} {
		before := len(env.plat.getSent())
		env.userSends("goal-user", step.command)
		deadline := time.Now().Add(2 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			for _, x := range env.plat.getSent()[before:] {
				if strings.Contains(x, step.visible) {
					found = true
				}
			}
			if found {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !found {
			t.Fatalf("%s: missing %q in %v", step.command, step.visible, env.plat.getSent()[before:])
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sentPrompts) != 0 {
		t.Fatal("goal command was sent as model prompt")
	}
}

func TestGoalStopRPCFailureClosesSession(t *testing.T) {
	env := newCUJEnv(t)
	s := &goalTestSession{cujAgentSession: newCUJAgentSession(), cancelErr: errors.New("goal RPC unavailable")}
	env.engine.agent = &goalTestAgent{session: s}
	msg := &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}
	env.engine.cmdGoal(env.plat, msg, []string{"set", "Work"})
	env.engine.cmdStop(env.plat, msg)
	if env.engine.sessions.GetOrCreateActive(msg.SessionKey).GetResumeGoal() {
		t.Fatal("failed stop retained automatic recovery")
	}
	if !strings.Contains(strings.Join(env.plat.getSent(), "\n"), "goal RPC unavailable") {
		t.Fatal("RPC failure was not reported")
	}
	env.engine.interactiveMu.Lock()
	_, stillPresent := env.engine.interactiveStates[msg.SessionKey]
	env.engine.interactiveMu.Unlock()
	if stillPresent {
		t.Fatal("failed cancellation left live state registered")
	}
}

type blockingGoalSession struct {
	*goalTestSession
	getEntered, getRelease chan struct{}
	setEntered, setRelease chan struct{}
	getErr                 error
}

func (s *blockingGoalSession) GetGoal() (*Goal, error) {
	if s.getEntered != nil {
		close(s.getEntered)
		<-s.getRelease
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.goalTestSession.GetGoal()
}

func (s *blockingGoalSession) SetGoal(u GoalUpdate) (*Goal, error) {
	if s.setEntered != nil {
		close(s.setEntered)
		<-s.setRelease
	}
	return s.goalTestSession.SetGoal(u)
}

type concurrentGoalAgent struct {
	stubAgent
	session AgentSession
}

func (*concurrentGoalAgent) SupportsGoals() bool { return true }
func (a *concurrentGoalAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

func TestGoalQueryDrainsMessagesQueuedDuringRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"success", nil}, {"error", errors.New("query failed")}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newCUJEnv(t)
			base := &goalTestSession{cujAgentSession: newCUJAgentSession()}
			base.reply = "follow-up delivered"
			s := &blockingGoalSession{goalTestSession: base, getEntered: make(chan struct{}), getRelease: make(chan struct{}), getErr: tc.err}
			env.engine.agent = &concurrentGoalAgent{session: s}
			var release sync.Once
			t.Cleanup(func() {
				release.Do(func() { close(s.getRelease) })
				env.engine.cleanupInteractiveState("test:goal-user")
			})
			done := make(chan struct{})
			go func() {
				env.engine.cmdGoal(env.plat, &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}, nil)
				close(done)
			}()
			select {
			case <-s.getEntered:
			case <-time.After(time.Second):
				t.Fatal("goal query did not reach native RPC")
			}
			env.userSends("goal-user", "follow up")
			release.Do(func() { close(s.getRelease) })
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("goal command did not finish")
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if strings.Contains(strings.Join(env.plat.getSent(), "\n"), "follow-up delivered") {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("follow-up stayed queued after the Goal RPC finished")
		})
	}
}

func TestGoalStopWaitsForPendingMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		goal *Goal
	}{{"set", []string{"set", "Pending objective"}, nil}, {"resume", []string{"resume"}, &Goal{Objective: "Paused objective", Status: "paused"}}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newCUJEnv(t)
			base := &goalTestSession{cujAgentSession: newCUJAgentSession(), goal: tc.goal}
			s := &blockingGoalSession{goalTestSession: base, setEntered: make(chan struct{}), setRelease: make(chan struct{})}
			env.engine.agent = &concurrentGoalAgent{session: s}
			var release sync.Once
			t.Cleanup(func() {
				release.Do(func() { close(s.setRelease) })
				env.engine.cleanupInteractiveState("test:goal-user")
			})
			msg := &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}
			goalDone := make(chan struct{})
			go func() { env.engine.cmdGoal(env.plat, msg, tc.args); close(goalDone) }()
			select {
			case <-s.setEntered:
			case <-time.After(time.Second):
				t.Fatal("goal mutation did not reach native RPC")
			}
			stopDone := make(chan struct{})
			go func() { env.engine.cmdStop(env.plat, msg); close(stopDone) }()
			select {
			case <-stopDone:
			case <-time.After(200 * time.Millisecond):
			}
			release.Do(func() { close(s.setRelease) })
			for _, done := range []chan struct{}{goalDone, stopDone} {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("Goal/stop did not finish")
				}
			}
			g, _ := base.GetGoal()
			if g == nil || g.Status == "active" || env.engine.sessions.GetOrCreateActive(msg.SessionKey).GetResumeGoal() {
				t.Fatalf("pending Goal restarted after stop: goal=%+v", g)
			}
		})
	}
}

func TestGoalStopInvalidatesWaitingMutation(t *testing.T) {
	env := newCUJEnv(t)
	s := &goalTestSession{cujAgentSession: newCUJAgentSession()}
	env.engine.agent = &concurrentGoalAgent{session: s}
	msg := &Message{SessionKey: "test:goal-user", ReplyCtx: "ctx"}
	env.engine.cmdGoal(env.plat, msg, nil)
	t.Cleanup(func() { env.engine.cleanupInteractiveState(msg.SessionKey) })
	env.engine.interactiveMu.Lock()
	state := env.engine.interactiveStates[msg.SessionKey]
	env.engine.interactiveMu.Unlock()
	state.goalMu.Lock()
	var release sync.Once
	t.Cleanup(func() { release.Do(state.goalMu.Unlock) })
	goalDone := make(chan struct{})
	go func() { env.engine.cmdGoal(env.plat, msg, []string{"set", "Must not start"}); close(goalDone) }()
	session := env.engine.sessions.GetOrCreateActive(msg.SessionKey)
	env.waitFor("waiting Goal owns session", time.Second, session.Busy)
	stopDone := make(chan struct{})
	go func() { env.engine.cmdStop(env.plat, msg); close(stopDone) }()
	env.waitFor("stop invalidates pending Goal", time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.goalStopGeneration != 0
	})
	release.Do(state.goalMu.Unlock)
	for _, done := range []chan struct{}{goalDone, stopDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waiting Goal/stop did not finish")
		}
	}
	g, _ := s.GetGoal()
	if (g != nil && g.Objective == "Must not start") || session.GetResumeGoal() {
		t.Fatalf("cancelled waiting mutation ran: %+v", g)
	}
}

func TestGoalIdleCloseDoesNotInterruptForeground(t *testing.T) {
	for _, name := range []string{"late_no_queue", "late_after_queue", "already_firing"} {
		t.Run(name, func(t *testing.T) {
			env := newCUJEnv(t)
			s := &goalTestSession{cujAgentSession: newCUJAgentSession()}
			env.engine.agentSessionIdleTimeoutNanos.Store(int64(time.Hour))
			key := "test:goal-user"
			t.Cleanup(func() { env.engine.cleanupInteractiveState(key) })
			msg := &Message{SessionKey: key, ReplyCtx: "ctx"}
			if name == "already_firing" {
				env.engine.agent = &goalTestAgent{session: s}
				env.engine.cmdGoal(env.plat, msg, nil)
			} else {
				blocked := &blockingGoalSession{goalTestSession: s, getEntered: make(chan struct{}), getRelease: make(chan struct{})}
				env.engine.agent = &concurrentGoalAgent{session: blocked}
				var releaseGet, releaseSchedule sync.Once
				scheduleEntered, scheduleRelease := make(chan struct{}), make(chan struct{})
				defer func() {
					releaseGet.Do(func() { close(blocked.getRelease) })
					releaseSchedule.Do(func() { close(scheduleRelease) })
				}()
				goalDone := make(chan struct{})
				go func() { env.engine.cmdGoal(env.plat, msg, nil); close(goalDone) }()
				select {
				case <-blocked.getEntered:
				case <-time.After(time.Second):
					t.Fatal("query did not reach RPC")
				}
				sentBefore := 0
				if name == "late_after_queue" {
					s.pendingEvents = []Event{}
					env.userSends("goal-user", "queued during Goal query")
					releaseGet.Do(func() { close(blocked.getRelease) })
					env.waitFor("queued Send", time.Second, func() bool { return len(s.getSentPrompts()) == 1 })
					sentBefore = 1
				}
				env.engine.interactiveMu.Lock()
				state := env.engine.interactiveStates[key]
				env.engine.interactiveMu.Unlock()
				state.mu.Lock()
				state.agentSessionIdleCancel = func() {
					close(scheduleEntered)
					<-scheduleRelease
				}
				state.mu.Unlock()
				env.engine.agentSessionIdleTimeoutNanos.Store(int64(10 * time.Millisecond))
				if sentBefore == 1 {
					s.events <- Event{Type: EventResult, Content: "queued task finished", Done: true}
				} else {
					releaseGet.Do(func() { close(blocked.getRelease) })
				}
				select {
				case <-scheduleEntered:
				case <-time.After(time.Second):
					t.Fatal("Goal exit did not reach idle scheduling")
				}
				if env.engine.sessions.GetOrCreateActive(key).Busy() {
					t.Fatal("test must intercept scheduling after Goal unlock")
				}
				s.mu.Lock()
				s.pendingEvents = []Event{}
				s.mu.Unlock()
				env.userSends("goal-user", "Keep this next task running")
				env.waitFor("next foreground Send", time.Second, func() bool { return len(s.getSentPrompts()) == sentBefore+1 })
				releaseSchedule.Do(func() { close(scheduleRelease) })
				select {
				case <-goalDone:
				case <-time.After(time.Second):
					t.Fatal("Goal exit did not finish")
				}
				time.Sleep(30 * time.Millisecond)
				if !s.Alive() {
					t.Fatal("late Goal idle timer closed a busy foreground session")
				}
				s.events <- Event{Type: EventResult, Content: "foreground completed", Done: true}
				env.waitFor("foreground result", time.Second, func() bool {
					return strings.Contains(strings.Join(env.plat.getSent(), "\n"), "foreground completed")
				})
				return
			}
			env.engine.interactiveMu.Lock()
			state := env.engine.interactiveStates[key]
			env.engine.interactiveMu.Unlock()
			state.mu.Lock()
			token := state.agentSessionIdleToken
			state.mu.Unlock()
			session := env.engine.sessions.GetOrCreateActive(key)
			gen, ok := session.TryLock()
			if !ok {
				t.Fatal("foreground could not acquire session")
			}
			defer session.Unlock(gen)
			env.engine.cleanupInteractiveStateForIdleToken(key, state, token, time.Hour)
			if !s.Alive() {
				t.Fatal("late Goal idle timer closed a busy foreground session")
			}
		})
	}
}
