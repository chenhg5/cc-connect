package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func managedGroupPolicy(key, user string) core.GroupMessagePolicy {
	if !strings.Contains(key, ":oc_main:") {
		return core.GroupMessagePolicy{}
	}
	return core.GroupMessagePolicy{RequireMention: user != "ou_alice", DefaultReceiver: user == "ou_alice"}
}

func TestTeamCoordination_UnmentionedHumansOnlyInManagedProject(t *testing.T) {
	received := make(chan *core.Message, 8)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, m *core.Message) { received <- m })
	p.SetGroupMessagePolicy(managedGroupPolicy)
	tests := []struct {
		name, chat, sender   string
		mention, other, want bool
	}{
		{"ordinary", "oc_main", "user", false, false, true},
		{"another bot addressed", "oc_main", "user", true, true, false},
		{"manager addressed", "oc_main", "user", true, false, true},
		{"unrelated group", "oc_allowed", "user", false, false, false},
		{"own echo", "oc_main", "app", false, false, false},
		{"member echo", "oc_main", "app", true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := makeGroupHistoryEvent(t, "om_"+tc.name, "ou_alice", "text", `{"text":"please continue"}`, tc.chat, "", tc.mention)
			ev.Event.Sender.SenderType = &tc.sender
			if tc.other {
				ev.Event.Message.Mentions[0].Id.OpenId = stringPtr("ou_other")
			}
			if err := p.onMessage(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			select {
			case <-received:
				if !tc.want {
					t.Fatal("unintended manager response")
				}
			case <-time.After(40 * time.Millisecond):
				if tc.want {
					t.Fatal("unmentioned human message was dropped")
				}
			}
		})
	}
}

func TestTeamCoordination_OtherMembersRequireDirectMention(t *testing.T) {
	tests := []struct {
		name, user, text, root, msgType, chat, sender                      string
		mention, other, replyAll, all, unknownBot, decision, private, want bool
	}{
		{name: "owner ordinary", user: "ou_alice", want: true},
		{name: "other human ordinary", user: "ou_bob"},
		{name: "other human command", user: "ou_bob", text: `/status`},
		{name: "text at is not a mention", user: "ou_bob", text: `@cc-connect please continue`},
		{name: "direct mention", user: "ou_bob", mention: true, want: true},
		{name: "another bot mentioned", user: "ou_bob", mention: true, other: true},
		{name: "owner addresses another bot", user: "ou_alice", mention: true, other: true},
		{name: "at all is not a direct mention", user: "ou_bob", text: `@_all please continue`, all: true},
		{name: "reply all cannot bypass", user: "ou_bob", replyAll: true},
		{name: "thread does not identify sender", user: "ou_bob", root: "om_thread"},
		{name: "thread direct mention", user: "ou_bob", root: "om_thread", mention: true, want: true},
		{name: "owner thread", user: "ou_alice", root: "om_thread", want: true},
		{name: "active thread attachment", user: "ou_bob", root: "om_thread", msgType: "file"},
		{name: "unknown bot fails closed", user: "ou_bob", unknownBot: true, replyAll: true},
		{name: "unknown sender cannot claim owner", user: "ou_alice", sender: "unknown"},
		{name: "decision reply cannot bypass", user: "ou_bob", root: "om_thread", decision: true},
		{name: "decision direct mention", user: "ou_bob", root: "om_thread", decision: true, mention: true, want: true},
		{name: "owner decision reply", user: "ou_alice", root: "om_thread", decision: true, want: true},
		{name: "private chat unchanged", user: "ou_bob", private: true, want: true},
		{name: "unrelated reply all unchanged", user: "ou_bob", chat: "oc_allowed", replyAll: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan *core.Message, 1)
			p := newGroupHistoryTestPlatform(func(_ core.Platform, m *core.Message) { received <- m })
			p.SetGroupMessagePolicy(managedGroupPolicy)
			requests := make(chan struct{}, 8)
			if tc.msgType == "file" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests <- struct{}{}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test","expire":7200,"data":{"message_id":"om_reply"}}`))
				}))
				t.Cleanup(server.Close)
				p.client = lark.NewClient("group-policy-test", "test", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
			}
			p.groupReplyAll, p.respondToAtEveryoneAndHere = tc.replyAll, tc.all
			p.threadIsolation = tc.root != ""
			if tc.unknownBot {
				p.botOpenID = ""
			}
			msgType := tc.msgType
			if msgType == "" {
				msgType = "text"
			}
			body := tc.text
			if body == "" {
				body = "please continue"
			}
			content := `{"text":"` + body + `"}`
			if msgType == "file" {
				// An admitted attachment would try to report a missing download key;
				// even this error reply must be suppressed for unmentioned members.
				content = `{"file_name":"notes.txt"}`
			}
			chat := tc.chat
			if chat == "" {
				chat = "oc_main"
			}
			ev := makeGroupHistoryEvent(t, "om_case", tc.user, msgType, content, chat, tc.root, tc.mention)
			if tc.private {
				ev.Event.Message.ChatType = stringPtr("p2p")
			}
			if tc.sender != "" {
				ev.Event.Sender.SenderType = &tc.sender
			}
			if tc.decision {
				p.decisionReplies.Store(tc.root, decisionReply{chat: chat, session: "feishu:oc_main:root:om_thread", user: tc.user, token: "decision"})
			}
			if tc.other {
				ev.Event.Message.Mentions[0].Id.OpenId = stringPtr("ou_other")
			}
			p.markThreadSessionActive(p.makeSessionKey(ev.Event.Message, chat, tc.user))
			if err := p.onMessage(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			select {
			case <-requests:
				t.Fatal("unmentioned attachment caused an API request or reply")
			case <-received:
				if !tc.want {
					t.Fatal("unmentioned group member triggered the bot")
				}
			case <-time.After(50 * time.Millisecond):
				if tc.want {
					t.Fatal("intentionally addressed message was dropped")
				}
			}
		})
	}
}
