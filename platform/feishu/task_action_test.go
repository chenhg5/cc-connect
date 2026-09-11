package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func taskActionPlatform(t *testing.T) *interactivePlatform {
	t.Helper()
	p, err := New(map[string]any{"app_id": "task-" + t.Name(), "app_secret": "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*interactivePlatform)
}

func taskActionEvent(action string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_owner"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_queue_card"},
		Action: &callback.CallBackAction{Value: map[string]any{
			"action": action, "session_key": "feishu:oc_chat:root:om_root", "lang": "en",
		}},
	}}
}

func TestTaskCardAction_PreservesAuthenticatedOperatorAndOriginalCard(t *testing.T) {
	for _, action := range []string{"steer:opaque", "unqueue:opaque"} {
		t.Run(action, func(t *testing.T) {
			p := taskActionPlatform(t)
			p.handler = func(core.Platform, *core.Message) {
				t.Error("task action reached ordinary message or permission handler")
			}
			var received core.CardTaskAction
			p.SetCardTaskActionHandler(func(_ context.Context, request core.CardTaskAction) core.CardTaskActionResult {
				received = request
				return core.CardTaskActionResult{Card: core.NewCard().Markdown("Accepted").Build(), Toast: "Accepted", ToastType: "success"}
			})
			event := taskActionEvent(action)
			// Untrusted card values must not replace the authenticated operator.
			event.Event.Action.Value["user_id"] = "ou_forged"
			resp, err := p.onCardAction(event)
			if err != nil {
				t.Fatal(err)
			}
			if received.Action != action || received.UserID != "ou_owner" || received.ChatID != "oc_chat" ||
				received.MessageID != "om_queue_card" || received.SessionKey != "feishu:oc_chat:root:om_root" {
				t.Fatalf("incorrect task request: %#v", received)
			}
			rctx, ok := received.ReplyCtx.(replyContext)
			if !ok || rctx.messageID != "om_queue_card" || rctx.chatID != "oc_chat" || rctx.sessionKey != received.SessionKey {
				t.Fatalf("incorrect reply context: %#v", received.ReplyCtx)
			}
			if resp == nil || resp.Card == nil || resp.Toast == nil || resp.Toast.Type != "success" {
				t.Fatalf("expected actual handler result, got %#v", resp)
			}
		})
	}
}

func TestTaskCardAction_RejectsMissingIdentityAndDisallowedOperator(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		change func(*Platform, *callback.CardActionTriggerEvent)
	}{
		{"operator", func(_ *Platform, e *callback.CardActionTriggerEvent) { e.Event.Operator = nil }},
		{"context", func(_ *Platform, e *callback.CardActionTriggerEvent) { e.Event.Context = nil }},
		{"chat", func(_ *Platform, e *callback.CardActionTriggerEvent) { e.Event.Context.OpenChatID = "" }},
		{"message", func(_ *Platform, e *callback.CardActionTriggerEvent) { e.Event.Context.OpenMessageID = "" }},
		{"allow_from", func(p *Platform, _ *callback.CardActionTriggerEvent) { p.allowFrom = "ou_other" }},
		{"allow_chat", func(p *Platform, _ *callback.CardActionTriggerEvent) { p.allowChat = "oc_other" }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			p := taskActionPlatform(t)
			p.SetCardTaskActionHandler(func(context.Context, core.CardTaskAction) core.CardTaskActionResult {
				t.Error("invalid callback reached core")
				return core.CardTaskActionResult{}
			})
			e := taskActionEvent("steer:opaque")
			fixture.change(p.Platform, e)
			resp, err := p.onCardAction(e)
			if err != nil {
				t.Fatal(err)
			}
			if resp != nil && (resp.Card != nil || resp.Toast == nil || resp.Toast.Type != "error") {
				t.Fatalf("unexpected invalid callback response: %#v", resp)
			}
		})
	}
}

func TestTaskCardAction_SlowResultPatchesExactCardAndNeverClaimsEarlySuccess(t *testing.T) {
	p := taskActionPlatform(t)
	requests := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/auth/") {
			_, err := fmt.Fprint(w, `{"code":0,"expire":7200,"tenant_access_token":"test-token"}`)
			if err != nil {
				t.Error(err)
			}
			return
		}
		if r.Method != http.MethodPatch {
			t.Errorf("method=%s, want PATCH", r.Method)
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(body.Content, "Final result") {
			t.Errorf("wrong patched card: %s", body.Content)
		}
		_, err := fmt.Fprint(w, `{"code":0,"data":{}}`)
		if err != nil {
			t.Error(err)
		}
		requests <- r.URL.Path
	}))
	defer srv.Close()
	p.client = lark.NewClient("slow-action", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	release := make(chan struct{})
	p.SetCardTaskActionHandler(func(context.Context, core.CardTaskAction) core.CardTaskActionResult {
		<-release
		return core.CardTaskActionResult{Card: core.NewCard().Markdown("Final result").Build(), Toast: "Accepted", ToastType: "success"}
	})
	resp, err := p.onCardAction(taskActionEvent("steer:opaque"))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Card != nil || resp.Toast == nil || resp.Toast.Type != "info" || resp.Toast.Content != core.NewI18n(core.LangEnglish).T(core.MsgSteerSubmitting) {
		t.Fatalf("expected pending response, got %#v", resp)
	}
	p.cardActionMsgMu.Lock()
	p.cardActionMsgIDs = map[string]string{"feishu:oc_chat:root:om_root": "om_other_card"}
	p.cardActionMsgMu.Unlock()
	close(release)
	select {
	case path := <-requests:
		if path != "/open-apis/im/v1/messages/om_queue_card" {
			t.Fatalf("patched wrong card: %s", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no card patch after task result")
	}
}

func TestTaskCardAction_RenderedButtonRoundTripsSessionAndLanguage(t *testing.T) {
	card := core.NewCard().Buttons(core.CardButton{Text: "Steer", Value: "steer:opaque", Extra: map[string]string{"lang": "ja"}}).Build()
	var rendered map[string]any
	if err := json.Unmarshal([]byte(renderCard(card, "feishu:oc_chat:root:om_root")), &rendered); err != nil {
		t.Fatal(err)
	}
	element := rendered["elements"].([]any)[0].(map[string]any)
	button := element["actions"].([]any)[0].(map[string]any)
	value := button["value"].(map[string]any)
	if value["action"] != "steer:opaque" || value["session_key"] != "feishu:oc_chat:root:om_root" || value["lang"] != "ja" {
		t.Fatalf("callback values lost: %#v", value)
	}
}

func TestTaskCardAction_DisabledCardsDoNotAdvertiseTaskActions(t *testing.T) {
	p, err := New(map[string]any{"app_id": "task-disabled", "app_secret": "test-secret", "enable_feishu_card": false})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(core.CardTaskActionHandlerSetter); ok {
		t.Fatal("disabled cards advertised task actions")
	}
}

func TestTaskCardAction_DenialDoesNotRenderOptimisticSuccess(t *testing.T) {
	p := taskActionPlatform(t)
	p.SetCardTaskActionHandler(func(context.Context, core.CardTaskAction) core.CardTaskActionResult {
		return core.CardTaskActionResult{Toast: "Only the original sender may do this", ToastType: "error"}
	})
	response, err := p.onCardAction(taskActionEvent("steer:opaque"))
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Card != nil || response.Toast == nil || response.Toast.Type != "error" {
		t.Fatalf("denied action must preserve card and return failure: %#v", response)
	}
}

func TestTaskCardAction_DispatchNormalizesChannelIDAndPreservesExplicitValue(t *testing.T) {
	for _, channelID := range []string{"", "oc_explicit"} {
		t.Run("channel_"+channelID, func(t *testing.T) {
			p := taskActionPlatform(t)
			var received *core.Message
			p.handler = func(actual core.Platform, msg *core.Message) {
				if actual != p {
					t.Errorf("dispatch platform = %T %p, want wrapped platform %p", actual, actual, p)
				}
				received = msg
			}
			p.dispatchCoreMessage(&core.Message{ChannelID: channelID, ReplyCtx: replyContext{chatID: "oc_context"}})
			want := channelID
			if want == "" {
				want = "oc_context"
			}
			if received == nil || received.ChannelID != want {
				t.Fatalf("dispatched message = %#v, want channel %s", received, want)
			}
		})
	}
}
