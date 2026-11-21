package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// cardActionEventWithKey mirrors cardActionEvent but also carries the
// session_key the sender injected into the button value — the routing key
// that made every group member's click land in the requester's session.
func cardActionEventWithKey(openID, action, sessionKey string) *callback.CardActionTriggerEvent {
	value := map[string]any{"action": action}
	if sessionKey != "" {
		value["session_key"] = sessionKey
	}
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID},
			Action:   &callback.CallBackAction{Value: value},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	}
}

func TestRequesterFromSessionKey(t *testing.T) {
	cases := []struct {
		sessionKey string
		wantID     string
		wantOK     bool
	}{
		// Per-user session key: identity segment IS the requester open_id.
		{"feishu:oc_chat:ou_owner", "ou_owner", true},
		{"lark:oc_chat:ou_owner", "ou_owner", true},
		// Union-id sessions are equally identifiable.
		{"feishu:oc_chat:on_owner", "on_owner", true},
		// Thread-isolated and shared-channel keys encode no single requester.
		{"feishu:oc_chat:root:om_root", "", false},
		{"feishu:oc_chat:thread:omt_x", "", false},
		{"feishu:oc_chat", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		id, ok := requesterFromSessionKey(c.sessionKey)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("requesterFromSessionKey(%q) = %q,%v want %q,%v", c.sessionKey, id, ok, c.wantID, c.wantOK)
		}
	}
}

// Non-requester group members clicking an askq option must be rejected:
// no dispatch into the requester's session, no answer consumed. The clicker
// only gets a toast; the original card stays.
func TestOnCardAction_NonRequesterAskqClickRejected(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")

	resp, err := ip.onCardAction(cardActionEventWithKey("ou_intruder", "askq:0:1", "feishu:oc_test_chat:ou_owner"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil {
		t.Fatal("response = nil, want toast-only rejection")
	}
	if resp.Card != nil {
		t.Errorf("replacement Card = %#v, want nil (clicker must not replace the requester's card)", resp.Card)
	}
	if resp.Toast == nil || !strings.Contains(resp.Toast.Content, "提问者") {
		t.Errorf("Toast = %#v, want requester-only notice", resp.Toast)
	}
	waitNoMessage(t, msgCh, 150*time.Millisecond, "non-requester askq click")
}

// The requester's own click keeps working end to end: dispatch reaches the
// engine and the callback returns the replacement card. Regression for the
// "button not greyed out after click" report: the replacement card the
// clicker gets back must carry no live buttons.
func TestOnCardAction_RequesterAskqClickDispatchesWithButtonlessCard(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")

	resp, err := ip.onCardAction(cardActionEventWithKey("ou_owner", "askq:0:1", "feishu:oc_test_chat:ou_owner"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	msg := waitAnyMessage(t, msgCh, 3*time.Second, "requester askq click")
	if msg.Content != "askq:0:1" {
		t.Errorf("dispatched content = %q, want %q", msg.Content, "askq:0:1")
	}
	if msg.SessionKey != "feishu:oc_test_chat:ou_owner" {
		t.Errorf("dispatched session key = %q, want requester session", msg.SessionKey)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("response card = nil, want replacement card")
	}
	raw, err := json.Marshal(resp.Card.Data)
	if err != nil {
		t.Fatalf("marshal card data: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `"tag":"button"`) {
		t.Errorf("replacement card still has interactive buttons: %s", s)
	}
	if !strings.Contains(s, "✅") {
		t.Errorf("replacement card missing answered marker: %s", s)
	}
}

// Same hole guarded on the permission card: a stranger clicking "Allow"
// must not feed an allow into the requester's pending permission.
func TestOnCardAction_NonRequesterPermClickRejected(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")

	resp, err := ip.onCardAction(cardActionEventWithKey("ou_intruder", "perm:allow", "feishu:oc_test_chat:ou_owner"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card != nil {
		t.Errorf("response = %#v, want toast-only rejection", resp)
	}
	if resp.Toast == nil {
		t.Fatal("Toast = nil, want requester-only notice")
	}
	waitNoMessage(t, msgCh, 150*time.Millisecond, "non-requester perm click")
}

func TestOnCardAction_RequesterPermClickDispatches(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")

	resp, err := ip.onCardAction(cardActionEventWithKey("ou_owner", "perm:allow", "feishu:oc_test_chat:ou_owner"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	msg := waitAnyMessage(t, msgCh, 3*time.Second, "requester perm click")
	if msg.Content != "allow" {
		t.Errorf("dispatched content = %q, want %q", msg.Content, "allow")
	}
	if !msg.IsPermissionResponse {
		t.Error("IsPermissionResponse = false, want true")
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("response card = nil, want replacement card")
	}
}

// Thread-isolated sessions are shared by design: the key carries no single
// requester, so the gate must stay open there (click routes as before).
func TestOnCardAction_ThreadSessionAskqNotGated(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")

	_, err := ip.onCardAction(cardActionEventWithKey("ou_teammate", "askq:0:1", "feishu:oc_test_chat:root:om_root"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitAnyMessage(t, msgCh, 3*time.Second, "thread-session askq click")
}

// The requester-only toast follows the engine's language once a localizer is
// set, instead of the hard-coded bilingual fallback (#2303).
func TestOnCardAction_NonRequesterToastIsLocalized(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")
	i18n := core.NewI18n(core.LangJapanese)
	ip.SetLocalizer(i18n.T)

	resp, err := ip.onCardAction(cardActionEventWithKey("ou_intruder", "askq:0:1", "feishu:oc_test_chat:ou_owner"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	want := i18n.T(core.MsgCardRequesterOnly)
	if resp == nil || resp.Toast == nil || resp.Toast.Content != want {
		t.Fatalf("Toast = %#v, want %q", resp, want)
	}
	waitNoMessage(t, msgCh, 150*time.Millisecond, "non-requester askq click")
}
