package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitForQueuedSends(t *testing.T, s *queuingAgentSession, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.sendMu.Lock()
		got := len(s.sendCalls)
		s.sendMu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	t.Fatalf("agent received %d prompts, want %d: %q", len(s.sendCalls), n, s.sendCalls)
}

func waitForSessionIdle(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("session still busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A user message that arrives while the agent is busy is queued and handled
// after the running turn. It is still the user talking, so it must restart the
// reset_on_idle_mins clock; otherwise the user's next message is measured
// against the last message that happened to find the session idle and can be
// moved to a fresh session in the middle of a conversation.
func TestHandleMessage_QueuedUserMessageRefreshesIdleTimer(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newQueuingSession("idle-queue")
	started := 0
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		started++
		if started == 1 {
			return as, nil
		}
		return newQueuingSession("replacement"), nil
	}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetResetOnIdle(60 * time.Minute)

	key := "test:user1"
	userMsg := func(content, replyCtx string) *Message {
		return &Message{SessionKey: key, Platform: "test", UserID: "u1", UserName: "user", Content: content, ReplyCtx: replyCtx}
	}

	e.handleMessage(p, userMsg("start a long task", "ctx-1"))
	waitForQueuedSends(t, as, 1)
	session := e.sessions.GetOrCreateActive(key)

	// The turn has been running for 70 minutes when the user writes again.
	session.mu.Lock()
	session.LastUserActivity = time.Now().Add(-70 * time.Minute)
	session.mu.Unlock()

	queuedAt := time.Now()
	e.handleMessage(p, userMsg("one more thing", "ctx-2"))
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	state.mu.Unlock()
	if queued != 1 {
		t.Fatalf("pending messages = %d, want the second message queued behind the running turn", queued)
	}
	if got := session.GetLastUserActivity(); got.Before(queuedAt) {
		t.Errorf("LastUserActivity = %s after a user message was queued, want >= %s (when it was queued)",
			got.Format(time.TimeOnly), queuedAt.Format(time.TimeOnly))
	}

	as.events <- Event{Type: EventResult, Content: "long task done", Done: true}
	waitForQueuedSends(t, as, 2)
	as.events <- Event{Type: EventResult, Content: "a question for you", Done: true}
	waitForSessionIdle(t, session)

	// The user answers right away.
	e.handleMessage(p, userMsg("yes", "ctx-3"))
	if active := e.sessions.GetOrCreateActive(key); active.ID != session.ID {
		t.Fatalf("reply sent right after the queued message was moved to new session %s, want it to stay in %s; replies: %q",
			active.ID, session.ID, p.getSent())
	}
	for _, line := range p.getSent() {
		if strings.Contains(line, "Session auto-reset") {
			t.Fatalf("unexpected idle auto-reset notice; replies: %q", p.getSent())
		}
	}
	waitForQueuedSends(t, as, 3)
	as.events <- Event{Type: EventResult, Content: "ok", Done: true}
	waitForSessionIdle(t, session)
}

// Turns injected by the engine itself are not user activity (#1115): they never
// queue (they fail fast when the session is busy) and must not keep an
// otherwise idle session from being reset.
func TestInjectedTurnsDoNotRefreshIdleTimer(t *testing.T) {
	cases := []struct {
		name string
		run  func(e *Engine, key string) error
	}{
		{"timer", func(e *Engine, key string) error {
			return e.ExecuteTimerJob(&TimerJob{ID: "t1", Project: "test", SessionKey: key, Prompt: "status from another tool"})
		}},
		{"heartbeat", func(e *Engine, key string) error {
			return e.ExecuteHeartbeat(key, "heartbeat check", true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &cujReplyCtxPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
			as := newResultAgentSession("noted")
			e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
			e.SetResetOnIdle(60 * time.Minute)

			key := "test:user1"
			session := e.sessions.GetOrCreateActive(key)
			session.AddHistory("user", "earlier context")
			session.SetAgentSessionID("old-session", "stub")
			staleAt := time.Now().Add(-70 * time.Minute)
			session.mu.Lock()
			session.LastUserActivity = staleAt
			session.mu.Unlock()

			if err := tc.run(e, key); err != nil {
				t.Fatalf("injected turn: %v", err)
			}
			if len(as.sentPrompts) != 1 {
				t.Fatalf("agent received %d prompts from the %s turn, want 1", len(as.sentPrompts), tc.name)
			}
			if got := session.GetLastUserActivity(); !got.Equal(staleAt) {
				t.Errorf("LastUserActivity = %s after a %s turn, want unchanged %s",
					got.Format(time.TimeOnly), tc.name, staleAt.Format(time.TimeOnly))
			}

			gen, ok := session.TryLock()
			if !ok {
				t.Fatalf("session still busy after the %s turn", tc.name)
			}
			rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, e.sessions, key, session, gen)
			if rotated == nil {
				t.Fatalf("a %s turn kept the session from idle reset", tc.name)
			}
		})
	}
}

// Only a message that actually joins the queue counts. One the queue turns
// away (full, or a stale redelivery) is not delivered to the session, so, as
// on the idle path, it does not count as activity on it.
func TestHandleMessage_BusySessionCountsOnlyQueuedMessagesAsActivity(t *testing.T) {
	cases := []struct {
		name       string
		prefill    int
		createdMs  int64
		wantQueued bool
	}{
		{"queued", 0, 6_000, true},
		{"queue full", defaultMaxQueuedMessages, 6_000, false},
		{"stale redelivery", 0, 3_000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			as := newQueuingSession("busy")
			e := NewEngine("test", &controllableAgent{nextSession: as}, []Platform{p}, "", LangEnglish)
			e.SetResetOnIdle(60 * time.Minute)

			key := "test:user1"
			session := e.sessions.GetOrCreateActive(key)
			if _, ok := session.TryLock(); !ok {
				t.Fatal("could not lock session")
			}
			staleAt := time.Now().Add(-70 * time.Minute)
			session.mu.Lock()
			session.LastUserActivity = staleAt
			session.mu.Unlock()

			state := &interactiveState{
				agentSession:                   as,
				busySession:                    session,
				platform:                       p,
				replyCtx:                       "ctx",
				lastCompletedUserMessageTimeMs: 5_000,
			}
			for i := 0; i < tc.prefill; i++ {
				state.pendingMessages = append(state.pendingMessages, queuedMessage{platform: p, content: "earlier"})
			}
			e.interactiveMu.Lock()
			e.interactiveStates[key] = state
			e.interactiveMu.Unlock()

			before := time.Now()
			e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", UserName: "user",
				Content: "while busy", ReplyCtx: "ctx-new", UserMessageTimeMs: tc.createdMs})

			state.mu.Lock()
			depth := len(state.pendingMessages)
			state.mu.Unlock()
			wantDepth := tc.prefill
			if tc.wantQueued {
				wantDepth++
			}
			if depth != wantDepth {
				t.Fatalf("queue depth = %d, want %d; replies: %q", depth, wantDepth, p.getSent())
			}
			got := session.GetLastUserActivity()
			if tc.wantQueued && got.Before(before) {
				t.Errorf("LastUserActivity = %s after the message was queued, want >= %s",
					got.Format(time.TimeOnly), before.Format(time.TimeOnly))
			}
			if !tc.wantQueued && !got.Equal(staleAt) {
				t.Errorf("LastUserActivity = %s after a message the queue turned away, want unchanged %s",
					got.Format(time.TimeOnly), staleAt.Format(time.TimeOnly))
			}
		})
	}
}
