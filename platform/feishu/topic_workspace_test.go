package feishu

import (
	"testing"

	"github.com/timmyagentic/cc-connect-next/core"
)

func TestPopulateWorkspaceChannelKeysUsesTopicScope(t *testing.T) {
	p := &Platform{platformName: "feishu", threadIsolation: true}
	msg := &core.Message{ReplyCtx: replyContext{
		chatID:     "oc_chat",
		sessionKey: "feishu:oc_chat:root:om_root",
	}}

	p.populateWorkspaceChannelKeys(msg)

	if msg.ChannelKey != "oc_chat:topic:om_root" {
		t.Fatalf("ChannelKey = %q, want topic scope", msg.ChannelKey)
	}
	if msg.LegacyChannelKey != "oc_chat" {
		t.Fatalf("LegacyChannelKey = %q, want chat default", msg.LegacyChannelKey)
	}
}

func TestPopulateWorkspaceChannelKeysKeepsChatScopeOutsideTopicMode(t *testing.T) {
	p := &Platform{platformName: "lark", threadIsolation: false}
	msg := &core.Message{ReplyCtx: replyContext{
		chatID:     "oc_chat",
		sessionKey: "lark:oc_chat:ou_user",
	}}

	p.populateWorkspaceChannelKeys(msg)

	if msg.ChannelKey != "oc_chat" {
		t.Fatalf("ChannelKey = %q, want chat scope", msg.ChannelKey)
	}
	if msg.LegacyChannelKey != "" {
		t.Fatalf("LegacyChannelKey = %q, want empty", msg.LegacyChannelKey)
	}
}
