package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// videoChatContent has the shape of a real video-meeting card (topic,
// meet_number, start_time in milliseconds as a string, end_time null); the
// values are made up.
const videoChatContent = `{"topic":"Weekly sync","meet_number":"123456789","start_time":"1792026635000","end_time":null}`

// 1792026635000 ms = 2026-10-15 01:10:35 UTC.
const videoChatText = "[video meeting: Weekly sync, meeting no. 123456789, started 2026-10-15 09:10 +08:00]"

func pinFeishuDisplayLocation(t *testing.T) {
	t.Helper()
	old := feishuDisplayLocation
	feishuDisplayLocation = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { feishuDisplayLocation = old })
}

func TestDescribeFeishuMessage(t *testing.T) {
	pinFeishuDisplayLocation(t)
	cases := []struct {
		name, msgType, content, want string
	}{
		{"video_chat", "video_chat", videoChatContent, videoChatText},
		{"video_chat with end time", "video_chat", `{"topic":"Weekly sync","start_time":"1792026635000","end_time":"1792029635000"}`,
			"[video meeting: Weekly sync, started 2026-10-15 09:10 +08:00, ended 2026-10-15 10:00 +08:00]"},
		{"share_chat", "share_chat", `{"chat_id":"oc_0dd200d32fda"}`, "[shared chat: oc_0dd200d32fda]"},
		{"share_user", "share_user", `{"user_id":"ou_0dd200d32f76"}`, "[shared contact: ou_0dd200d32f76]"},
		{"location", "location", `{"name":"Hangzhou, Zhejiang","longitude":"120.15","latitude":"30.27"}`,
			"[location: Hangzhou, Zhejiang (30.27, 120.15)]"},
		{"share_calendar_event", "share_calendar_event", `{"summary":"Design review","start_time":"1608265395000","end_time":"1608267015000"}`,
			"[calendar event: Design review, starts 2020-12-18 12:23 +08:00, ends 2020-12-18 12:50 +08:00]"},
		{"calendar invitation", "calendar", `{"summary":"Design review","start_time":"1608265395000","end_time":"1608267015000"}`,
			"[calendar event: Design review, starts 2020-12-18 12:23 +08:00, ends 2020-12-18 12:50 +08:00]"},
		{"general_calendar", "general_calendar", `{"summary":"Design review"}`, "[calendar event: Design review]"},
		{"todo", "todo", `{"task_id":"t1","summary":{"title":"","content":[[{"tag":"text","text":"Eat more fruits, "},{"tag":"text","text":"exercise more"}],[{"tag":"text","text":"and sleep"}]]},"due_time":"1623124318000"}`,
			"[task: Eat more fruits, exercise more and sleep, due 2021-06-08 11:51 +08:00]"},
		{"vote", "vote", `{"topic":"Lunch","options":["noodles","rice"]}`, "[vote: Lunch, options: noodles / rice]"},
		{"unknown type", "hongbao", `{"text":"[red envelope]"}`, "[unsupported Feishu message: hongbao]"},

		// Degraded payloads keep the type's label and whatever is usable.
		{"documented video_chat example (start_time is not a timestamp)", "video_chat", `{"topic":"Video call message","start_time":"6745784522794598413"}`,
			"[video meeting: Video call message]"},
		{"malformed content", "video_chat", `not json`, "[video meeting]"},
		{"empty content", "share_chat", ``, "[shared chat]"},
		{"location without coordinates", "location", `{"name":"Hangzhou","latitude":"30.27"}`, "[location: Hangzhou]"},
		{"todo without summary", "todo", `{"task_id":"t1"}`, "[task]"},
		{"meeting number as a JSON number", "video_chat", `{"meet_number":123456789}`, "[video meeting: meeting no. 123456789]"},
		{"multi-line topic", "vote", "{\"topic\":\"Lunch\\nplace\",\"options\":[]}", "[vote: Lunch place]"},
		{"long topic", "vote", `{"topic":"` + strings.Repeat("投", 250) + `"}`, "[vote: " + strings.Repeat("投", maxPlaceholderFieldRunes) + "…]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeFeishuMessage(tc.msgType, tc.content)
			if got != tc.want {
				t.Fatalf("describeFeishuMessage(%q, %s) = %q, want %q", tc.msgType, tc.content, got, tc.want)
			}
			if strings.Contains(got, "{") {
				t.Fatalf("describeFeishuMessage(%q) = %q leaks raw JSON", tc.msgType, got)
			}
		})
	}
}

// newMessageTypesPlatform returns a Platform backed by a fake Open API that
// answers name lookups and, for parentID, returns a quoted message of
// parentType with parentContent.
func newMessageTypesPlatform(t *testing.T, parentID, parentType, parentContent string, handler core.MessageHandler) *Platform {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "t-token"})
		case parentID != "" && r.URL.Path == "/open-apis/im/v1/messages/"+parentID:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"items": []map[string]any{{
				"msg_type": parentType,
				"sender":   map[string]any{"id": "ou_alice", "sender_type": "user"},
				"body":     map[string]any{"content": parentContent},
			}}}})
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"user": map[string]any{"name": "Alice"}}})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	t.Cleanup(srv.Close)
	return &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        "cli_msg_types",
		appSecret:    "secret-msg-types",
		botOpenID:    "ou_bot",
		dedup:        &core.MessageDedup{},
		client:       lark.NewClient("cli_msg_types", "secret-msg-types", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler:      handler,
	}
}

// A video-meeting card sent to the bot in a p2p chat used to fall through to
// the debug-level "ignoring unsupported message type": no reply, and at the
// default log level no log line either.
func TestOnMessage_VideoChatReachesAgent(t *testing.T) {
	pinFeishuDisplayLocation(t)
	got := make(chan *core.Message, 1)
	p := newMessageTypesPlatform(t, "", "", "", func(_ core.Platform, msg *core.Message) { got <- msg })

	chatType, msgType, senderType := "p2p", "video_chat", "user"
	content := videoChatContent
	createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: strPtr("ou_alice")}, SenderType: &senderType},
		Message: &larkim.EventMessage{
			MessageId: strPtr("om_vc"), ChatId: strPtr("oc_p2p"), ChatType: &chatType,
			MessageType: &msgType, Content: &content, CreateTime: &createTime,
		},
	}})
	if err != nil {
		t.Fatalf("onMessage: %v", err)
	}
	select {
	case msg := <-got:
		if msg.Content != videoChatText {
			t.Fatalf("Content = %q, want %q", msg.Content, videoChatText)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("video_chat message never reached the handler")
	}
}

func TestDispatchMessage_UnhandledMessageTypes(t *testing.T) {
	pinFeishuDisplayLocation(t)
	cases := []struct {
		msgType, content string
		want             string // "" = must not be dispatched
	}{
		{"share_chat", `{"chat_id":"oc_0dd200d32fda"}`, "[shared chat: oc_0dd200d32fda]"},
		{"share_user", `{"user_id":"ou_0dd200d32f76"}`, "[shared contact: ou_0dd200d32f76]"},
		{"location", `{"name":"Hangzhou","longitude":"120.15","latitude":"30.27"}`, "[location: Hangzhou (30.27, 120.15)]"},
		{"vote", `{"topic":"Lunch","options":["noodles","rice"]}`, "[vote: Lunch, options: noodles / rice]"},
		{"folder", `{"file_key":"fk","file_name":"docs"}`, "[unsupported Feishu message: folder]"},
		{"system", `{"template":"{from_user} invited {to_chatters} to this chat.","from_user":["bot"],"to_chatters":["Alice"]}`, ""},
		{"interactive", `{"title":"Alert","elements":[]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.msgType, func(t *testing.T) {
			var got []*core.Message
			p := newMessageTypesPlatform(t, "", "", "", func(_ core.Platform, msg *core.Message) { got = append(got, msg) })
			p.dispatchMessage(context.Background(), tc.msgType, tc.content, nil, "om_"+tc.msgType, "feishu:oc_p2p:ou_alice",
				"ou_alice", "oc_p2p", replyContext{messageID: "om_" + tc.msgType, chatID: "oc_p2p"}, "", 0)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("%s message dispatched with Content %q, want it ignored", tc.msgType, got[0].Content)
			case tc.want != "" && len(got) != 1:
				t.Fatalf("%s message dispatched %d times, want 1", tc.msgType, len(got))
			case tc.want != "" && got[0].Content != tc.want:
				t.Fatalf("%s Content = %q, want %q", tc.msgType, got[0].Content, tc.want)
			}
		})
	}
}

// A reply quoting a video-meeting card carries the card's details, not the
// bare "[video_chat]" placeholder. Unknown types keep the short placeholder.
func TestFetchQuotedMessage_DescribesUnhandledTypes(t *testing.T) {
	pinFeishuDisplayLocation(t)
	cases := []struct {
		parentType, parentContent, want, notWant string
	}{
		{"video_chat", videoChatContent, videoChatText, "[video_chat]"},
		{"hongbao", `{"text":"[red envelope]"}`, "[hongbao]", "{"},
	}
	for _, tc := range cases {
		t.Run(tc.parentType, func(t *testing.T) {
			var got []*core.Message
			p := newMessageTypesPlatform(t, "om_parent", tc.parentType, tc.parentContent, func(_ core.Platform, msg *core.Message) { got = append(got, msg) })
			p.dispatchMessage(context.Background(), "text", `{"text":"when does it start?"}`, nil, "om_reply", "feishu:oc_p2p:ou_alice",
				"ou_alice", "oc_p2p", replyContext{messageID: "om_reply", chatID: "oc_p2p"}, "om_parent", 0)
			if len(got) != 1 {
				t.Fatalf("reply dispatched %d times, want 1", len(got))
			}
			if q := got[0].ExtraContent; !strings.Contains(q, tc.want) || strings.Contains(q, tc.notWant) {
				t.Fatalf("quoted content = %q, want it to contain %q and not %q", q, tc.want, tc.notWant)
			}
		})
	}
}

// Sub-messages of a merge-forward bundle get the same descriptions; types
// without one keep the existing "[<type> message]" placeholder.
func TestFormatMergeForwardTree_DescribesUnhandledTypes(t *testing.T) {
	pinFeishuDisplayLocation(t)
	item := func(id, msgType, content string) *larkim.Message {
		return &larkim.Message{
			MessageId:  strPtr(id),
			MsgType:    strPtr(msgType),
			CreateTime: strPtr("1792027571969"),
			Sender:     &larkim.Sender{Id: strPtr("ou_alice")},
			Body:       &larkim.MessageBody{Content: strPtr(content)},
		}
	}
	children := map[string][]*larkim.Message{"om_root": {
		item("om_1", "video_chat", videoChatContent),
		item("om_2", "location", `{"name":"Hangzhou","longitude":"120.15","latitude":"30.27"}`),
		item("om_3", "hongbao", `{"text":"[red envelope]"}`),
		item("om_4", "audio", `{"file_key":"file_v3","duration":2000}`),
	}}
	var sb strings.Builder
	(&Platform{platformName: "feishu"}).formatMergeForwardTree("om_root", children, map[string]string{"ou_alice": "Alice"}, &sb, new([]core.ImageAttachment), new([]core.FileAttachment), 0)
	out := sb.String()
	for _, want := range []string{
		"Alice: " + videoChatText + "\n",
		"Alice: [location: Hangzhou (30.27, 120.15)]\n",
		"Alice: [hongbao message]\n",
		"Alice: [audio message]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("forwarded bundle = %q, want it to contain %q", out, want)
		}
	}
}
