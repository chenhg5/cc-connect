package core

import (
	"strings"
	"testing"
	"time"
)

// perTurnProcessSession mimics an agent whose Send starts one CLI process per
// turn (Antigravity print mode).
type perTurnProcessSession struct {
	*queuingAgentSession
	perTurn bool
}

func (s *perTurnProcessSession) SpawnsProcessPerTurn() bool { return s.perTurn }

func (s *perTurnProcessSession) sends() []string {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return append([]string(nil), s.sendCalls...)
}

func busyPsEngine(t *testing.T, sess AgentSession) (*Engine, *stubPlatformEngine, *interactiveState, *Session) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	state := &interactiveState{agentSession: sess, platform: p}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	session := e.sessions.GetOrCreateActive(key)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("expected TryLock to succeed")
	}
	t.Cleanup(func() { session.Unlock(0) })
	return e, p, state, session
}

func TestCmdPs_PerTurnProcessSession_QueuesInsteadOfSecondProcess(t *testing.T) {
	sess := &perTurnProcessSession{queuingAgentSession: newQueuingSession("ps-per-turn"), perTurn: true}
	e, p, state, _ := busyPsEngine(t, sess)

	e.cmdPs(p, &Message{SessionKey: "test:user1", MessageID: "m-ps", UserID: "u", Content: "/ps add unit tests", ReplyCtx: "ctx"}, []string{"add", "unit", "tests"})

	if calls := sess.sends(); len(calls) != 0 {
		t.Fatalf("Send while busy would start a second process: %v", calls)
	}
	state.mu.Lock()
	pending := append([]queuedMessage(nil), state.pendingMessages...)
	state.mu.Unlock()
	if len(pending) != 1 || pending[0].content != "add unit tests" || pending[0].messageID != "m-ps" {
		t.Fatalf("pending = %+v, want the P.S. text queued as the next turn", pending)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != e.i18n.T(MsgPsQueued) {
		t.Fatalf("replies = %v, want only the P.S. queued notice", sent)
	}
}

func TestCmdPs_SessionNotSpawningPerTurn_StillSends(t *testing.T) {
	// e.g. the Antigravity ACP session, which embeds the print-mode session.
	sess := &perTurnProcessSession{queuingAgentSession: newQueuingSession("ps-live"), perTurn: false}
	e, p, state, _ := busyPsEngine(t, sess)

	e.cmdPs(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"add", "unit", "tests"})

	if calls := sess.sends(); len(calls) != 1 || calls[0] != "add unit tests" {
		t.Fatalf("sends = %v, want the P.S. sent into the live process", calls)
	}
	state.mu.Lock()
	n := len(state.pendingMessages)
	state.mu.Unlock()
	if n != 0 {
		t.Fatalf("pending = %d, want none", n)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != e.i18n.T(MsgPsSent) {
		t.Fatalf("replies = %v, want P.S. delivered", sent)
	}
}

// User journey: while a task runs on a one-process-per-turn agent, the user
// sends /ps. The agent must not get a second prompt during the task; the P.S.
// becomes the next turn and both answers arrive in order.
func TestCUJ_PsOnPerTurnProcessAgentRunsAfterCurrentTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := &perTurnProcessSession{queuingAgentSession: newQueuingSession("ps-cuj"), perTurn: true}
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	const key = "test:ps-per-turn"
	send := func(id, text string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: p.Name(), UserID: "user", MessageID: id, Content: text, ReplyCtx: "ctx"})
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s; sends=%v replies=%v", what, sess.sends(), p.getSent())
			}
			time.Sleep(time.Millisecond)
		}
	}
	replied := func(want string) func() bool {
		return func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), want) }
	}

	send("m-a", "run the tests")
	waitFor("the task's prompt", func() bool { return len(sess.sends()) == 1 })
	send("m-ps", "/ps also check the docs")
	waitFor("the queued notice", replied(e.i18n.T(MsgPsQueued)))
	if calls := sess.sends(); len(calls) != 1 {
		t.Fatalf("P.S. reached the agent during the task: %v", calls)
	}

	sess.events <- Event{Type: EventResult, Content: "tests passed", Done: true}
	waitFor("the P.S. as the next prompt", func() bool { return len(sess.sends()) == 2 })
	if calls := sess.sends(); !strings.Contains(calls[1], "also check the docs") {
		t.Fatalf("second prompt = %q, want the P.S. text", calls[1])
	}
	sess.events <- Event{Type: EventResult, Content: "docs checked", Done: true}
	waitFor("the P.S. answer", replied("docs checked"))

	all := strings.Join(p.getSent(), "\n")
	if strings.Index(all, "tests passed") > strings.Index(all, "docs checked") {
		t.Fatalf("answers out of order: %v", p.getSent())
	}
	if strings.Contains(all, e.i18n.T(MsgPsSent)) {
		t.Fatalf("P.S. reported as delivered into the running task: %v", p.getSent())
	}
}
