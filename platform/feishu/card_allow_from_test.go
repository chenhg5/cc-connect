package feishu

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// newAllowFromTestPlatform constructs an interactivePlatform with the given
// allowFrom whitelist and a handler channel that lets the test assert whether
// a card action triggered a downstream message dispatch.
func newAllowFromTestPlatform(t *testing.T, allowFrom string) (*interactivePlatform, chan *core.Message) {
	t.Helper()
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"allow_from":         allowFrom,
		"enable_feishu_card": true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}
	msgCh := make(chan *core.Message, 4)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}
	return ip, msgCh
}

// waitNoMessage fails the test if the handler channel receives any message
// within the given timeout. Used to assert that an unauthorized card action
// was silently dropped before reaching dispatchCoreMessage.
func waitNoMessage(t *testing.T, ch <-chan *core.Message, timeout time.Duration, prefix string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("%s: expected no dispatched message, got %+v", prefix, msg)
	case <-time.After(timeout):
		// ok — handler was not invoked
	}
}

// waitAnyMessage fails the test if no message reaches the handler within the
// given timeout.
func waitAnyMessage(t *testing.T, ch <-chan *core.Message, timeout time.Duration, prefix string) *core.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatalf("%s: expected dispatched message, got none", prefix)
		return nil
	}
}

// cardActionEvent builds a CardActionTriggerEvent with the given operator and
// action value, using a consistent chat/message context.
func cardActionEvent(openID, action string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": action}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	}
}

// TestOnCardAction_AllowFromBlocksCmdAction asserts that a `cmd:` button
// click from a user not in allow_from does NOT dispatch to the agent.
func TestOnCardAction_AllowFromBlocksCmdAction(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "cmd:/help"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "cmd:/help from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksPermAllow asserts that a `perm:allow`
// button click from a user not in allow_from does NOT dispatch.
func TestOnCardAction_AllowFromBlocksPermAllow(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "perm:allow"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "perm:allow from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksPermDeny asserts that a `perm:deny` button
// click from a user not in allow_from does NOT dispatch.
func TestOnCardAction_AllowFromBlocksPermDeny(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "perm:deny"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "perm:deny from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksPermAllowAll asserts that `perm:allow_all`
// (the most dangerous: blanket-approves ALL pending dangerous actions) is
// also blocked when the clicker is not in allow_from.
func TestOnCardAction_AllowFromBlocksPermAllowAll(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "perm:allow_all"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "perm:allow_all from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksAskq asserts that `askq:` button clicks
// (AskUserQuestion option selection forwarded as user message) are blocked
// for unauthorized users.
func TestOnCardAction_AllowFromBlocksAskq(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "askq:option_a"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "askq: from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksNavAction asserts that `nav:` (which calls
// cardNavHandler to render a card update) is also gated by allow_from so
// unauthorized users cannot navigate the bot's card flow.
func TestOnCardAction_AllowFromBlocksNavAction(t *testing.T) {
	ip, _ := newAllowFromTestPlatform(t, "ou_allowed")
	navCalled := make(chan struct{}, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		select {
		case navCalled <- struct{}{}:
		default:
		}
		return nil
	}
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "nav:/foo"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	select {
	case <-navCalled:
		t.Fatal("nav:/foo from unauthorized user should not invoke cardNavHandler")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

// TestOnCardAction_AllowFromBlocksActAction asserts that `act:` is gated by
// allow_from. The toast short-circuit for "act:/delete-mode toggle" runs only
// after the user check, so we use a different act: value to ensure the
// nav/act branch is exercised.
func TestOnCardAction_AllowFromBlocksActAction(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	// Use a non-toggle act: prefix so the function falls through to the
	// nav-handler branch. We expect no dispatch either via cardNavHandler
	// (which would not call our handler, but might still produce a card) or
	// via dispatchCoreMessage. The clearest signal is the absence of a
	// dispatched message; nav-handler activity is asserted separately.
	_, err := ip.onCardAction(cardActionEvent("ou_attacker", "act:/foo"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "act:/foo from unauthorized user")
}

// TestOnCardAction_AllowFromBlocksEmptyOperator asserts that a card action
// with no Operator (so userID is empty) is rejected even when allow_from is
// empty/allow-all — failing closed is the correct behavior since we cannot
// know who clicked.
func TestOnCardAction_AllowFromBlocksEmptyOperator(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")
	ev := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: nil,
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "cmd:/help"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	}
	_, err := ip.onCardAction(ev)
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "cmd:/help with no Operator (empty userID)")
}

// TestOnCardAction_AllowFromAllowsAuthorizedCmd asserts the positive case:
// a user in allow_from CAN click cmd: and have it dispatch.
func TestOnCardAction_AllowFromAllowsAuthorizedCmd(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed,ou_other")
	_, err := ip.onCardAction(cardActionEvent("ou_allowed", "cmd:/help"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	msg := waitAnyMessage(t, msgCh, 2*time.Second, "cmd:/help from authorized user")
	if msg.Content != "/help" {
		t.Fatalf("dispatched content = %q, want %q", msg.Content, "/help")
	}
	if msg.UserID != "ou_allowed" {
		t.Fatalf("dispatched UserID = %q, want %q", msg.UserID, "ou_allowed")
	}
}

// TestOnCardAction_AllowFromAllowsAuthorizedPermAllow asserts the positive
// case for the most dangerous action: perm:allow from an authorized user
// dispatches as a permission response.
func TestOnCardAction_AllowFromAllowsAuthorizedPermAllow(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_allowed")
	_, err := ip.onCardAction(cardActionEvent("ou_allowed", "perm:allow"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	msg := waitAnyMessage(t, msgCh, 2*time.Second, "perm:allow from authorized user")
	if !msg.IsPermissionResponse {
		t.Fatalf("dispatched IsPermissionResponse = false, want true")
	}
	if msg.Content != "allow" {
		t.Fatalf("dispatched content = %q, want %q", msg.Content, "allow")
	}
}

// TestOnCardAction_AllowFromWildcardAllows asserts that the existing
// allow-all default (allow_from == "" or "*") continues to work — the fix
// must NOT regress deployments that rely on the wildcard.
func TestOnCardAction_AllowFromWildcardAllows(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "*")
	_, err := ip.onCardAction(cardActionEvent("ou_anyone", "cmd:/help"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	msg := waitAnyMessage(t, msgCh, 2*time.Second, "cmd:/help with wildcard allow_from")
	if msg.Content != "/help" {
		t.Fatalf("dispatched content = %q, want %q", msg.Content, "/help")
	}
}

// TestOnCardAction_AllowFromMultiListRespectsExactMember asserts that with
// a comma-separated allow_from list, only exact-listed members can dispatch,
// not arbitrary other users.
func TestOnCardAction_AllowFromMultiListRespectsExactMember(t *testing.T) {
	ip, msgCh := newAllowFromTestPlatform(t, "ou_a, ou_b , ou_c")
	// ou_b is in the list → allowed
	if _, err := ip.onCardAction(cardActionEvent("ou_b", "cmd:/foo")); err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if msg := waitAnyMessage(t, msgCh, 2*time.Second, "cmd:/foo from ou_b (listed)"); msg.Content != "/foo" {
		t.Fatalf("dispatched content = %q, want %q", msg.Content, "/foo")
	}
	// ou_d is NOT in the list → blocked
	if _, err := ip.onCardAction(cardActionEvent("ou_d", "cmd:/bar")); err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	waitNoMessage(t, msgCh, 200*time.Millisecond, "cmd:/bar from ou_d (not listed)")
}
