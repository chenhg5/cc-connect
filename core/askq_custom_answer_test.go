package core

import (
	"strings"
	"testing"
)

func customAnswerQuestion() []UserQuestion {
	return []UserQuestion{{
		Question: "Which database?",
		Options: []UserQuestionOption{
			{Label: "PostgreSQL"},
			{Label: "SQLite"},
			{Label: "Other", Description: "Type a different database"},
		},
	}}
}

func TestIssue1076_CustomOptionRequiresTypedAnswer(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	rec := &recordingAgentSession{}
	pending := &pendingPermission{RequestID: "req-custom", ToolName: "AskUserQuestion", Questions: customAnswerQuestion(), ToolInput: map[string]any{}, Resolved: make(chan struct{})}
	key := "test:custom:user"
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: rec, pending: pending}
	e.interactiveMu.Unlock()
	msg := &Message{SessionKey: key, ReplyCtx: "ctx"}
	if !e.handlePendingPermission(p, msg, "askq:0:3", key) {
		t.Fatal("custom option click was not handled")
	}
	if rec.calls != 0 || pending.Answers[0] != "" {
		t.Fatalf("custom option submitted before text: calls=%d answers=%v", rec.calls, pending.Answers)
	}
	if !strings.Contains(strings.ToLower(strings.Join(p.sent, "\n")), "type") {
		t.Fatalf("custom option did not request typed answer: %v", p.sent)
	}
	if !e.handlePendingPermission(p, msg, "MariaDB", key) {
		t.Fatal("typed answer was not handled")
	}
	if rec.calls != 1 {
		t.Fatalf("RespondPermission calls=%d, want 1", rec.calls)
	}
	answers, ok := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	if !ok || answers["Which database?"] != "MariaDB" {
		t.Fatalf("custom answer was not sent to agent: %v", rec.lastResult.UpdatedInput)
	}
}

func TestIssue1076_CustomOptionIsNotAButton(t *testing.T) {
	e := newTestEngine()
	questions := customAnswerQuestion()
	cardPlatform := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e.sendAskQuestionPrompt(cardPlatform, "ctx", questions, 0)
	if got := countCardActionValues(cardPlatform.sentCards[0], "askq:"); got != 2 {
		t.Fatalf("card option buttons=%d, want 2 non-custom options", got)
	}
	if !strings.Contains(strings.ToLower(cardPlatform.sentCards[0].RenderText()), "type") {
		t.Fatal("card does not instruct user to type a custom answer")
	}
	inlinePlatform := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e.sendAskQuestionPrompt(inlinePlatform, "ctx", questions, 0)
	if got := len(inlinePlatform.buttonRows); got != 2 {
		t.Fatalf("inline option buttons=%d, want 2 non-custom options", got)
	}
}

func TestIssue1076_CustomNumberAndMultiSelectDoNotSubmitLabel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		multi bool
	}{
		{name: "number", input: "3"},
		{name: "multi select includes custom", input: "1,3", multi: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine()
			p := &stubPlatformEngine{n: "test"}
			rec := &recordingAgentSession{}
			questions := customAnswerQuestion()
			questions[0].MultiSelect = tc.multi
			pending := &pendingPermission{RequestID: "req-custom", ToolName: "AskUserQuestion", Questions: questions, ToolInput: map[string]any{}, Resolved: make(chan struct{})}
			key := "test:custom:user"
			e.interactiveMu.Lock()
			e.interactiveStates[key] = &interactiveState{agentSession: rec, pending: pending}
			e.interactiveMu.Unlock()
			if !e.handlePendingPermission(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, tc.input, key) {
				t.Fatal("custom option was not handled")
			}
			if rec.calls != 0 || pending.Answers[0] != "" {
				t.Fatalf("custom label was submitted: calls=%d answers=%v", rec.calls, pending.Answers)
			}
		})
	}
}
