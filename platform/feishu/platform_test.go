package feishu

import (
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

	if err := ip.onMessage(event); err != nil {
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

	_ = ip.onMessage(&larkim.P2MessageReceiveV1{
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

	_, err = base.ReconstructReplyCtx("feishu:oc_chat:ou_user")
	if err == nil {
		t.Fatal("expected error for feishu-prefixed key on lark platform")
	}
}

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
