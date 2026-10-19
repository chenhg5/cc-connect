package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// askqCardPlatform renders AskUserQuestion as a card with its own answer box
// (option buttons plus a text input), like Feishu's schema 2.0 card. With
// err set it reports the card as unavailable so the engine falls back.
type askqCardPlatform struct {
	*stubPlatformEngine
	err error
}

func (p *askqCardPlatform) SendAskQuestionCard(ctx context.Context, replyCtx any, _ AskQuestionCardTexts, q UserQuestion, _ int) error {
	if p.err != nil {
		return p.err
	}
	return p.Send(ctx, replyCtx, "[card] "+q.Question)
}

// CUJ: while a question card is open, the user sends a chat message about
// something else, then answers in the card. The chat message must not be
// taken as the answer; it waits in the queue and reaches the agent as a
// normal prompt after the turn. Before the fix the chat message answered the
// question and the card answer was dropped.
func TestCUJ_C8_ChatMessageDoesNotAnswerQuestionCard(t *testing.T) {
	p := &askqCardPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	session := &askqJourneySession{
		receiptAgentSession: &receiptAgentSession{queuingAgentSession: newQueuingSession("askq-card-session")},
	}
	e := NewEngine("askq", &askqJourneyAgent{session: session}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	env := &cujEnv{t: t, engine: e, plat: p.stubPlatformEngine}
	send := func(content string, fromCard bool) {
		e.ReceiveMessage(p, &Message{
			SessionKey: "test:asker", Platform: "test", UserID: "asker", UserName: "asker",
			MessageID: "m-" + content, Content: content, ReplyCtx: "ctx",
			IsAskQuestionAnswer: fromCard,
		})
	}

	// User action 1: start a task; the agent asks a question as a card.
	send("do the task", false)
	env.waitFor("task sent to agent", 3*time.Second, func() bool { return len(session.prompts()) == 1 })
	session.events <- Event{Type: EventPermissionRequest, RequestID: "req-q", ToolName: "AskUserQuestion",
		ToolInputRaw: map[string]any{}, Questions: testQuestions()}
	env.waitFor("question card shown", 3*time.Second, func() bool { return env.sentContains("[card] Which database?") })

	// User action 2: a chat message about something else while the card is open.
	send("let another session rescue you", false)
	env.waitFor("queued notice", 3*time.Second, func() bool {
		return env.sentContains(e.i18n.T(MsgMessageQueuedQuestionOpen))
	})
	if n := session.answerCount(); n != 0 {
		t.Fatalf("chat message was taken as the answer (answers sent = %d)", n)
	}

	// User action 3: answer in the card's text box.
	send("SQLite, but keep a migration path", true)
	env.waitFor("card answer delivered", 3*time.Second, func() bool { return session.answerCount() == 1 })
	session.answerMu.Lock()
	answers, _ := session.answers[0].UpdatedInput["answers"].(map[string]any)
	session.answerMu.Unlock()
	if got := answers["Which database?"]; got != "SQLite, but keep a migration path" {
		t.Fatalf("answer sent to agent = %v, want the card answer", got)
	}

	// The turn ends; the chat message now reaches the agent as its own prompt.
	session.events <- Event{Type: EventResult, Content: "task done", Done: true}
	env.waitFor("queued chat message sent", 3*time.Second, func() bool { return len(session.prompts()) >= 2 })
	if got, want := fmt.Sprint(session.prompts()), fmt.Sprint([]string{"do the task", "let another session rescue you"}); got != want {
		t.Fatalf("prompts sent to agent = %s, want %s", got, want)
	}

	// A queued message is saved to the session store after it is sent, so
	// finish its turn and wait for the session to go idle before the temp
	// dir is removed.
	session.events <- Event{Type: EventResult, Content: "chat message handled", Done: true}
	env.waitFor("queued turn finished", 3*time.Second, func() bool {
		return env.sentContains("chat message handled") && !e.sessions.GetOrCreateActive("test:asker").Busy()
	})
}

func TestHandlePendingPermission_CardAnswerOnly(t *testing.T) {
	setup := func(t *testing.T, cardErr error, questions []UserQuestion) (*Engine, *askqCardPlatform, *recordingAgentSession, *pendingPermission) {
		t.Helper()
		e := newTestEngine()
		p := &askqCardPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}, err: cardErr}
		rec := &recordingAgentSession{}
		pending := &pendingPermission{
			RequestID: "req-q", ToolName: "AskUserQuestion", ToolInput: map[string]any{},
			Questions: questions, Resolved: make(chan struct{}),
		}
		state := &interactiveState{agentSession: rec, platform: p, replyCtx: "ctx", pending: pending}
		e.interactiveMu.Lock()
		e.interactiveStates["ws:sk"] = state
		e.interactiveMu.Unlock()
		// Same order as the engine: pending is registered, then the prompt is sent.
		pending.CardAnswerOnly = e.sendAskQuestionPrompt(p, "ctx", questions, 0)
		return e, p, rec, pending
	}
	chat := func(content string) *Message { return &Message{SessionKey: "sk", Content: content, ReplyCtx: "ctx"} }

	t.Run("chat text and option numbers do not answer the card", func(t *testing.T) {
		e, p, rec, pending := setup(t, nil, testQuestions())
		if !pending.CardAnswerOnly {
			t.Fatal("question sent as a card with an answer box must be card-answer-only")
		}
		for _, text := range []string{"let another session rescue you", "2"} {
			if e.handlePendingPermission(p, chat(text), text, "ws:sk") {
				t.Fatalf("chat message %q was consumed as the answer", text)
			}
		}
		if rec.calls != 0 {
			t.Fatalf("RespondPermission calls = %d, want 0", rec.calls)
		}
		// An option tap from the card still answers.
		if !e.handlePendingPermission(p, chat("askq:0:2"), "askq:0:2", "ws:sk") || rec.calls != 1 {
			t.Fatalf("option tap did not answer (calls = %d)", rec.calls)
		}
	})

	t.Run("card unavailable: fallback prompt keeps chat answers", func(t *testing.T) {
		e, p, rec, pending := setup(t, errors.New("card rejected"), testQuestions())
		if pending.CardAnswerOnly {
			t.Fatal("fallback prompt has no answer box; chat must still answer it")
		}
		if !e.handlePendingPermission(p, chat("2"), "2", "ws:sk") || rec.calls != 1 {
			t.Fatalf("chat answer to the fallback prompt was not accepted (calls = %d)", rec.calls)
		}
	})

	t.Run("next question without an answer box accepts chat again", func(t *testing.T) {
		qs := testMultiQuestions()
		qs[1].MultiSelect = true // multi-select questions are not sent as the answer-box card
		e, p, rec, pending := setup(t, nil, qs)

		msg := chat("PostgreSQL")
		msg.IsAskQuestionAnswer = true
		if !e.handlePendingPermission(p, msg, "PostgreSQL", "ws:sk") {
			t.Fatal("card answer to question 1 was not accepted")
		}
		if pending.CurrentQuestion != 1 || pending.CardAnswerOnly {
			t.Fatalf("after question 1: CurrentQuestion=%d CardAnswerOnly=%v, want 1 false", pending.CurrentQuestion, pending.CardAnswerOnly)
		}
		if !e.handlePendingPermission(p, chat("1,2"), "1,2", "ws:sk") || rec.calls != 1 {
			t.Fatalf("chat answer to the multi-select question was not accepted (calls = %d)", rec.calls)
		}
		answers, _ := rec.lastResult.UpdatedInput["answers"].(map[string]any)
		if answers["Which database?"] != "PostgreSQL" || answers["Which framework?"] != "Gin, Echo" {
			t.Fatalf("answers = %v", answers)
		}
	})

	t.Run("card answer with no open question is not sent to the agent", func(t *testing.T) {
		e := newTestEngine()
		p := &stubPlatformEngine{n: "test"}
		msg := chat("typed after /stop")
		msg.IsAskQuestionAnswer = true
		if !e.handlePendingPermission(p, msg, msg.Content, "ghost") {
			t.Fatal("card answer with no open question fell through to the normal message path")
		}
		if fmt.Sprint(p.getSent()) != fmt.Sprint([]string{e.i18n.T(MsgAskQuestionStale)}) {
			t.Fatalf("sent = %v, want only the stale-question notice", p.getSent())
		}
	})
}
