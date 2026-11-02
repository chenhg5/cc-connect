package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// askqJourneySession is a busy agent session the test drives event by event,
// recording both prompts and AskUserQuestion answers sent to the agent.
type askqJourneySession struct {
	*receiptAgentSession
	answerMu sync.Mutex
	answers  []PermissionResult
}

func (s *askqJourneySession) RespondPermission(_ string, result PermissionResult) error {
	s.answerMu.Lock()
	defer s.answerMu.Unlock()
	s.answers = append(s.answers, result)
	return nil
}

func (s *askqJourneySession) answerCount() int {
	s.answerMu.Lock()
	defer s.answerMu.Unlock()
	return len(s.answers)
}

func (s *askqJourneySession) prompts() []string {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return append([]string(nil), s.sendCalls...)
}

type askqJourneyAgent struct {
	stubAgent
	session *askqJourneySession
}

func (a *askqJourneyAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

// CUJ: the agent asks a question, the user answers it in chat, then taps the
// question card's option button while the turn is still running. The late
// tap must not reach the agent as a prompt. Before the fix it was queued as
// literal "askq:0:2" text and sent to the agent once the turn ended.
func TestCUJ_C7_StaleAskQuestionClickNotSentToAgent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	session := &askqJourneySession{
		receiptAgentSession: &receiptAgentSession{queuingAgentSession: newQueuingSession("askq-session")},
	}
	e := NewEngine("askq", &askqJourneyAgent{session: session}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	env := &cujEnv{t: t, engine: e, plat: p}
	send := func(content string) {
		e.ReceiveMessage(p, &Message{
			SessionKey: "test:asker", Platform: "test", UserID: "asker", UserName: "asker",
			MessageID: "m-" + content, Content: content, ReplyCtx: "ctx",
		})
	}

	// User action 1: start a task; the agent asks a question mid-turn.
	send("do the task")
	env.waitFor("task sent to agent", 3*time.Second, func() bool { return len(session.prompts()) == 1 })
	session.events <- Event{Type: EventPermissionRequest, RequestID: "req-q", ToolName: "AskUserQuestion",
		ToolInputRaw: map[string]any{}, Questions: testQuestions()}
	env.waitFor("question shown", 3*time.Second, func() bool { return env.sentContains("Which database?") })

	// User action 2: answer in chat. The question is resolved, the turn goes on.
	send("PostgreSQL please")
	env.waitFor("answer delivered", 3*time.Second, func() bool { return session.answerCount() == 1 })

	// User action 3: tap the card's option button after the question is gone.
	p.clearSent()
	send("askq:0:2")
	env.waitFor("stale notice", 3*time.Second, func() bool {
		return env.sentContains(e.i18n.T(MsgAskQuestionStale))
	})
	if env.sentContains(e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("stale option tap was queued for the agent; sent: %v", p.getSent())
	}

	// User action 4: the turn ends and the user sends the next task. A queued
	// tap would be drained first and sent to the agent before this one.
	session.events <- Event{Type: EventResult, Content: "task done", Done: true}
	env.waitFor("turn result", 3*time.Second, func() bool { return env.sentContains("task done") })
	send("next task")
	env.waitFor("next task sent", 3*time.Second, func() bool { return len(session.prompts()) >= 2 })
	if got, want := fmt.Sprint(session.prompts()), fmt.Sprint([]string{"do the task", "next task"}); got != want {
		t.Fatalf("prompts sent to agent = %s, want %s", got, want)
	}
	if n := session.answerCount(); n != 1 {
		t.Fatalf("AskUserQuestion answers sent = %d, want 1", n)
	}
}

func TestParseAskQuestionCallback(t *testing.T) {
	cases := []struct {
		in      string
		qIdx    int
		hasQIdx bool
		ok      bool
	}{
		{"askq:0:2", 0, true, true},
		{"askq:3:1", 3, true, true},
		{" askq:1:1 ", 1, true, true},
		{"askq:2", 0, false, true}, // legacy askq:optIdx
		{"askq:", 0, false, false},
		{"askq: use the second one", 0, false, false},
		{"askq:0:x", 0, false, false},
		{"askq:-1:2", 0, false, false},
		{"askq:1:2:3", 0, false, false},
		{"2", 0, false, false},
		{"PostgreSQL", 0, false, false},
	}
	for _, c := range cases {
		qIdx, hasQIdx, ok := parseAskQuestionCallback(c.in)
		if qIdx != c.qIdx || hasQIdx != c.hasQIdx || ok != c.ok {
			t.Errorf("parseAskQuestionCallback(%q) = (%d, %v, %v), want (%d, %v, %v)",
				c.in, qIdx, hasQIdx, ok, c.qIdx, c.hasQIdx, c.ok)
		}
	}
}

func TestHandlePendingPermission_StaleAskQuestionCallback(t *testing.T) {
	stale := func(e *Engine, p *stubPlatformEngine) bool {
		return fmt.Sprint(p.getSent()) == fmt.Sprint([]string{e.i18n.T(MsgAskQuestionStale)})
	}

	t.Run("no interactive state (e.g. after restart)", func(t *testing.T) {
		e := newTestEngine()
		p := &stubPlatformEngine{n: "test"}
		msg := &Message{SessionKey: "sk", Content: "askq:0:1", ReplyCtx: "ctx"}
		if !e.handlePendingPermission(p, msg, "askq:0:1", "ghost") {
			t.Fatal("stale option tap fell through to the normal message path")
		}
		if !stale(e, p) {
			t.Fatalf("sent = %v, want only the stale-question notice", p.getSent())
		}
	})

	t.Run("tool permission pending, not a question", func(t *testing.T) {
		e := newTestEngine()
		p := &stubPlatformEngine{n: "test"}
		rec := &recordingAgentSession{}
		pending := &pendingPermission{RequestID: "req-bash", ToolName: "Bash", Resolved: make(chan struct{})}
		state := &interactiveState{agentSession: rec, platform: p, replyCtx: "ctx", pending: pending}
		e.interactiveMu.Lock()
		e.interactiveStates["ws:sk"] = state
		e.interactiveMu.Unlock()

		msg := &Message{SessionKey: "sk", Content: "askq:0:1", ReplyCtx: "ctx"}
		if !e.handlePendingPermission(p, msg, "askq:0:1", "ws:sk") {
			t.Fatal("expected the tap to be handled")
		}
		if rec.calls != 0 {
			t.Fatalf("RespondPermission calls = %d, want 0", rec.calls)
		}
		if state.pending != pending {
			t.Fatal("the tool permission request must stay pending")
		}
		if !stale(e, p) {
			t.Fatalf("sent = %v, want only the stale-question notice", p.getSent())
		}
	})

	t.Run("tap on an earlier question's card", func(t *testing.T) {
		e := newTestEngine()
		p := &stubPlatformEngine{n: "test"}
		rec := &recordingAgentSession{}
		pending := &pendingPermission{
			RequestID: "req-q", ToolName: "AskUserQuestion", ToolInput: map[string]any{},
			Questions: testMultiQuestions(), Answers: map[int]string{0: "PostgreSQL"},
			CurrentQuestion: 1, Resolved: make(chan struct{}),
		}
		state := &interactiveState{agentSession: rec, platform: p, replyCtx: "ctx", pending: pending}
		e.interactiveMu.Lock()
		e.interactiveStates["ws:sk"] = state
		e.interactiveMu.Unlock()

		msg := &Message{SessionKey: "sk", ReplyCtx: "ctx"}
		if !e.handlePendingPermission(p, msg, "askq:0:2", "ws:sk") {
			t.Fatal("expected the tap to be handled")
		}
		if rec.calls != 0 || pending.Answers[1] != "" {
			t.Fatalf("tap on question 0's card answered question 1 (calls=%d, answers=%v)", rec.calls, pending.Answers)
		}
		if !stale(e, p) {
			t.Fatalf("sent = %v, want only the stale-question notice", p.getSent())
		}

		// The current question's card still answers it.
		if !e.handlePendingPermission(p, msg, "askq:1:2", "ws:sk") {
			t.Fatal("expected the current card's tap to be handled")
		}
		if rec.calls != 1 {
			t.Fatalf("RespondPermission calls = %d, want 1", rec.calls)
		}
		answers, _ := rec.lastResult.UpdatedInput["answers"].(map[string]any)
		if answers["Which framework?"] != "Echo" || answers["Which database?"] != "PostgreSQL" {
			t.Fatalf("answers = %v", answers)
		}
	})
}
