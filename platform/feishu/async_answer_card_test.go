package feishu

import (
	"testing"
	"time"
)

func TestOnCardAction_AsyncAnswerDoesNotClaimSuccessBeforeCore(t *testing.T) {
	p, messages := newAllowFromTestPlatform(t, "ou_allowed")
	event := cardActionEvent("ou_allowed", "askq:cTE:0:2")
	event.Event.Action.Value["askq_label"] = "Square"
	event.Event.Action.Value["askq_question"] = "Shape?"
	response, err := p.onCardAction(event)
	if err != nil {
		t.Fatal(err)
	}
	if response != nil && response.Card != nil {
		t.Fatal("optimistic card replacement before validation")
	}
	msg := waitAnyMessage(t, messages, time.Second, "question callback")
	if msg.OnAnswerStatus == nil {
		t.Fatal("missing exact-card feedback callback")
	}
}

func TestOnCardAction_SynchronousQuestionPreservesExistingFeedback(t *testing.T) {
	p, messages := newAllowFromTestPlatform(t, "ou_allowed")
	event := cardActionEvent("ou_allowed", "askq:0:2")
	event.Event.Action.Value["askq_label"] = "Square"
	response, err := p.onCardAction(event)
	if err != nil || response == nil || response.Card == nil {
		t.Fatal("legacy synchronous callback changed", err)
	}
	msg := waitAnyMessage(t, messages, time.Second, "legacy question")
	if msg.OnAnswerStatus != nil {
		t.Fatal("legacy callback claimed async lifecycle")
	}
}
