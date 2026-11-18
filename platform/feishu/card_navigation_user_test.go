package feishu

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestCardAction_NavigationCarriesClickerForSharedSessions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		shared      bool
		thread      bool
		sessionKey  string
		callbackKey string
		clicker     string
	}{
		{"per-user", false, false, "feishu:oc_chat:ou_member", "", "ou_member"},
		{"shared", true, false, "feishu:oc_chat", "", "ou_member"},
		{"thread", false, true, "feishu:oc_chat:root:om_root", "feishu:oc_chat:root:om_root", "ou_member"},
		{"admin-card-member-clicker", false, false, "feishu:oc_chat:ou_admin", "feishu:oc_chat:ou_admin", "ou_member"},
		{"member-card-admin-clicker", false, false, "feishu:oc_chat:ou_member", "feishu:oc_chat:ou_member", "ou_admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No SDK client or constructor: navigation is fully in-memory.
			p := &Platform{platformName: "feishu", shareSessionInChannel: tc.shared, threadIsolation: tc.thread}
			for _, action := range []string{"nav:/help agent", "act:/provider b", "act:/delete-mode submit"} {
				var gotAction, gotSessionKey, gotUserID string
				p.SetUserAwareCardNavigationHandler(func(action, sessionKey, userID string) *core.Card {
					gotAction, gotSessionKey, gotUserID = action, sessionKey, userID
					return core.NewCard().Markdown("result").Build()
				})
				event := &callback.CardActionTriggerEvent{
					Event: &callback.CardActionTriggerRequest{
						Operator: &callback.Operator{OpenID: tc.clicker},
						Action: &callback.CallBackAction{Value: map[string]any{
							"action": action, "session_key": tc.callbackKey,
						}},
						Context: &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
					},
				}
				resp, err := p.onCardAction(event)
				if err != nil || resp == nil || resp.Card == nil {
					t.Fatalf("onCardAction(%q) = %#v, %v; want card", action, resp, err)
				}
				if gotAction != action || gotSessionKey != tc.sessionKey || gotUserID != tc.clicker {
					t.Fatalf("navigation = (%q, %q, %q), want (%q, %q, %q)", gotAction, gotSessionKey, gotUserID, action, tc.sessionKey, tc.clicker)
				}
				// The original callback API remains usable and replaces the handler.
				gotAction, gotSessionKey = "", ""
				p.SetCardNavigationHandler(func(action, sessionKey string) *core.Card {
					gotAction, gotSessionKey = action, sessionKey
					return core.NewCard().Markdown("legacy result").Build()
				})
				resp, err = p.onCardAction(event)
				if err != nil || resp == nil || resp.Card == nil || gotAction != action || gotSessionKey != tc.sessionKey {
					t.Fatalf("legacy navigation = (%q, %q), response %#v, error %v", gotAction, gotSessionKey, resp, err)
				}
			}
		})
	}
}

func TestCardAction_NavigationWithoutOperatorFailsClosed(t *testing.T) {
	for _, operator := range []*callback.Operator{nil, {OpenID: ""}} {
		for _, key := range []string{"feishu:oc_chat:ou_admin", "feishu:oc_chat", "feishu:oc_chat:root:om_root"} {
			p := &Platform{platformName: "feishu"}
			called := false
			p.SetUserAwareCardNavigationHandler(func(action, sessionKey, userID string) *core.Card {
				called = true
				return core.NewCard().Markdown("must not run").Build()
			})
			event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
				Operator: operator,
				Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/provider b", "session_key": key}},
				Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"},
			}}
			resp, err := p.onCardAction(event)
			if err != nil || resp != nil || called || len(p.cardActionMsgIDs) != 0 {
				t.Fatalf("missing operator borrowed card owner for %q: response=%#v err=%v called=%v", key, resp, err, called)
			}
		}
	}
}
