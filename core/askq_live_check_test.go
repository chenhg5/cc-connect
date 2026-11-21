package core

import (
	"testing"
	"time"
)

// askQuestionCallbackLive must agree with handlePendingPermission: a click
// is live only while its question is the one waiting (#2302).
func TestAskQuestionCallbackLive(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	session := &askqJourneySession{
		receiptAgentSession: &receiptAgentSession{queuingAgentSession: newQueuingSession("askq-live")},
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

	if e.askQuestionCallbackLive("test:asker", "askq:0:1") {
		t.Fatal("click with no question asked reported live")
	}
	if !e.askQuestionCallbackLive("test:asker", "plain text") {
		t.Fatal("non-option content must be reported live")
	}

	send("do the task")
	env.waitFor("task sent", 3*time.Second, func() bool { return len(session.prompts()) == 1 })
	session.events <- Event{Type: EventPermissionRequest, RequestID: "req-q", ToolName: "AskUserQuestion",
		ToolInputRaw: map[string]any{}, Questions: testQuestions()}
	env.waitFor("question shown", 3*time.Second, func() bool { return env.sentContains("Which database?") })

	if !e.askQuestionCallbackLive("test:asker", "askq:0:1") {
		t.Fatal("click on the waiting question reported stale")
	}
	if !e.askQuestionCallbackLive("test:asker", "askq:1") {
		t.Fatal("legacy click without question index reported stale")
	}
	if e.askQuestionCallbackLive("test:asker", "askq:5:1") {
		t.Fatal("click on another question's card reported live")
	}

	send("PostgreSQL please")
	env.waitFor("answer delivered", 3*time.Second, func() bool { return session.answerCount() == 1 })
	if e.askQuestionCallbackLive("test:asker", "askq:0:1") {
		t.Fatal("click after a text answer reported live")
	}
}
