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

func TestIssue1076_CustomAnswerLabelsMatchWholeOption(t *testing.T) {
	for _, label := range []string{"Other", " Others... ", "OTHER…", "Custom:", "Custom Answer：", "Something Else", "Type Your Own", "其他", "其它", "自定义", "自由输入", "その他", "自由入力", "Otro", "Otra", "Personalizado", "Personalizada"} {
		if !isCustomAnswerOption(UserQuestionOption{Label: label}) {
			t.Errorf("%q should request a typed answer", label)
		}
	}
	for _, label := range []string{"Use another library", "Other approach is faster", "Customize settings"} {
		if isCustomAnswerOption(UserQuestionOption{Label: label}) {
			t.Errorf("%q should remain a selectable option", label)
		}
	}
}

func TestIssue1076_OrdinaryOptionsRemainSelectable(t *testing.T) {
	labels := []string{"Use another library", "Other approach is faster", "Customize settings"}
	questions := []UserQuestion{{Question: "Choose an approach", Options: []UserQuestionOption{{Label: labels[0]}, {Label: labels[1]}, {Label: labels[2]}}}}
	e := newTestEngine()
	inline := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e.sendAskQuestionPrompt(inline, "ctx", questions, 0)
	if got := len(inline.buttonRows); got != len(labels) {
		t.Fatalf("inline buttons=%d, want %d", got, len(labels))
	}
	card := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e.sendAskQuestionPrompt(card, "ctx", questions, 0)
	if got := countCardActionValues(card.sentCards[0], "askq:"); got != len(labels) {
		t.Fatalf("card buttons=%d, want %d", got, len(labels))
	}
	for i, label := range labels {
		p := &stubPlatformEngine{n: "test"}
		rec := &recordingAgentSession{}
		pending := &pendingPermission{RequestID: "req-ordinary", ToolName: "AskUserQuestion", Questions: questions, ToolInput: map[string]any{}, Resolved: make(chan struct{})}
		key := "test:ordinary:user"
		e.interactiveMu.Lock()
		e.interactiveStates[key] = &interactiveState{agentSession: rec, pending: pending}
		e.interactiveMu.Unlock()
		if !e.handlePendingPermission(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, string(rune('1'+i)), key) {
			t.Fatalf("option %q was not handled", label)
		}
		if rec.calls != 1 {
			t.Errorf("option %q: RespondPermission calls=%d, want 1", label, rec.calls)
		}
	}
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
