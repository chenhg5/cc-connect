package feishu

import (
	"testing"
	"time"

	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// Both ways of answering inside the card (typing in its box, tapping an
// option) must be marked as card answers. The engine only accepts marked
// answers for a question sent as this card, so an unmarked one would leave
// the question stuck.
func TestOnCardActionAskqAnswersAreMarkedAsCardAnswers(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)

	optionTap := func(token string) *larkcallback.CardActionTriggerEvent {
		return &larkcallback.CardActionTriggerEvent{
			Event: &larkcallback.CardActionTriggerRequest{
				Operator: &larkcallback.Operator{OpenID: "ou_test_user"},
				Action: &larkcallback.CallBackAction{
					Value: map[string]any{"action": "askq:0:2", "askq_token": token, "askq_label": "蓝绿"},
				},
				Context: &larkcallback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
			},
		}
	}

	cases := []struct {
		name  string
		event func(token string) *larkcallback.CardActionTriggerEvent
	}{
		{"typed answer", func(token string) *larkcallback.CardActionTriggerEvent {
			return askqTextEvent(token, "滚动发布即可", "oc_chat")
		}},
		{"option tap", optionTap},
	}
	for _, c := range cases {
		// Mint per case: a new card for the session supersedes the previous one.
		name, ev := c.name, c.event(mintAskq(ip, "feishu:oc_chat:ou_test_user", "oc_chat", 0))
		if _, err := ip.onCardAction(ev); err != nil {
			t.Fatalf("%s: onCardAction() error = %v", name, err)
		}
		select {
		case msg := <-msgCh:
			if !msg.IsAskQuestionAnswer {
				t.Fatalf("%s: dispatched message is not marked as a card answer", name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: expected the answer to be dispatched", name)
		}
	}
}
