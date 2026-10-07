package feishu

import (
	"errors"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func newGroupReplyAllTestPlatform(t *testing.T, name string, options map[string]any, handler core.MessageHandler) *Platform {
	t.Helper()
	opts := map[string]any{
		"app_id": "cli_test", "app_secret": "secret_test",
		"allow_from": "*", "allow_chat": "*", "enable_feishu_card": false,
	}
	for key, value := range options {
		opts[key] = value
	}
	platform, err := newPlatform(name, lark.FeishuBaseUrl, opts)
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	p := extractBasePlatform(platform)
	p.botOpenID = "ou_bot"
	p.handler = handler
	for id, name := range map[string]string{"ou_alice": "Alice", "ou_other": "Other"} {
		p.userNameCache.Store(id, name)
	}
	for _, chatID := range []string{"oc_allowed", "oc_main"} {
		p.chatNameCache.Store(chatID, chatID)
	}
	return p
}

func TestFeishuGroupReplyAllChats_RoutesOnlySelectedChats(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		opts           map[string]any
	}{
		{"csv", "feishu", map[string]any{"group_reply_all_chats": " , oc_allowed, oc_allowed, "}},
		{"string_array", "feishu", map[string]any{"group_reply_all_chats": []string{"", " oc_allowed "}}},
		{"toml_array", "lark", map[string]any{"group_reply_all_chats": []any{"oc_allowed", ""}}},
		{"overrides_global", "feishu", map[string]any{"group_reply_all": true, "group_reply_all_chats": "oc_allowed"}},
		{"overrides_require_mention", "feishu", map[string]any{"require_mention": false, "group_reply_all_chats": "oc_allowed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan *core.Message, 3)
			p := newGroupReplyAllTestPlatform(t, tc.platform, tc.opts, func(_ core.Platform, msg *core.Message) { messages <- msg })
			sendGroupHistoryText(t, p, "m1", "ou_alice", "selected group", "oc_allowed", "", false)
			if msg := awaitGroupHistoryMessage(t, messages); msg.Content != "selected group" {
				t.Fatalf("selected chat message = %q", msg.Content)
			}
			sendGroupHistoryText(t, p, "m2", "ou_alice", "ordinary chatter", "oc_main", "", false)
			assertNoGroupHistoryMessage(t, messages)
			sendGroupHistoryText(t, p, "m3", "ou_alice", "explicit mention", "oc_main", "", true)
			if msg := awaitGroupHistoryMessage(t, messages); msg.Content != "explicit mention" {
				t.Fatalf("mentioned chat message = %q", msg.Content)
			}
		})
	}
}

func TestFeishuGroupReplyAllChats_PreservesDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]any
		want bool
	}{
		{"omitted", nil, false},
		{"global_true", map[string]any{"group_reply_all": true}, true},
		{"require_mention_false", map[string]any{"require_mention": false}, true},
		{"empty_csv", map[string]any{"group_reply_all": true, "group_reply_all_chats": " , "}, true},
		{"empty_array", map[string]any{"group_reply_all": true, "group_reply_all_chats": []any{}}, true},
		{"empty_array_default", map[string]any{"group_reply_all_chats": []string{" "}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan *core.Message, 1)
			p := newGroupReplyAllTestPlatform(t, "feishu", tc.opts, func(_ core.Platform, msg *core.Message) { messages <- msg })
			sendGroupHistoryText(t, p, "m1", "ou_alice", "no mention", "oc_main", "", false)
			if tc.want {
				awaitGroupHistoryMessage(t, messages)
			} else {
				assertNoGroupHistoryMessage(t, messages)
			}
		})
	}
}

func TestFeishuGroupReplyAllChats_RejectsInvalidConfig(t *testing.T) {
	for _, raw := range []any{true, 123, []any{"oc_allowed", 123}} {
		_, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
			"app_id": "cli_test", "app_secret": "secret_test", "group_reply_all_chats": raw,
		})
		if err == nil || !strings.Contains(err.Error(), "group_reply_all_chats") {
			t.Errorf("raw %T: error = %v, want group_reply_all_chats configuration error", raw, err)
		}
	}
}

func TestFeishuGroupReplyAllChats_PreservesAllowLists(t *testing.T) {
	for _, tc := range []struct {
		name, userID string
		opts         map[string]any
	}{
		{"allow_from", "ou_other", map[string]any{"allow_from": "ou_alice"}},
		{"allow_chat", "ou_alice", map[string]any{"allow_chat": "oc_main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan *core.Message, 1)
			tc.opts["group_reply_all_chats"] = "oc_allowed"
			p := newGroupReplyAllTestPlatform(t, "feishu", tc.opts, func(_ core.Platform, msg *core.Message) { messages <- msg })
			sendGroupHistoryText(t, p, "m1", tc.userID, "not authorized", "oc_allowed", "", false)
			assertNoGroupHistoryMessage(t, messages)
		})
	}
}

func TestFeishuGroupReplyAllChats_UnselectedChatsStillFailClosed(t *testing.T) {
	messages := make(chan *core.Message, 2)
	p := newGroupReplyAllTestPlatform(t, "feishu", map[string]any{
		"group_reply_all": true, "group_reply_all_chats": "oc_allowed",
	}, func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.botOpenID = ""
	p.markGroupFilterDegraded(errors.New("bot discovery failed"))
	sendGroupHistoryText(t, p, "m1", "ou_alice", "drop this", "oc_main", "", false)
	assertNoGroupHistoryMessage(t, messages)
	sendGroupHistoryText(t, p, "m2", "ou_alice", "selected group", "oc_allowed", "", false)
	awaitGroupHistoryMessage(t, messages)
}

func TestFeishuGroupReplyAllChats_PreservesHistoryForUnselectedChats(t *testing.T) {
	messages := make(chan *core.Message, 3)
	p := newGroupReplyAllTestPlatform(t, "feishu", map[string]any{
		"group_reply_all": true, "group_reply_all_chats": "oc_allowed", "group_chat_history_share": true,
	}, func(_ core.Platform, msg *core.Message) { messages <- msg })
	sendGroupHistoryText(t, p, "m1", "ou_alice", "reply immediately", "oc_allowed", "", false)
	if msg := awaitGroupHistoryMessage(t, messages); msg.ExtraContent != "" {
		t.Fatalf("selected group was incorrectly buffered: %q", msg.ExtraContent)
	}
	sendGroupHistoryText(t, p, "m2", "ou_alice", "pending group context", "oc_main", "", false)
	assertNoGroupHistoryMessage(t, messages)
	sendGroupHistoryText(t, p, "m3", "ou_alice", "use that context", "oc_main", "", true)
	msg := awaitGroupHistoryMessage(t, messages)
	if !strings.Contains(msg.ExtraContent, "pending group context") || msg.OnAccepted == nil {
		t.Fatalf("unselected chat lost history: extra=%q onAccepted=%v", msg.ExtraContent, msg.OnAccepted != nil)
	}
	msg.OnAccepted()
	sendGroupHistoryText(t, p, "m4", "ou_alice", "next turn", "oc_main", "", true)
	if msg := awaitGroupHistoryMessage(t, messages); msg.ExtraContent != "" {
		t.Fatalf("consumed history repeated: %q", msg.ExtraContent)
	}
}
