package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestNew_DefaultsToInteractivePlatform(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := p.(core.CardSender); !ok {
		t.Fatal("expected default Feishu platform to implement core.CardSender")
	}
}

func TestNew_CanDisableInteractiveCards(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := p.(core.CardSender); ok {
		t.Fatal("expected disabled Feishu platform to fall back to plain text")
	}
}

func TestNew_DisabledInteractiveCardsDoesNotStartPreviewCard(t *testing.T) {
	pAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p, ok := pAny.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *Platform", pAny)
	}

	_, err = p.SendPreviewStart(context.Background(), replyContext{messageID: "om_x", chatID: "oc_x"}, "hello")
	if err == nil {
		t.Fatal("SendPreviewStart() error = nil, want not supported when cards are disabled")
	}
	if err != core.ErrNotSupported {
		t.Fatalf("SendPreviewStart() error = %v, want %v", err, core.ErrNotSupported)
	}
}

func TestNew_ProgressStyleDefaultLegacy(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sp, ok := p.(core.ProgressStyleProvider)
	if !ok {
		t.Fatalf("platform type %T does not implement ProgressStyleProvider", p)
	}
	if got := sp.ProgressStyle(); got != "legacy" {
		t.Fatalf("ProgressStyle() = %q, want legacy", got)
	}
}

func TestNew_ProgressStyleSupportsCompactAndCard(t *testing.T) {
	tests := []string{"compact", "card"}
	for _, style := range tests {
		t.Run(style, func(t *testing.T) {
			p, err := New(map[string]any{
				"app_id":         "cli_xxx",
				"app_secret":     "secret",
				"progress_style": style,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			sp, ok := p.(core.ProgressStyleProvider)
			if !ok {
				t.Fatalf("platform type %T does not implement ProgressStyleProvider", p)
			}
			if got := sp.ProgressStyle(); got != style {
				t.Fatalf("ProgressStyle() = %q, want %q", got, style)
			}
			payloadCap, ok := p.(core.ProgressCardPayloadSupport)
			if !ok {
				t.Fatalf("platform type %T does not implement ProgressCardPayloadSupport", p)
			}
			if !payloadCap.SupportsProgressCardPayload() {
				t.Fatal("SupportsProgressCardPayload() = false, want true")
			}
		})
	}
}

func TestNew_ProgressStyleRejectsInvalidValue(t *testing.T) {
	_, err := New(map[string]any{
		"app_id":         "cli_xxx",
		"app_secret":     "secret",
		"progress_style": "invalid-style",
	})
	if err == nil {
		t.Fatal("expected error for invalid progress_style")
	}
	if !strings.Contains(err.Error(), "invalid progress_style") {
		t.Fatalf("error = %q, want invalid progress_style", err.Error())
	}
}

func TestInteractivePlatform_OnMessagePassesCardSenderToHandler(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	messageID := "om_test_message"
	chatID := "oc_test_chat"
	openID := "ou_test_user"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"/help"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var (
		wg           sync.WaitGroup
		receivedPlat core.Platform
		receivedMsg  *core.Message
	)
	wg.Add(1)
	ip.handler = func(p core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedPlat = p
		receivedMsg = msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("expected handler to receive a message")
	}
	if receivedMsg.Content != "/help" {
		t.Fatalf("message content = %q, want /help", receivedMsg.Content)
	}
	if _, ok := receivedPlat.(core.CardSender); !ok {
		t.Fatalf("handler platform type = %T, want core.CardSender", receivedPlat)
	}
}

func TestInteractivePlatform_CardActionPassesCardSenderToHandler(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	openID := "ou_test_user"
	chatID := "oc_test_chat"
	messageID := "om_test_message"
	action := "cmd:/help"

	var (
		msgCh  = make(chan *core.Message, 1)
		platCh = make(chan core.Platform, 1)
	)
	ip.handler = func(p core.Platform, msg *core.Message) {
		platCh <- p
		msgCh <- msg
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": action}},
			Context:  &callback.Context{OpenChatID: chatID, OpenMessageID: messageID},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case receivedPlat := <-platCh:
		if _, ok := receivedPlat.(core.CardSender); !ok {
			t.Fatalf("handler platform type = %T, want core.CardSender", receivedPlat)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action handler invocation")
	}

	select {
	case receivedMsg := <-msgCh:
		if receivedMsg.Content != "/help" {
			t.Fatalf("message content = %q, want /help", receivedMsg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action message")
	}
}

func TestInteractivePlatform_CardActionActWithoutCardResponseDoesNotWarn(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		return nil
	}

	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/delete-mode toggle session-1"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response for silent toggle, got %#v", resp)
	}
	if resp.Card != nil {
		t.Fatalf("expected no card update on toggle, got %#v", resp.Card)
	}

	logs := buf.String()
	if strings.Contains(logs, "level=WARN") && strings.Contains(logs, "card nav returned nil, ignoring") {
		t.Fatalf("unexpected warning logs: %s", logs)
	}
}

func TestInteractivePlatform_CardActionFormSubmitPassesSelectedIDs(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{"action": "act:/delete-mode form-submit"},
				FormValue: map[string]any{
					deleteModeCheckerName("session-2"): true,
					deleteModeCheckerName("session-1"): true,
					deleteModeCheckerName("session-3"): false,
				},
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode form-submit session-1,session-2"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionFormSubmitUsesActionNameFallback(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Name: "delete_mode_submit",
				FormValue: map[string]any{
					deleteModeCheckerName("session-2"): true,
					deleteModeCheckerName("session-1"): true,
				},
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode form-submit session-1,session-2"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionFormCancelUsesActionNameFallback(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Name: "delete_mode_cancel",
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode cancel"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionUsesCallbackSessionKey(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true, "thread_isolation": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	wantSessionKey := "feishu:oc_test_chat:root:om_root_thread"
	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":      "cmd:/help",
				"session_key": wantSessionKey,
			}},
			Context: &callback.Context{
				OpenChatID:    "oc_test_chat",
				OpenMessageID: "om_any_card_message",
			},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != wantSessionKey {
			t.Fatalf("SessionKey = %q, want %q", msg.SessionKey, wantSessionKey)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action message")
	}
}

func TestInteractivePlatform_ModelCardActionReturnsCardUpdate(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	var gotAction, gotSessionKey string
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		gotAction = action
		gotSessionKey = sessionKey
		return core.NewCard().Markdown("switching").Build()
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/model switch 1"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card response, got %#v", resp)
	}
	if gotAction != "act:/model switch 1" {
		t.Fatalf("action = %q, want act:/model switch 1", gotAction)
	}
	if gotSessionKey == "" {
		t.Fatal("expected non-empty session key")
	}
	ip.cardActionMsgMu.Lock()
	tracked := ip.cardActionMsgIDs[gotSessionKey]
	ip.cardActionMsgMu.Unlock()
	if tracked != "om_test_message" {
		t.Fatalf("tracked message id = %q, want om_test_message", tracked)
	}
}

func TestNewLark_PlatformNameAndDomain(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret",
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	if p.Name() != "lark" {
		t.Fatalf("Name() = %q, want lark", p.Name())
	}
	ip, ok := p.(*interactivePlatform)
	if !ok {
		t.Fatalf("type = %T, want *interactivePlatform", p)
	}
	if ip.domain != lark.LarkBaseUrl {
		t.Fatalf("domain = %q, want %q", ip.domain, lark.LarkBaseUrl)
	}
}

func TestPlatformShouldUseWebhookMode(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		encryptKey string
		want       bool
	}{
		{name: "lark defaults to websocket", platform: "lark", want: false},
		{name: "lark webhook when encrypt key set", platform: "lark", encryptKey: "enc-key", want: true},
		{name: "feishu defaults to websocket", platform: "feishu", want: false},
		{name: "feishu webhook when encrypt key set", platform: "feishu", encryptKey: "enc-key", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Platform{platformName: tt.platform, encryptKey: tt.encryptKey}
			if got := p.shouldUseWebhookMode(); got != tt.want {
				t.Fatalf("shouldUseWebhookMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewFeishu_PlatformNameAndDomain(t *testing.T) {
	p, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Name() != "feishu" {
		t.Fatalf("Name() = %q, want feishu", p.Name())
	}
}

func TestNewFeishu_CustomDomainOverride(t *testing.T) {
	customDomain := "https://open.example.invalid"
	p, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "domain": customDomain,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := p.(*interactivePlatform)
	if !ok {
		t.Fatalf("type = %T, want *interactivePlatform", p)
	}
	if ip.domain != customDomain {
		t.Fatalf("domain = %q, want %q", ip.domain, customDomain)
	}
}

func TestNewFeishu_InvalidCustomDomain(t *testing.T) {
	_, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "domain": "://bad",
	})
	if err == nil {
		t.Fatal("expected invalid domain error")
	}
}

func TestLark_SessionKeyPrefix(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_test"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"hello"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var receivedMsg *core.Message
	var wg sync.WaitGroup
	wg.Add(1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	_ = ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	})
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("handler not called")
	}
	if !strings.HasPrefix(receivedMsg.SessionKey, "lark:") {
		t.Fatalf("SessionKey = %q, want lark: prefix", receivedMsg.SessionKey)
	}
	if receivedMsg.Platform != "lark" {
		t.Fatalf("Platform = %q, want lark", receivedMsg.Platform)
	}
}

func TestLark_ThreadIsolationUsesRootSessionKey(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true, "thread_isolation": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_reply"
	rootID := "om_root"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"@bot hello"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var receivedMsg *core.Message
	var wg sync.WaitGroup
	wg.Add(1)
	ip.botOpenID = "ou_bot"
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	_ = ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				RootId:      &rootID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
				Mentions: []*larkim.MentionEvent{
					{
						Key: stringPtr("@bot"),
						Id:  &larkim.UserId{OpenId: stringPtr("ou_bot")},
					},
				},
			},
		},
	})
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("handler not called")
	}
	if receivedMsg.SessionKey != "lark:oc_test:root:om_root" {
		t.Fatalf("SessionKey = %q, want lark:oc_test:root:om_root", receivedMsg.SessionKey)
	}
}

func TestLark_GroupReplyAllWithThreadIsolationUsesRootSessionKeyWithoutMention(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true,
		"group_reply_all": true, "thread_isolation": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_root"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"hello from group root"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	if err := ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case receivedMsg := <-msgCh:
		if receivedMsg.SessionKey != "lark:oc_test:root:om_root" {
			t.Fatalf("SessionKey = %q, want lark:oc_test:root:om_root", receivedMsg.SessionKey)
		}
		rc, ok := receivedMsg.ReplyCtx.(replyContext)
		if !ok {
			t.Fatalf("ReplyCtx type = %T, want replyContext", receivedMsg.ReplyCtx)
		}
		if rc.sessionKey != "lark:oc_test:root:om_root" {
			t.Fatalf("replyContext.sessionKey = %q, want lark:oc_test:root:om_root", rc.sessionKey)
		}
		if rc.messageID != "om_root" {
			t.Fatalf("replyContext.messageID = %q, want om_root", rc.messageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected group root message to be handled without mention")
	}
}

func TestBuildReplyMessageReqBody_SetsReplyInThreadFlag(t *testing.T) {
	tests := []struct {
		name          string
		platform      *Platform
		replyCtx      replyContext
		wantThreading bool
	}{
		{
			name:          "thread isolation enabled",
			platform:      &Platform{threadIsolation: true},
			replyCtx:      replyContext{messageID: "om_reply", sessionKey: "feishu:oc_chat:root:om_root"},
			wantThreading: true,
		},
		{
			name:          "thread isolation does not affect p2p session",
			platform:      &Platform{threadIsolation: true},
			replyCtx:      replyContext{messageID: "om_reply", sessionKey: "feishu:oc_chat:ou_user"},
			wantThreading: false,
		},
		{
			name:          "plain reply remains non-threaded",
			platform:      &Platform{},
			replyCtx:      replyContext{messageID: "om_reply"},
			wantThreading: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.platform.buildReplyMessageReqBody(tt.replyCtx, larkim.MsgTypeText, `{"text":"hello"}`)
			if body == nil {
				t.Fatal("Body = nil, want populated reply body")
			}
			if body.ReplyInThread == nil {
				if tt.wantThreading {
					t.Fatal("ReplyInThread = nil, want true")
				}
				return
			}
			if got := *body.ReplyInThread; got != tt.wantThreading {
				t.Fatalf("ReplyInThread = %v, want %v", got, tt.wantThreading)
			}
		})
	}
}

func TestLark_ReconstructReplyCtx(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	base := p.(*Platform)

	rctx, err := base.ReconstructReplyCtx("lark:oc_chat123:ou_user456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.chatID != "oc_chat123" {
		t.Fatalf("chatID = %q, want oc_chat123", rc.chatID)
	}

	rctx, err = base.ReconstructReplyCtx("lark:oc_chat123:root:om_root456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx(thread) error = %v", err)
	}
	rc = rctx.(replyContext)
	if rc.chatID != "oc_chat123" {
		t.Fatalf("thread chatID = %q, want oc_chat123", rc.chatID)
	}
	if rc.messageID != "om_root456" {
		t.Fatalf("thread messageID = %q, want om_root456", rc.messageID)
	}

	_, err = base.ReconstructReplyCtx("feishu:oc_chat:ou_user")
	if err == nil {
		t.Fatal("expected error for feishu-prefixed key on lark platform")
	}
}

func stringPtr(s string) *string { return &s }

func TestSanitizeMarkdownURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "http link kept",
			input: "see [docs](http://example.com)",
			want:  "see [docs](http://example.com)",
		},
		{
			name:  "https link kept",
			input: "see [docs](https://example.com/path)",
			want:  "see [docs](https://example.com/path)",
		},
		{
			name:  "file scheme removed",
			input: "open [file](file:///tmp/foo.txt)",
			want:  "open file (file:///tmp/foo.txt)",
		},
		{
			name:  "data scheme removed",
			input: "img [pic](data:image/png;base64,abc)",
			want:  "img pic (data:image/png;base64,abc)",
		},
		{
			name:  "mixed links",
			input: "[ok](https://x.com) and [bad](file:///etc/passwd)",
			want:  "[ok](https://x.com) and bad (file:///etc/passwd)",
		},
		{
			name:  "no links unchanged",
			input: "plain text without links",
			want:  "plain text without links",
		},
		{
			name:  "ftp scheme removed",
			input: "[dl](ftp://files.example.com/f.zip)",
			want:  "dl (ftp://files.example.com/f.zip)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMarkdownURLs(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeMarkdownURLs(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLark_ErrorMessagePrefix(t *testing.T) {
	_, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !strings.HasPrefix(err.Error(), "lark:") {
		t.Fatalf("error = %q, want lark: prefix", err.Error())
	}
}

func TestBuildPreviewCardJSON_ProgressPayloadUsesStructuredCard(t *testing.T) {
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "planning"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"},
	}, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)
	if payload == "" {
		t.Fatal("BuildProgressCardPayload returned empty payload")
	}

	cardJSON := buildPreviewCardJSON(payload)
	if strings.Contains(cardJSON, core.ProgressCardPayloadPrefix) {
		t.Fatalf("card JSON should not leak payload prefix, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "Codex · Running") {
		t.Fatalf("card JSON should contain progress title, got %q", cardJSON)
	}
	if strings.Contains(cardJSON, "\"tag\":\"note\"") {
		t.Fatalf("card JSON should not use deprecated note tag, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\"text_color\":\"grey\"") {
		t.Fatalf("card JSON should render thinking with grey style, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\\u003ctext_tag color='blue'\\u003eTool") {
		t.Fatalf("card JSON should include tool label, got %q", cardJSON)
	}

	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card JSON is invalid: %v", err)
	}
	header, ok := card["header"].(map[string]any)
	if !ok || header == nil {
		t.Fatalf("expected header in card json, got %#v", card["header"])
	}
}

func TestBuildPreviewCardJSON_NormalTextFallback(t *testing.T) {
	cardJSON := buildPreviewCardJSON("plain progress text")
	if strings.Contains(cardJSON, "cc-connect · 进度") {
		t.Fatalf("normal text should use default card template, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\"tag\":\"markdown\"") {
		t.Fatalf("default preview card should contain markdown element, got %q", cardJSON)
	}
}

func TestFormatProgressToolInput_TodoWrite(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantContains    []string
		notWantContains []string
	}{
		{
			name: "valid todos with all statuses",
			input: `{"todos": [
				{"content": "Task 1", "status": "completed", "activeForm": "Completing task 1"},
				{"content": "Task 2", "status": "in_progress", "activeForm": "Working on task 2"},
				{"content": "Task 3", "status": "pending", "activeForm": "Planning task 3"}
			]}`,
			wantContains:    []string{"✅", "🔄", "⏳", "Task 1", "Task 2", "Task 3", "Completing task 1", "Working on task 2"},
			notWantContains: []string{"```"},
		},
		{
			name:            "todos without activeForm",
			input:           `{"todos": [{"content": "Simple task", "status": "pending"}]}`,
			wantContains:    []string{"⏳", "Simple task"},
			notWantContains: []string{"(", ")"},
		},
		{
			name:         "invalid JSON falls back to default",
			input:        `not valid json`,
			wantContains: []string{"```text"},
		},
		{
			name:         "empty todos array",
			input:        `{"todos": []}`,
			wantContains: []string{"```text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatProgressToolInput("TodoWrite", tt.input)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q, got %q", want, result)
				}
			}
			for _, notWant := range tt.notWantContains {
				if strings.Contains(result, notWant) {
					t.Errorf("result should not contain %q, got %q", notWant, result)
				}
			}
		})
	}
}

func TestFormatProgressToolInput_OtherTools(t *testing.T) {
	// Non-TodoWrite tools should use default formatting
	result := formatProgressToolInput("Bash", "ls -la")
	if !strings.Contains(result, "```bash") {
		t.Errorf("Bash tool should use bash code block, got %q", result)
	}

	// TodoWrite with invalid JSON should fall back to text block
	result = formatProgressToolInput("TodoWrite", "not json")
	if !strings.Contains(result, "```text") {
		t.Errorf("TodoWrite with invalid JSON should fall back to text block, got %q", result)
	}
}

func TestAllowChat_FiltersGroupMessages(t *testing.T) {
	tests := []struct {
		name      string
		allowChat string
		chatID    string
		chatType  string
		wantPass  bool
	}{
		{"empty allow_chat permits all groups", "", "oc_abc", "group", true},
		{"wildcard permits all groups", "*", "oc_abc", "group", true},
		{"matching chat_id passes", "oc_abc", "oc_abc", "group", true},
		{"non-matching chat_id blocked", "oc_abc", "oc_xyz", "group", false},
		{"multiple chat_ids, match second", "oc_abc,oc_xyz", "oc_xyz", "group", true},
		{"multiple chat_ids, no match", "oc_abc,oc_def", "oc_xyz", "group", false},
		{"private chat bypasses allow_chat filter", "oc_abc", "oc_xyz", "p2p", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
				"app_id": "cli_xxx", "app_secret": "secret",
				"enable_feishu_card": true,
				"group_reply_all":   true,
				"allow_chat":        tt.allowChat,
			})
			if err != nil {
				t.Fatalf("newPlatform() error = %v", err)
			}
			ip := p.(*interactivePlatform)

			messageID := "om_test_" + tt.name
			openID := "ou_test"
			msgType := "text"
			senderType := "user"
			content := `{"text":"hello"}`
			createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)

			msgCh := make(chan *core.Message, 1)
			ip.handler = func(_ core.Platform, msg *core.Message) {
				msgCh <- msg
			}

			if err := ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
				Event: &larkim.P2MessageReceiveV1Data{
					Sender: &larkim.EventSender{
						SenderId:   &larkim.UserId{OpenId: &openID},
						SenderType: &senderType,
					},
					Message: &larkim.EventMessage{
						MessageId:   &messageID,
						ChatId:      &tt.chatID,
						ChatType:    &tt.chatType,
						MessageType: &msgType,
						Content:     &content,
						CreateTime:  &createTime,
					},
				},
			}); err != nil {
				t.Fatalf("onMessage() error = %v", err)
			}

			select {
			case <-msgCh:
				if !tt.wantPass {
					t.Fatal("expected message to be blocked by allow_chat, but it was delivered")
				}
			case <-time.After(2 * time.Second):
				if tt.wantPass {
					t.Fatal("expected message to pass allow_chat filter, but it was blocked")
				}
			}
		})
	}
}

// --- Mention resolution tests ---

func TestResolveMentions_ReplacesKnownMember(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan", "李四": "ou_lisi"},
		fetchedAt: time.Now(),
	})
	input := "巡检完成，@张三 @李四 请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, `<at user_id="ou_zhangsan">张三</at>`) {
		t.Fatalf("expected 张三 to be resolved, got %q", result)
	}
	if !strings.Contains(result, `<at user_id="ou_lisi">李四</at>`) {
		t.Fatalf("expected 李四 to be resolved, got %q", result)
	}
}

func TestResolveMentions_UnknownMemberKeptAsIs(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	input := "@不存在的人 请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if strings.Contains(result, "<at") {
		t.Fatalf("unknown member should not be replaced, got %q", result)
	}
}

func TestResolveMentions_LongestMatchFirst(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan", "张三丰": "ou_zhangsanfeng"},
		fetchedAt: time.Now(),
	})
	input := "@张三丰请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "ou_zhangsanfeng") {
		t.Fatalf("should match 张三丰 (longest), got %q", result)
	}
}

func TestResolveMentions_CardFormat(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	// Content with complex markdown triggers card format
	input := "# 巡检报告\n\n@张三 请查看\n\n```\nstatus: ok\n```"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "<at id=ou_zhangsan></at>") {
		t.Fatalf("card format should use <at id=...>, got %q", result)
	}
}

func TestResolveMentions_DisabledByConfig(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: false}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	input := "@张三 请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if result != input {
		t.Fatalf("resolve_mentions=false should not replace, got %q", result)
	}
}

func TestResolveMentions_NoAtSign(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	input := "普通消息没有at"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if result != input {
		t.Fatalf("no @ should return unchanged, got %q", result)
	}
}

func TestResolveMentions_DuplicateNameSkipped(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "", "李四": "ou_lisi"},
		fetchedAt: time.Now(),
	})
	input := "请 @张三 和 @李四 看看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "@张三") {
		t.Fatal("ambiguous name should be kept as-is")
	}
	if strings.Contains(result, "@李四") {
		t.Fatal("unique name should be resolved")
	}
}

func TestResolveMentions_SpecialCharsEscaped(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{`A<"B">`: "ou_special"},
		fetchedAt: time.Now(),
	})
	input := `@A<"B"> 你好`
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if strings.Contains(result, `<"B">`) {
		t.Fatalf("special chars should be escaped, got %q", result)
	}
	if !strings.Contains(result, "A&lt;") {
		t.Fatalf("expected HTML-escaped name, got %q", result)
	}
}
