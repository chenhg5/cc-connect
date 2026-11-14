package feishu

import (
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

// TestCreateMessageReceiveIDType pins the mapping from a reply context's
// receive ID type onto the Feishu CreateMessage enum. Anything unknown or
// empty must stay on chat_id so every non-bot-menu path is unaffected.
func TestCreateMessageReceiveIDType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", larkim.CreateMessageV1ReceiveIDTypeChatId},
		{"chat_id", larkim.CreateMessageV1ReceiveIDTypeChatId},
		{"open_id", larkim.CreateMessageV1ReceiveIDTypeOpenId},
		{"union_id", larkim.CreateMessageV1ReceiveIDTypeUnionId},
		{"bogus", larkim.CreateMessageV1ReceiveIDTypeChatId},
	}
	for _, tt := range cases {
		if got := createMessageReceiveIDType(tt.in); got != tt.want {
			t.Errorf("createMessageReceiveIDType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestBotMenuReplyCtx_UsesOpenIdReceiveIDType is the regression test for
// bot menu click events replying with receive_id_type=chat_id while the
// chat context is actually a user open_id, which makes Feishu reject the
// send with "invalid receive_id" (code 230001).
func TestBotMenuReplyCtx_UsesOpenIdReceiveIDType(t *testing.T) {
	p := newFeishuReceiveIDTestPlatform(t, "feishu")
	const openID = "ou_bot_menu_user"
	// Seed the display-name cache so name resolution never hits the network.
	p.userNameCache.Store(openID, "Menu User")

	var got *core.Message
	p.handler = func(_ core.Platform, m *core.Message) { got = m }

	event := &larkapplication.P2BotMenuV6{
		Event: &larkapplication.P2BotMenuV6Data{
			EventKey: strPtr("/status"),
			Operator: &larkapplication.Operator{
				OperatorId: &larkapplication.UserId{OpenId: strPtr(openID)},
			},
		},
	}
	if err := p.onBotMenu(event); err != nil {
		t.Fatalf("onBotMenu() error = %v", err)
	}
	if got == nil {
		t.Fatal("onBotMenu() dispatched no message")
	}
	if got.Content != "/status" {
		t.Errorf("content = %q, want /status", got.Content)
	}

	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", got.ReplyCtx)
	}
	if rc.chatID != openID {
		t.Errorf("chatID = %q, want %q", rc.chatID, openID)
	}
	if rc.receiveIDType != "open_id" {
		t.Errorf("receiveIDType = %q, want open_id", rc.receiveIDType)
	}
	if got := createMessageReceiveIDType(rc.receiveIDType); got != larkim.CreateMessageV1ReceiveIDTypeOpenId {
		t.Errorf("send receive_id_type = %q, want %q", got, larkim.CreateMessageV1ReceiveIDTypeOpenId)
	}
}

// TestReconstructReplyCtx_ReceiveIDTypeFromPrefix covers the restart path:
// a persisted session key whose chat segment is a user or union ID must be
// rebuilt with the matching receive ID type instead of defaulting to chat_id.
func TestReconstructReplyCtx_ReceiveIDTypeFromPrefix(t *testing.T) {
	p := newFeishuReceiveIDTestPlatform(t, "feishu")

	cases := []struct {
		sessionKey string
		wantChatID string
		wantType   string
	}{
		{"feishu:ou_user123:ou_user123", "ou_user123", "open_id"},
		{"feishu:on_union123:ou_user123", "on_union123", "union_id"},
		{"feishu:oc_chat123:ou_user123", "oc_chat123", ""},
	}
	for _, tt := range cases {
		rctx, err := p.ReconstructReplyCtx(tt.sessionKey)
		if err != nil {
			t.Fatalf("ReconstructReplyCtx(%q) error = %v", tt.sessionKey, err)
		}
		rc := rctx.(replyContext)
		if rc.chatID != tt.wantChatID {
			t.Errorf("ReconstructReplyCtx(%q) chatID = %q, want %q", tt.sessionKey, rc.chatID, tt.wantChatID)
		}
		if rc.receiveIDType != tt.wantType {
			t.Errorf("ReconstructReplyCtx(%q) receiveIDType = %q, want %q", tt.sessionKey, rc.receiveIDType, tt.wantType)
		}
	}
}

func newFeishuReceiveIDTestPlatform(t *testing.T, name string) *Platform {
	t.Helper()
	p, err := newPlatform(name, lark.FeishuBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false,
	})
	if err != nil {
		t.Fatalf("newPlatform(%s) error = %v", name, err)
	}
	return p.(*Platform)
}
