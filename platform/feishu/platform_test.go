package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestDetectFeishuFileMessageType(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		fileName string
		fileType string
		msgType  string
	}{
		{
			name:     "mp4 video",
			mimeType: "video/mp4",
			fileName: "demo.mp4",
			fileType: larkim.FileTypeMp4,
			msgType:  larkim.MsgTypeMedia,
		},
		{
			name:     "opus audio",
			mimeType: "audio/ogg",
			fileName: "voice.ogg",
			fileType: larkim.FileTypeOpus,
			msgType:  larkim.MsgTypeAudio,
		},
		{
			name:     "pdf file",
			mimeType: "application/pdf",
			fileName: "report.pdf",
			fileType: larkim.FileTypePdf,
			msgType:  larkim.MsgTypeFile,
		},
		{
			name:     "unknown file",
			mimeType: "application/octet-stream",
			fileName: "payload.bin",
			fileType: larkim.FileTypeStream,
			msgType:  larkim.MsgTypeFile,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileType := detectFeishuFileType(tt.mimeType, tt.fileName)
			if fileType != tt.fileType {
				t.Fatalf("detectFeishuFileType() = %q, want %q", fileType, tt.fileType)
			}
			if msgType := detectFeishuFileMessageType(fileType); msgType != tt.msgType {
				t.Fatalf("detectFeishuFileMessageType() = %q, want %q", msgType, tt.msgType)
			}
		})
	}
}

func TestBuildFeishuFileMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		msgType string
	}{
		{name: "file", msgType: larkim.MsgTypeFile},
		{name: "audio", msgType: larkim.MsgTypeAudio},
		{name: "media", msgType: larkim.MsgTypeMedia},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := buildFeishuFileMessageContent(tt.msgType, "file_v2_test")
			if err != nil {
				t.Fatalf("buildFeishuFileMessageContent() error = %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(content), &got); err != nil {
				t.Fatalf("content is not JSON: %v", err)
			}
			if got["file_key"] != "file_v2_test" {
				t.Fatalf("file_key = %q, want file_v2_test", got["file_key"])
			}
		})
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
		if msg.ChannelKey != "oc_test_chat:topic:om_root_thread" {
			t.Fatalf("ChannelKey = %q, want oc_test_chat:topic:om_root_thread", msg.ChannelKey)
		}
		if msg.LegacyChannelKey != "oc_test_chat" {
			t.Fatalf("LegacyChannelKey = %q, want oc_test_chat", msg.LegacyChannelKey)
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
	if receivedMsg.ChannelKey != "oc_test" {
		t.Fatalf("ChannelKey = %q, want oc_test", receivedMsg.ChannelKey)
	}
	if receivedMsg.LegacyChannelKey != "" {
		t.Fatalf("LegacyChannelKey = %q, want empty outside topic mode", receivedMsg.LegacyChannelKey)
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
	if receivedMsg.ChannelKey != "oc_test:topic:om_root" {
		t.Fatalf("ChannelKey = %q, want oc_test:topic:om_root", receivedMsg.ChannelKey)
	}
	if receivedMsg.LegacyChannelKey != "oc_test" {
		t.Fatalf("LegacyChannelKey = %q, want oc_test", receivedMsg.LegacyChannelKey)
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
		if receivedMsg.ChannelKey != "oc_test:topic:om_root" {
			t.Fatalf("ChannelKey = %q, want oc_test:topic:om_root", receivedMsg.ChannelKey)
		}
		if receivedMsg.LegacyChannelKey != "oc_test" {
			t.Fatalf("LegacyChannelKey = %q, want oc_test", receivedMsg.LegacyChannelKey)
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

func TestUserIDFromEventFallsBackToUserID(t *testing.T) {
	userID := "uid_user123"
	if got := userIDFromEvent(&larkim.UserId{UserId: &userID}); got != userID {
		t.Fatalf("userIDFromEvent() = %q, want %q", got, userID)
	}
}

func TestResolveUserNameSkipsInvalidLookupID(t *testing.T) {
	p := &Platform{}
	for _, id := range []string{"", "feishu:oc_chat:ou_user", "ou user"} {
		if got := p.resolveUserName(id); got != id {
			t.Fatalf("resolveUserName(%q) = %q, want unchanged", id, got)
		}
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

func TestBuildPreviewCardJSON_ProgressPayloadSeparatesReasoningAndTools(t *testing.T) {
	exitCode := 0
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "Inspecting event routing"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"},
		{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "/tmp/project", ExitCode: &exitCode},
	}, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)

	panels := collectCardPanels(t, buildPreviewCardJSON(payload))
	if len(panels) != 2 {
		t.Fatalf("panel count = %d, want 2 panels: %#v", len(panels), panels)
	}
	if got := cardPanelTitle(panels[0]); got != "Reasoning (1)" {
		t.Fatalf("first panel title = %q, want Reasoning (1)", got)
	}
	if got := cardPanelTitle(panels[1]); got != "Tools (2)" {
		t.Fatalf("second panel title = %q, want Tools (2)", got)
	}
	if panelContains(t, panels[0], "pwd") {
		t.Fatalf("reasoning panel should not include tool content: %#v", panels[0])
	}
	if panelContains(t, panels[1], "Inspecting event routing") {
		t.Fatalf("tools panel should not include reasoning content: %#v", panels[1])
	}
	if !panelContains(t, panels[0], "mindmap_outlined") {
		t.Fatalf("reasoning panel should render a reasoning icon: %#v", panels[0])
	}
}

func TestBuildPreviewCardJSON_ProgressPayloadUsesToolDescriptors(t *testing.T) {
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "web_fetch", Text: "https://example.com/docs?token=secret"},
	}, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)

	panels := collectCardPanels(t, buildPreviewCardJSON(payload))
	if len(panels) != 1 {
		t.Fatalf("panel count = %d, want 1 tools panel: %#v", len(panels), panels)
	}
	if !panelContains(t, panels[0], "language_outlined") {
		t.Fatalf("tools panel should use web fetch icon: %#v", panels[0])
	}
	if !panelContains(t, panels[0], "Fetch web page") {
		t.Fatalf("tools panel should use descriptor title: %#v", panels[0])
	}
	if panelContains(t, panels[0], "token=secret") {
		t.Fatalf("tools panel should redact sensitive URL query params: %#v", panels[0])
	}
}

func TestBuildRichCard_UsesCodexRuntimeToolDescriptors(t *testing.T) {
	cardJSON := buildRichCard(core.CardStatusWorking, "", []core.ToolStep{
		{Kind: core.ToolStepKindTool, Name: "functions.exec_command", Summary: `{"cmd":"pwd"}`},
		{Kind: core.ToolStepKindTool, Name: "functions.write_stdin", Summary: `{"chars":"q"}`},
		{Kind: core.ToolStepKindTool, Name: "functions.exec_command", Summary: `{"cmd":"git status --short"}`},
		{Kind: core.ToolStepKindTool, Name: "functions.exec_command", Summary: `{"cmd":"ps aux | grep cc-connect"}`},
		{Kind: core.ToolStepKindTool, Name: "apply_patch", Summary: "/tmp/file.go"},
		{Kind: core.ToolStepKindTool, Name: "multi_tool_use.parallel", Summary: "3 tool calls"},
		{Kind: core.ToolStepKindTool, Name: "tool_search_tool", Summary: "search available tools"},
		{Kind: core.ToolStepKindTool, Name: "update_plan", Summary: "revise checklist"},
		{Kind: core.ToolStepKindTool, Name: "request_user_input", Summary: "ask for confirmation"},
	}, "answer", true, "")

	panels := collectCardPanels(t, cardJSON)
	if len(panels) != 1 {
		t.Fatalf("panel count = %d, want 1 tools panel: %#v", len(panels), panels)
	}
	for _, want := range []string{
		"Inspect files",
		"Command I/O",
		"Git",
		"Inspect process",
		"Edit",
		"Run tools",
		"Search tools",
		"Update plan",
		"Ask user",
		"file-link-text_outlined",
		"keyboard_outlined",
		"code_outlined",
		"computer_outlined",
		"edit_outlined",
		"list-check_outlined",
		"search_outlined",
		"robot_outlined",
	} {
		if !panelContains(t, panels[0], want) {
			t.Fatalf("tools panel should contain %q: %#v", want, panels[0])
		}
	}
}

func TestBuildRichCard_RendersThinkingAndToolResultRows(t *testing.T) {
	code := 0
	success := true
	cardJSON := buildRichCard(core.CardStatusWorking, "", []core.ToolStep{
		{Kind: core.ToolStepKindThinking, Name: "Thinking", Summary: "Inspecting event routing"},
		{
			Kind:     core.ToolStepKindTool,
			Name:     "Bash",
			Summary:  "echo hi",
			Result:   "hi",
			Status:   "completed",
			ExitCode: &code,
			Success:  &success,
			Done:     true,
		},
	}, "done", true, "")

	for _, want := range []string{"Inspecting event routing", "echo hi", "completed", "exit: 0", "hi"} {
		if !strings.Contains(cardJSON, want) {
			t.Fatalf("rich card should contain %q, got %q", want, cardJSON)
		}
	}
	if strings.Contains(cardJSON, core.ProgressCardPayloadPrefix) {
		t.Fatalf("rich card should not contain progress payload prefix, got %q", cardJSON)
	}
}

func TestBuildRichCard_OversizePanelsKeepVisibleContent(t *testing.T) {
	var steps []core.ToolStep
	for i := 0; i < 40; i++ {
		steps = append(steps, core.ToolStep{
			Kind:    core.ToolStepKindThinking,
			Name:    "Thinking",
			Summary: "visible reasoning " + strconv.Itoa(i) + " " + strings.Repeat("r", 700),
		})
		steps = append(steps, core.ToolStep{
			Kind:    core.ToolStepKindTool,
			Name:    "Bash",
			Summary: "visible command " + strconv.Itoa(i) + " " + strings.Repeat("c", 700),
			Result:  "visible result " + strconv.Itoa(i) + " " + strings.Repeat("o", 700),
			Done:    true,
		})
	}

	cardJSON := buildRichCard(core.CardStatusWorking, "", steps, "", true, "")

	panels := collectCardPanels(t, cardJSON)
	if len(panels) == 0 {
		t.Fatalf("oversize rich card should preserve compact reasoning/tool panels instead of blank fallback: %q", cardJSON)
	}
	rendered := strings.Join(collectCardMarkdownContents(t, cardJSON), "\n") + "\n" + cardJSON
	if !strings.Contains(rendered, "visible reasoning") && !strings.Contains(rendered, "visible command") {
		t.Fatalf("oversize rich card should keep visible step content, got %q", cardJSON)
	}
}

func TestBuildRichCard_PanelsShowLatestTenSteps(t *testing.T) {
	var steps []core.ToolStep
	for i := 0; i < 15; i++ {
		steps = append(steps, core.ToolStep{
			Kind:    core.ToolStepKindThinking,
			Name:    "Thinking",
			Summary: "reasoning-index-" + strconv.Itoa(i),
		})
		steps = append(steps, core.ToolStep{
			Kind:    core.ToolStepKindTool,
			Name:    "Bash",
			Summary: "tool-command-" + strconv.Itoa(i),
			Result:  "tool-result-" + strconv.Itoa(i),
			Done:    true,
		})
	}

	cardJSON := buildRichCard(core.CardStatusWorking, "", steps, "answer", true, "")

	panels := collectCardPanels(t, cardJSON)
	if len(panels) != 2 {
		t.Fatalf("panel count = %d, want reasoning and tools panels: %#v", len(panels), panels)
	}
	for _, tt := range []struct {
		name          string
		panel         map[string]any
		oldestHidden  string
		windowStart   string
		latestVisible string
		resultVisible string
		hiddenSummary string
	}{
		{
			name:          "reasoning",
			panel:         panels[0],
			oldestHidden:  "reasoning-index-0",
			windowStart:   "reasoning-index-5",
			latestVisible: "reasoning-index-14",
			hiddenSummary: "5 earlier steps hidden",
		},
		{
			name:          "tools",
			panel:         panels[1],
			oldestHidden:  "tool-command-0",
			windowStart:   "tool-command-5",
			latestVisible: "tool-command-14",
			resultVisible: "tool-result-14",
			hiddenSummary: "5 earlier steps hidden",
		},
	} {
		if panelContains(t, tt.panel, tt.oldestHidden) {
			t.Fatalf("%s panel should hide oldest step %q: %#v", tt.name, tt.oldestHidden, tt.panel)
		}
		for _, want := range []string{tt.windowStart, tt.latestVisible, tt.resultVisible, tt.hiddenSummary} {
			if want == "" {
				continue
			}
			if !panelContains(t, tt.panel, want) {
				t.Fatalf("%s panel should contain %q: %#v", tt.name, want, tt.panel)
			}
		}
	}
}

func TestBuildRichCard_SeparatesReasoningAndTools(t *testing.T) {
	cardJSON := buildRichCard(core.CardStatusWorking, "", []core.ToolStep{
		{Kind: core.ToolStepKindThinking, Summary: "Inspecting event routing"},
		{Kind: core.ToolStepKindTool, Name: "Bash", Summary: "pwd"},
	}, "answer", true, "")

	panels := collectCardPanels(t, cardJSON)
	if len(panels) != 2 {
		t.Fatalf("panel count = %d, want 2 panels: %#v", len(panels), panels)
	}
	if got := cardPanelTitle(panels[0]); got != "Reasoning (1)" {
		t.Fatalf("first panel title = %q, want Reasoning (1)", got)
	}
	if got := cardPanelTitle(panels[1]); got != "Tools (1)" {
		t.Fatalf("second panel title = %q, want Tools (1)", got)
	}
	if panelContains(t, panels[0], "pwd") {
		t.Fatalf("reasoning panel should not include tool content: %#v", panels[0])
	}
	if panelContains(t, panels[1], "Inspecting event routing") {
		t.Fatalf("tools panel should not include reasoning content: %#v", panels[1])
	}
	if !panelContains(t, panels[0], "mindmap_outlined") {
		t.Fatalf("reasoning panel should render a reasoning icon: %#v", panels[0])
	}
}

func TestBuildRichCard_UsesToolDescriptorsForAliases(t *testing.T) {
	cardJSON := buildRichCard(core.CardStatusWorking, "", []core.ToolStep{
		{Kind: core.ToolStepKindTool, Name: "web_fetch", Summary: "https://example.com/docs?token=secret"},
	}, "answer", true, "")

	panels := collectCardPanels(t, cardJSON)
	if len(panels) != 1 {
		t.Fatalf("panel count = %d, want 1 tools panel: %#v", len(panels), panels)
	}
	if !panelContains(t, panels[0], "language_outlined") {
		t.Fatalf("tools panel should use web fetch icon: %#v", panels[0])
	}
	if !panelContains(t, panels[0], "Fetch web page") {
		t.Fatalf("tools panel should use descriptor title: %#v", panels[0])
	}
	if panelContains(t, panels[0], "token=secret") {
		t.Fatalf("tools panel should redact sensitive URL query params: %#v", panels[0])
	}
}

func TestBuildRichCard_SanitizesMarkdownForCardLimits(t *testing.T) {
	markdown := strings.Join([]string{
		"```",
		"| code |",
		"|---|",
		"| example |",
		"```",
		"",
		"# Big Result",
		"| A |",
		"|---|",
		"| 1 |",
		"",
		"| B |",
		"|---|",
		"| 2 |",
		"",
		"| C |",
		"|---|",
		"| 3 |",
		"",
		"| D |",
		"|---|",
		"| 4 |",
		"",
		"![remote](https://example.com/image.png)",
		"![ok](img_v3_abc)",
	}, "\n")

	content := strings.Join(collectCardMarkdownContents(t, buildRichCard(core.CardStatusDone, "", nil, markdown, false, "")), "\n")
	if containsMarkdownLine(content, "# Big Result") {
		t.Fatalf("card markdown should downgrade h1 headings, got %q", content)
	}
	if !strings.Contains(content, "#### Big Result") {
		t.Fatalf("card markdown should render h1 as h4, got %q", content)
	}
	if !strings.Contains(content, "```\n| D |\n|---|\n| 4 |\n```") {
		t.Fatalf("fourth table should be downgraded to a code block, got %q", content)
	}
	if !strings.Contains(content, "```\n| code |\n|---|\n| example |\n```") {
		t.Fatalf("existing code block tables should stay intact, got %q", content)
	}
	if strings.Contains(content, "![remote]") || strings.Contains(content, "https://example.com/image.png") {
		t.Fatalf("card markdown should strip unresolved non-img_ images, got %q", content)
	}
	if !strings.Contains(content, "![ok](img_v3_abc)") {
		t.Fatalf("card markdown should preserve Feishu image keys, got %q", content)
	}
}

func TestResolveRichCardMarkdownUploadsRemoteImages(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	var (
		mu   sync.Mutex
		urls []string
	)
	p.richCardImageUploadFunc = func(_ context.Context, rawURL string) (string, error) {
		mu.Lock()
		urls = append(urls, rawURL)
		mu.Unlock()
		return "img_v3_uploaded", nil
	}

	input := strings.Join([]string{
		"before",
		"![chart](https://example.com/chart.png)",
		"![existing](img_v3_existing)",
		"![local](/tmp/chart.png)",
		"![inline](data:image/png;base64,abc)",
	}, "\n")

	got := p.ResolveRichCardMarkdown(context.Background(), input, true)
	if !strings.Contains(got, "![chart](img_v3_uploaded)") {
		t.Fatalf("remote image should be replaced with uploaded image key, got %q", got)
	}
	if !strings.Contains(got, "![existing](img_v3_existing)") {
		t.Fatalf("existing Feishu image key should be preserved, got %q", got)
	}
	for _, blocked := range []string{"https://example.com/chart.png", "/tmp/chart.png", "data:image"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("unsupported or unresolved image reference %q should be stripped, got %q", blocked, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 1 || urls[0] != "https://example.com/chart.png" {
		t.Fatalf("uploaded URLs = %v, want only remote chart URL", urls)
	}
}

func TestResolveRichCardMarkdownStreamingStartsUploadWithoutWaiting(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	started := make(chan struct{})
	release := make(chan struct{})
	seenURL := make(chan string, 1)
	var once sync.Once
	p.richCardImageUploadFunc = func(_ context.Context, rawURL string) (string, error) {
		once.Do(func() {
			seenURL <- rawURL
			close(started)
		})
		<-release
		return "img_v3_chart", nil
	}

	input := "before ![chart](https://example.com/chart.png) after"
	start := time.Now()
	streaming := p.ResolveRichCardMarkdown(context.Background(), input, false)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("streaming resolve waited %s, want quick unresolved strip", elapsed)
	}
	if strings.Contains(streaming, "https://example.com/chart.png") || strings.Contains(streaming, "![chart]") {
		t.Fatalf("streaming resolve should strip unresolved remote image, got %q", streaming)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("streaming resolve should start background upload")
	}
	if rawURL := <-seenURL; rawURL != "https://example.com/chart.png" {
		t.Fatalf("upload URL = %q, want chart URL", rawURL)
	}
	close(release)

	final := p.ResolveRichCardMarkdown(context.Background(), input, true)
	if !strings.Contains(final, "![chart](img_v3_chart)") {
		t.Fatalf("final resolve should wait for uploaded image key, got %q", final)
	}
}

func TestResolveRichCardMarkdownFailedImageIsNotRetried(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	var (
		mu    sync.Mutex
		calls int
	)
	p.richCardImageUploadFunc = func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return "", errors.New("upload failed")
	}

	input := "before ![chart](https://example.com/chart.png) after"
	first := p.ResolveRichCardMarkdown(context.Background(), input, true)
	second := p.ResolveRichCardMarkdown(context.Background(), input, true)

	for _, got := range []string{first, second} {
		if strings.Contains(got, "https://example.com/chart.png") || strings.Contains(got, "![chart]") {
			t.Fatalf("failed remote image should be stripped, got %q", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("upload calls = %d, want failed URL cached after first attempt", calls)
	}
}

func TestRichCardImageBlocksPrivateAndReservedIPs(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.0.0.1",
		"169.254.169.254",
		"100.64.0.1",
		"192.0.2.1",
		"2001:db8::1",
	}
	for _, ip := range blocked {
		if !isBlockedRichCardImageIP(net.ParseIP(ip)) {
			t.Fatalf("IP %s should be blocked for rich-card image fetch", ip)
		}
	}
	if isBlockedRichCardImageIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP 8.8.8.8 should not be blocked")
	}
}

func containsMarkdownLine(content string, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func TestBuildCardJSONWithStatusFooter_SharesCardTableBudget(t *testing.T) {
	body := strings.Join([]string{
		"| A |",
		"|---|",
		"| 1 |",
		"",
		"| B |",
		"|---|",
		"| 2 |",
	}, "\n")
	footer := strings.Join([]string{
		"| C |",
		"|---|",
		"| 3 |",
		"",
		"| D |",
		"|---|",
		"| 4 |",
	}, "\n")

	content := strings.Join(collectCardMarkdownContents(t, buildCardJSONWithStatusFooter(body, footer)), "\n")
	if !strings.Contains(content, "| C |\n|---|\n| 3 |") {
		t.Fatalf("third table should remain renderable, got %q", content)
	}
	if !strings.Contains(content, "```\n| D |\n|---|\n| 4 |\n```") {
		t.Fatalf("fourth table across card elements should be downgraded, got %q", content)
	}
}

func TestFeishuCardAPIErrorClassification(t *testing.T) {
	rateLimitErr := classifyFeishuCardAPIError("stream", 230020, "rate limited")
	if !errors.Is(rateLimitErr, errFeishuCardRateLimited) {
		t.Fatalf("230020 should be rate limit, got %v", rateLimitErr)
	}

	tableErr := classifyFeishuCardAPIError("update", 230099, "Failed to create card content, ext=ErrCode: 11310; ErrMsg: card table number over limit; ErrorValue: table;")
	if !errors.Is(tableErr, errFeishuCardTableLimit) {
		t.Fatalf("230099/11310 table error should be table limit, got %v", tableErr)
	}

	otherElementErr := classifyFeishuCardAPIError("update", 230099, "Failed to create card content, ext=ErrCode: 11310; ErrMsg: element exceeds the limit;")
	if errors.Is(otherElementErr, errFeishuCardTableLimit) {
		t.Fatalf("generic 11310 element errors should not be treated as table limit: %v", otherElementErr)
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

func collectCardPanels(t *testing.T, cardJSON string) []map[string]any {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card JSON is invalid: %v\n%s", err, cardJSON)
	}
	body, ok := card["body"].(map[string]any)
	if !ok || body == nil {
		t.Fatalf("card JSON missing body: %#v", card)
	}
	rawElements, ok := body["elements"].([]any)
	if !ok {
		t.Fatalf("card body elements have unexpected type: %#v", body["elements"])
	}
	var panels []map[string]any
	for _, raw := range rawElements {
		elem, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if elem["tag"] == "collapsible_panel" {
			panels = append(panels, elem)
		}
	}
	return panels
}

func collectCardMarkdownContents(t *testing.T, cardJSON string) []string {
	t.Helper()
	var card any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card JSON is invalid: %v\n%s", err, cardJSON)
	}
	var contents []string
	var walk func(any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if node["tag"] == "markdown" {
				if content, ok := node["content"].(string); ok {
					contents = append(contents, content)
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(card)
	return contents
}

func cardPanelTitle(panel map[string]any) string {
	header, _ := panel["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	content, _ := title["content"].(string)
	return content
}

func panelContains(t *testing.T, panel map[string]any, want string) bool {
	t.Helper()
	b, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("panel marshal failed: %v", err)
	}
	return strings.Contains(string(b), want)
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
				"group_reply_all":    true,
				"allow_chat":         tt.allowChat,
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

// TestResolveMentions_MarkdownContent verifies that @name inside markdown
// content (which would trigger MsgTypeInteractive) is still resolved to the
// MsgTypeText at syntax (<at user_id="...">name</at>).
func TestResolveMentions_MarkdownContent(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	// Content with complex markdown
	input := "# 巡检报告\n\n@张三 请查看\n\n```\nstatus: ok\n```"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, `<at user_id="ou_zhangsan">张三</at>`) {
		t.Fatalf("markdown content should resolve to text format <at user_id=...>, got %q", result)
	}
	if strings.Contains(result, "<at id=") {
		t.Fatalf("card format <at id=...> must not be emitted (no mention event); got %q", result)
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

type mockRefreshPlatform struct {
	*Platform
	refreshCalled atomic.Int32
	refreshDone   chan struct{}
	refreshCard   func(ctx context.Context, sessionKey string, card *core.Card) error
}

func newMockRefreshPlatform(p *Platform) *mockRefreshPlatform {
	return &mockRefreshPlatform{Platform: p, refreshDone: make(chan struct{})}
}

func (m *mockRefreshPlatform) RefreshCard(ctx context.Context, sessionKey string, card *core.Card) error {
	m.refreshCalled.Add(1)
	close(m.refreshDone)
	if m.refreshCard != nil {
		return m.refreshCard(ctx, sessionKey, card)
	}
	return nil
}

func TestCardAction_NavFast_ReturnsCard(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		return core.NewCard().Markdown("list content").Build()
	}

	start := time.Now()
	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "nav:/list"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card response, got %#v", resp)
	}
	if elapsed >= cardNavTimeout {
		t.Fatalf("fast nav should return within cardNavTimeout, took %v", elapsed)
	}
	if resp.Toast != nil {
		t.Fatalf("expected no toast for fast response, got %q", resp.Toast.Content)
	}
}

func TestCardAction_NavSlow_ReturnsToastThenRefreshes(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	mock := newMockRefreshPlatform(ip.Platform)
	ip.Platform.self = mock

	handlerDone := make(chan struct{})
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		time.Sleep(cardNavTimeout + 200*time.Millisecond)
		close(handlerDone)
		return core.NewCard().Markdown("async list content").Build()
	}

	start := time.Now()
	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "nav:/list"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got %#v", resp)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("should return within feishu timeout, took %v", elapsed)
	}
	if resp.Card != nil {
		t.Fatalf("expected no card for timeout response, got non-nil card")
	}

	select {
	case <-mock.refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshCard should have been called")
	}

	if got := mock.refreshCalled.Load(); got != 1 {
		t.Fatalf("RefreshCard called %d times, want 1", got)
	}
}

func TestCardAction_NavSlow_NilCard_NoRefresh(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	mock := newMockRefreshPlatform(ip.Platform)
	ip.Platform.self = mock

	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		time.Sleep(cardNavTimeout + 200*time.Millisecond)
		return nil
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "nav:/list"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})

	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got %#v", resp)
	}

	time.Sleep(cardNavTimeout + 500*time.Millisecond)

	if got := mock.refreshCalled.Load(); got != 0 {
		t.Fatalf("RefreshCard should not be called for nil card, called %d times", got)
	}
}

// TestOnMessage_OldMessageAfterRestartIsFiltered verifies that a Feishu message whose
// create_time is before the process start time is silently dropped.  This is the
// expected "drop replayed pre-restart messages" behaviour described in issue #972.
func TestOnMessage_OldMessageAfterRestartIsFiltered(t *testing.T) {
	// Simulate a daemon "restart" by pinning StartTime to now.
	restartTime := time.Now()
	orig := core.StartTime
	core.StartTime = restartTime
	defer func() { core.StartTime = orig }()

	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	handlerCalled := false
	ip.handler = func(_ core.Platform, _ *core.Message) {
		handlerCalled = true
	}

	// A message created 10 minutes before the restart — must be dropped.
	oldCreateTime := strconv.FormatInt(restartTime.Add(-10*time.Minute).UnixMilli(), 10)
	msgID := "om_old_test"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"hello"}`

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &oldCreateTime,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if handlerCalled {
		t.Error("handler should NOT be called for pre-restart message (create_time before StartTime)")
	}
}

// TestOnMessage_NewMessageAfterRestartIsProcessed verifies that a Feishu message whose
// create_time is after the process start time is delivered to the handler normally.
// Regression test for issue #972: new messages must not be mis-classified as "old".
func TestOnMessage_NewMessageAfterRestartIsProcessed(t *testing.T) {
	// Simulate a daemon "restart" by pinning StartTime to 5 minutes ago.
	restartTime := time.Now().Add(-5 * time.Minute)
	orig := core.StartTime
	core.StartTime = restartTime
	defer func() { core.StartTime = orig }()

	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	var (
		wg          sync.WaitGroup
		receivedMsg *core.Message
	)
	wg.Add(1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	// A message created 2 minutes AFTER restart — must be processed.
	newCreateTime := strconv.FormatInt(restartTime.Add(2*time.Minute).UnixMilli(), 10)
	msgID := "om_new_test"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"/list"}`

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &newCreateTime,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	wg.Wait()
	if receivedMsg == nil {
		t.Error("handler should be called for post-restart message (create_time after StartTime)")
	}
}

// TestOnMessage_GracePeriodMessageIsProcessed verifies that a message created within
// the 2-second grace window just before StartTime is NOT dropped (issue #972 edge case).
func TestOnMessage_GracePeriodMessageIsProcessed(t *testing.T) {
	restartTime := time.Now()
	orig := core.StartTime
	core.StartTime = restartTime
	defer func() { core.StartTime = orig }()

	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	var (
		wg          sync.WaitGroup
		receivedMsg *core.Message
	)
	wg.Add(1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	// A message created 1 second before restart — within the 2s grace window, must pass.
	graceCreateTime := strconv.FormatInt(restartTime.Add(-1*time.Second).UnixMilli(), 10)
	msgID := "om_grace_test"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"grace"}`

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &graceCreateTime,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	wg.Wait()
	if receivedMsg == nil {
		t.Error("handler should be called for message within the 2s grace window")
	}
}

func TestCmdAction_WithoutAfterClick_DispatchesAndReturnsNil(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) { msgCh <- msg }

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user1"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "cmd:/verify F9F-162"}},
			Context:  &callback.Context{OpenChatID: "oc_chat1", OpenMessageID: "om_msg1"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response without after_click, got %#v", resp)
	}

	select {
	case msg := <-msgCh:
		if msg.Content != "/verify F9F-162" {
			t.Fatalf("dispatched content = %q, want /verify F9F-162", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected command to be dispatched")
	}
}

func TestCmdAction_WithAfterClick_DispatchesAndReturnsCard(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) { msgCh <- msg }

	afterClick := map[string]any{
		"title":     "✅ Done",
		"color":     "green",
		"markdown":  "Command recorded.",
		"link_text": "↗ Open",
		"link_url":  "https://example.com/task/123",
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user1"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":      "cmd:/run-task 123",
				"after_click": afterClick,
			}},
			Context: &callback.Context{OpenChatID: "oc_chat1", OpenMessageID: "om_msg1"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card response with after_click, got %#v", resp)
	}
	if resp.Card.Type != "raw" {
		t.Fatalf("card type = %q, want raw", resp.Card.Type)
	}
	cardData, ok := resp.Card.Data.(map[string]any)
	if !ok {
		t.Fatalf("card data type = %T, want map[string]any", resp.Card.Data)
	}
	header, ok := cardData["header"].(map[string]any)
	if !ok {
		t.Fatalf("card header type = %T, want map[string]any", cardData["header"])
	}
	if header["template"] != "green" {
		t.Fatalf("card color = %q, want green", header["template"])
	}

	select {
	case msg := <-msgCh:
		if msg.Content != "/run-task 123" {
			t.Fatalf("dispatched content = %q, want /run-task 123", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected command to be dispatched")
	}
}

func TestCmdAction_WithAfterClick_SessionKeyRoutes(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	const wantSessionKey = "custom-session-abc"
	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) { msgCh <- msg }

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user1"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":      "cmd:/hello",
				"session_key": wantSessionKey,
				"after_click": map[string]any{"title": "✅ Sent"},
			}},
			Context: &callback.Context{OpenChatID: "oc_chat1", OpenMessageID: "om_msg1"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card response, got %#v", resp)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != wantSessionKey {
			t.Fatalf("session key = %q, want %q", msg.SessionKey, wantSessionKey)
		}
		if msg.Content != "/hello" {
			t.Fatalf("content = %q, want /hello", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected command to be dispatched with correct session key")
	}
}
