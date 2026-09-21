package feishu

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// TestInteractivePlatform_CardActionRespectsAllowFrom ensures that
// onCardAction() enforces the same per-user allow_from allowlist that the
// plain text-message handler enforces. allow_from is documented (and
// enforced elsewhere in this file) as controlling "who may trigger an agent
// turn"; a card action dispatch (cmd:, perm:, askq:) must not be able to
// bypass it just because it arrives via an interactive card click instead
// of a typed message.
func TestInteractivePlatform_CardActionRespectsAllowFrom(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"allow_from":         "ou_authorized_user",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(p core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	// An operator NOT in allow_from clicks a "cmd:" card button.
	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_unauthorized_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "cmd:/whoami"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		t.Fatalf("card action from a user outside allow_from must not be dispatched, got message: %+v", msg)
	case <-time.After(300 * time.Millisecond):
		// expected: nothing dispatched
	}
}
