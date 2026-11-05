package feishu

import (
	"testing"
	"time"
)

func TestPermissionCardCallback_PreservesRequestBinding(t *testing.T) {
	for _, action := range []string{"allow", "deny", "allow_all"} {
		t.Run(action, func(t *testing.T) {
			p, messages := newAllowFromTestPlatform(t, "ou_allowed")
			p.userNameCache.Store("ou_allowed", "Alice")
			p.chatNameCache.Store("oc_test_chat", "Test")
			event := cardActionEvent("ou_allowed", "perm:"+action)
			event.Event.Action.Value["perm_request_id"] = "prompt-1"
			response, err := p.onCardAction(event)
			if err != nil {
				t.Fatal(err)
			}
			msg := waitAnyMessage(t, messages, time.Second, action)
			if !msg.IsPermissionResponse || msg.Content != "perm:prompt-1:"+action {
				t.Fatalf("permission callback = %+v, want request-bound decision", msg)
			}
			if response != nil && response.Card != nil {
				t.Fatal("callback must not display success before the engine accepts the decision")
			}
		})
	}
}

func TestPermissionCardCallback_MissingRequestBindingIsIgnored(t *testing.T) {
	p, messages := newAllowFromTestPlatform(t, "ou_allowed")
	event := cardActionEvent("ou_allowed", "perm:allow_all")
	delete(event.Event.Action.Value, "perm_request_id")
	response, err := p.onCardAction(event)
	if err != nil {
		t.Fatal(err)
	}
	if response != nil && response.Card != nil {
		t.Fatal("an unbound legacy card must not display approval success")
	}
	waitNoMessage(t, messages, 100*time.Millisecond, "unbound legacy permission card")
}
