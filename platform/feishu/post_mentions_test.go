package feishu

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func postMentionForTest(key, openID, name string) *larkim.MentionEvent {
	return &larkim.MentionEvent{Key: &key, Id: &larkim.UserId{OpenId: &openID}, Name: &name}
}

// Issue #1887: bot placeholders must be resolved before the post parser turns
// them into display names, otherwise /new no longer starts with a slash.
func TestDispatchPostMention_PreservesSlashCommand(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("localeWrapped=%v", wrapped), func(t *testing.T) {
			var got *core.Message
			p := &Platform{platformName: "feishu", botOpenID: "ou_bot", handler: func(_ core.Platform, msg *core.Message) {
				got = msg
			}}
			p.userNameCache.Store("ou_user", "Tester")
			p.chatNameCache.Store("oc_chat", "Group")
			content := `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":"ExampleBot"},{"tag":"text","text":" /new incident-triage"}]]}`
			if wrapped {
				content = `{"zh_cn":` + content + `}`
			}
			p.dispatchMessage(context.Background(), "post", content,
				[]*larkim.MentionEvent{postMentionForTest("@_user_1", "ou_bot", "ExampleBot")},
				"om_post", "feishu:oc_chat:ou_user", "ou_user", "oc_chat", replyContext{}, "", 0)
			if got == nil || got.Content != "/new incident-triage" {
				t.Fatalf("dispatch = %#v, want slash command without the bot mention", got)
			}
		})
	}
}

func TestDispatchPostMention_PreservesOtherUsersAndEveryone(t *testing.T) {
	var got *core.Message
	p := &Platform{platformName: "feishu", botOpenID: "ou_bot", handler: func(_ core.Platform, msg *core.Message) {
		got = msg
	}}
	p.userNameCache.Store("ou_user", "Tester")
	p.chatNameCache.Store("oc_chat", "Group")
	content := `{"content":[[{"tag":"at","user_id":"@_user_1","user_name":"ExampleBot"},{"tag":"at","user_id":"@_user_2","user_name":"ExampleBot"},{"tag":"at","user_id":"all"},{"tag":"text","text":"hello"}]]}`
	p.dispatchMessage(context.Background(), "post", content,
		[]*larkim.MentionEvent{
			postMentionForTest("@_user_1", "ou_bot", "ExampleBot"),
			postMentionForTest("@_user_2", "ou_other", "ExampleBot"),
		}, "om_post", "feishu:oc_chat:ou_user", "ou_user", "oc_chat", replyContext{}, "", 0)
	if got == nil || got.Content != "@ExampleBot\n@all\nhello" {
		t.Fatalf("dispatch = %#v, want other user's same-name mention and @all preserved", got)
	}
}

func TestPostMention_DirectOpenIDStillRemoved(t *testing.T) {
	p := &Platform{botOpenID: "ou_bot"}
	parts, _ := p.parsePostContent("", `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"Bot"},{"tag":"text","text":"/help"}]]}`)
	if got := strings.Join(parts, "\n"); got != "/help" {
		t.Fatalf("got %q, want /help", got)
	}
}
