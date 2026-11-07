package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// Exercise the actual Feishu serializer and SDK callback with queued-message
// actions. The conversation and authenticated operator must survive the card.
func TestInteractivePlatform_QueuedSteeringCallbacks(t *testing.T) {
	for _, action := range []string{"cmd:/ps --queued queue-one", "cmd:/ps --keep queue-one"} {
		t.Run(action, func(t *testing.T) {
			platform, err := New(map[string]any{"app_id": "cli_test", "app_secret": "test", "thread_isolation": true})
			if err != nil {
				t.Fatal(err)
			}
			p := platform.(*interactivePlatform)
			p.userNameCache.Store("ou_user", "User")
			p.chatNameCache.Store("oc_chat", "Chat")
			key := "feishu:oc_chat:root:om_thread"
			card := core.NewCard().Title("Choose", "blue").Buttons(core.PrimaryBtn("Option", action)).Build()
			rendered := renderCardMap(card, key)
			wire, err := json.Marshal(rendered)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &rendered); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			for _, raw := range rendered["elements"].([]any) {
				element := raw.(map[string]any)
				if element["tag"] == "action" {
					value = element["actions"].([]any)[0].(map[string]any)["value"].(map[string]any)
				}
			}
			if value == nil || value["action"] != action || value["session_key"] != key {
				t.Fatalf("button callback lost identity: %#v", value)
			}
			messages := make(chan *core.Message, 1)
			p.handler = func(platform core.Platform, msg *core.Message) {
				if _, ok := platform.(core.CardSender); !ok {
					t.Error("callback lost card capability")
				}
				messages <- msg
			}
			_, err = p.onCardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "ou_user"},
				Action:   &callback.CallBackAction{Value: value},
				Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_question"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-messages:
				if message.Content != strings.TrimPrefix(action, "cmd:") || message.SessionKey != key || message.UserID != "ou_user" {
					t.Fatalf("incorrect callback delivery: %#v", message)
				}
				rc, ok := message.ReplyCtx.(replyContext)
				if !ok || !rc.cardAction || rc.messageID != "om_question" || rc.sessionKey != key {
					t.Fatalf("callback lost the specific clicked card: %#v", message.ReplyCtx)
				}
			case <-time.After(time.Second):
				t.Fatal("button did not dispatch to the engine")
			}
		})
	}
}
