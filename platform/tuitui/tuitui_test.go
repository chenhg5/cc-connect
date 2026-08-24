package tuitui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("New() error = nil, want missing credentials error")
	}
}

func TestNewRejectsInvalidGroupPolicy(t *testing.T) {
	_, err := New(map[string]any{
		"app_id":       "app",
		"app_secret":   "secret",
		"group_policy": "opne",
	})
	if err == nil {
		t.Fatal("New() error = nil, want invalid group_policy error")
	}
}

func TestPostJSONRedactsAppSecretFromTransportError(t *testing.T) {
	const secret = "secret-value"
	p := &Platform{
		appID:     "app",
		appSecret: secret,
		apiBase:   "https://example.invalid",
		client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("request %s failed", req.URL.String())
		})},
	}
	err := p.postJSON(context.Background(), "/robot/test", map[string]any{}, nil)
	if err == nil {
		t.Fatal("postJSON() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked app secret: %v", err)
	}
}

func TestDispatchEventDoesNotBlockCaller(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		dispatchEvent(func() {
			close(started)
			<-release
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatchEvent blocked the caller")
	}
	<-started
	close(release)
}

func TestBuildEnvelopeGroupMessage(t *testing.T) {
	frame := &tuituiFrame{
		Body: tuituiBody{
			Event:    "group_chat",
			User:     "Alice",
			UserName: "Alice Zhang",
			Data: tuituiData{
				MsgType:   "text",
				Text:      "@bot hello",
				MsgID:     "m1",
				GroupID:   "7652669648832580",
				GroupName: "Ops",
				AtMe:      true,
			},
		},
	}

	env := buildEnvelope(frame)
	if env.chatType != chatTypeGroup {
		t.Fatalf("chatType = %q, want %q", env.chatType, chatTypeGroup)
	}
	if env.chatID != "7652669648832580" {
		t.Fatalf("chatID = %q", env.chatID)
	}
	if env.senderID != "alice" {
		t.Fatalf("senderID = %q, want normalized alice", env.senderID)
	}
	if env.text != "@bot hello" {
		t.Fatalf("text = %q", env.text)
	}
	if !env.atMe {
		t.Fatal("atMe = false, want true")
	}
}

func TestBuildEnvelopeChannelThread(t *testing.T) {
	frame := &tuituiFrame{
		Body: tuituiBody{
			Event: "teams_post_create",
			User:  "Bob",
			Data: tuituiData{
				Content:   "channel post",
				PostID:    "post-2",
				ParentID:  "root-1",
				TeamID:    "team-1",
				ChannelID: "chan-1",
			},
		},
	}

	env := buildEnvelope(frame)
	if env.chatType != chatTypeChannel {
		t.Fatalf("chatType = %q, want channel", env.chatType)
	}
	if env.chatID != "teams_team-1_chan-1_root-1" {
		t.Fatalf("chatID = %q", env.chatID)
	}
	if env.messageID != "post-2" {
		t.Fatalf("messageID = %q", env.messageID)
	}
	if env.text != "channel post" {
		t.Fatalf("text = %q", env.text)
	}
}

func TestBuildMessageBodyIncludesReferenceAndMergedMessages(t *testing.T) {
	body := buildMessageBody(tuituiData{
		MsgType: "merged",
		Ref: &tuituiData{
			MsgType:     "file",
			UserName:    "Alice",
			UserAccount: "alice",
			File: tuituiFile{
				Name: "notes.txt",
				URL:  "https://example.test/notes.txt",
			},
		},
		Merged: &tuituiMerged{
			Source: "project chat",
			Messages: []tuituiData{
				{
					MsgType:     "text",
					Text:        "first decision",
					UserName:    "Bob",
					UserAccount: "bob",
					Timestamp:   "1710000000",
				},
				{
					MsgType:     "merged",
					UserName:    "Carol",
					UserAccount: "carol",
					Merged: &tuituiMerged{
						Source:   "nested chat",
						Messages: []tuituiData{{MsgType: "text", Text: "nested detail", UserName: "Dave", UserAccount: "dave"}},
					},
				},
			},
		},
	})

	for _, want := range []string{
		"[file] notes.txt: https://example.test/notes.txt",
		"Alice (alice)",
		"[forwarded chat: project chat]",
		"Bob (bob)",
		"first decision",
		"[forwarded chat: nested chat]",
		"nested detail",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestHandleEventDefersUnmentionedGroupHistoryUntilMention(t *testing.T) {
	var messages []*core.Message
	p := &Platform{
		groupAllowFrom:      "g1",
		groupPolicy:         "allowlist",
		requireMention:      true,
		pendingHistoryLimit: 50,
		handler: func(_ core.Platform, msg *core.Message) {
			messages = append(messages, msg)
		},
	}

	p.handleEvent(context.Background(), &tuituiFrame{Body: tuituiBody{
		Event:    "group_chat",
		User:     "alice",
		UserName: "Alice",
		Data:     tuituiData{MsgType: "text", Text: "the key is in message one", MsgID: "m1", GroupID: "g1"},
	}})
	if len(messages) != 0 {
		t.Fatalf("unmentioned message dispatched: %#v", messages)
	}

	p.handleEvent(context.Background(), &tuituiFrame{Body: tuituiBody{
		Event:    "group_chat",
		User:     "bob",
		UserName: "Bob",
		Data:     tuituiData{MsgType: "text", Text: "@bot where is the key?", MsgID: "m2", GroupID: "g1", AtMe: true},
	}})
	if len(messages) != 1 {
		t.Fatalf("mentioned messages = %d, want 1", len(messages))
	}
	if messages[0].Content != "@bot where is the key?" {
		t.Fatalf("content = %q", messages[0].Content)
	}
	for _, want := range []string{"Recent TuiTui messages", "Alice (alice)", "the key is in message one"} {
		if !strings.Contains(messages[0].ExtraContent, want) {
			t.Fatalf("extra content missing %q: %q", want, messages[0].ExtraContent)
		}
	}
	if messages[0].OnAccepted == nil {
		t.Fatal("mentioned message missing history acceptance callback")
	}

	p.handleEvent(context.Background(), &tuituiFrame{Body: tuituiBody{
		Event:    "group_chat",
		User:     "bob",
		UserName: "Bob",
		Data:     tuituiData{MsgType: "text", Text: "@bot again", MsgID: "m3", GroupID: "g1", AtMe: true},
	}})
	if len(messages) != 2 {
		t.Fatalf("mentioned messages = %d, want 2", len(messages))
	}
	if !strings.Contains(messages[1].ExtraContent, "the key is in message one") {
		t.Fatalf("unaccepted history was lost: %q", messages[1].ExtraContent)
	}
	messages[1].OnAccepted()

	p.handleEvent(context.Background(), &tuituiFrame{Body: tuituiBody{
		Event:    "group_chat",
		User:     "bob",
		UserName: "Bob",
		Data:     tuituiData{MsgType: "text", Text: "@bot third", MsgID: "m4", GroupID: "g1", AtMe: true},
	}})
	if len(messages) != 3 {
		t.Fatalf("mentioned messages = %d, want 3", len(messages))
	}
	if messages[2].ExtraContent != "" {
		t.Fatalf("accepted history was not consumed: %q", messages[2].ExtraContent)
	}
}

func TestFormattingInstructionsPreferInjectedHistory(t *testing.T) {
	prompt := (&Platform{}).FormattingInstructions()
	for _, want := range []string{
		"Recent TuiTui messages",
		"authoritative recent chat context",
		"answer directly without calling the history CLI",
		"missing or insufficient",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("FormattingInstructions() missing %q:\n%s", want, prompt)
		}
	}
}

func TestFetchHistoryGroup(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"errcode":0,"cursor":"c2","has_more":true,"msgs":[{"user_account":"alice","user_name":"Alice","timestamp":1710000000,"data":{"text":"hello","msgid":"m1","group_id":"g1"}}]}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	asc := true
	got, err := p.FetchHistory(context.Background(), "g1", chatTypeGroup, HistoryOptions{RelativeTime: "today", Limit: 10, OrderAsc: &asc})
	if err != nil {
		t.Fatalf("FetchHistory() error = %v", err)
	}
	if gotPath != "/robot/message/group/sync" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotPayload["group_id"] != "g1" || gotPayload["relative_time"] != "today" || gotPayload["limit"].(float64) != 10 {
		t.Fatalf("payload = %#v", gotPayload)
	}
	if !got.HasMore || got.Cursor != "c2" || len(got.Messages) != 1 || got.Messages[0]["msgid"] != nil {
		t.Fatalf("history result = %#v", got)
	}
	if got.Messages[0]["text"] != "hello" || got.Messages[0]["user_account"] != "alice" {
		t.Fatalf("message = %#v", got.Messages[0])
	}
}

func TestFetchHistoryGroupParsesLargeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robot/message/group/sync" {
			t.Fatalf("path = %q, want group history", r.URL.Path)
		}
		longText := strings.Repeat("x", 5000)
		resp := map[string]any{
			"errcode":  0,
			"errmsg":   "ok",
			"cursor":   "next",
			"has_more": false,
			"msgs": []map[string]any{
				{
					"user_account": "bot",
					"user_name":    "Bot",
					"timestamp":    1710000000,
					"data": map[string]any{
						"text": longText,
					},
				},
				{
					"user_account": "alice",
					"user_name":    "Alice",
					"timestamp":    1710000001,
					"data": map[string]any{
						"msg_type": "file",
						"file": map[string]any{
							"name": "sslkeylog.log",
							"url":  "https://example.test/sslkeylog.log",
						},
					},
				},
			},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	got, err := p.FetchHistory(context.Background(), "g1", chatTypeGroup, HistoryOptions{Limit: 100})
	if err != nil {
		t.Fatalf("FetchHistory() error = %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(got.Messages))
	}
	file, ok := got.Messages[1]["file"].(map[string]any)
	if !ok {
		t.Fatalf("file = %#v", got.Messages[1]["file"])
	}
	if file["name"] != "sslkeylog.log" || file["url"] != "https://example.test/sslkeylog.log" {
		t.Fatalf("file = %#v", file)
	}
}

func TestFetchHistoryChannel(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/robot/teams/post/topic/list":
			_, _ = w.Write([]byte(`{"errcode":0,"datas":{"post_list":[{"topic":{"post_id":"p1","from_name":"Alice","create_time":1710000000000,"last_reply_time":1710000000000,"content":"topic","properties":{"files":[{"name":"a.txt","url":"https://example/a.txt"}]}},"reply_list":[{"post_id":"p2","from_name":"Bob","create_time":1710000001000,"content":"reply"}]}]}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	got, err := p.FetchHistory(context.Background(), "teams_team-1_chan-1_root-1", chatTypeChannel, HistoryOptions{Limit: 20})
	if err != nil {
		t.Fatalf("FetchHistory(channel) error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/robot/teams/post/topic/list" {
		t.Fatalf("paths = %#v", paths)
	}
	if len(got.Threads) != 1 || !strings.Contains(got.Threads[0], "文件 a.txt: https://example/a.txt") {
		t.Fatalf("threads = %#v", got.Threads)
	}
	if got.Cursor != "1710000000001" {
		t.Fatalf("cursor = %q", got.Cursor)
	}
}

func TestFetchHistoryChannelDescendingCursorUsesEndTimestamp(t *testing.T) {
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		payloads = append(payloads, payload)
		_, _ = w.Write([]byte(`{"errcode":0,"datas":{"post_list":[{"topic":{"last_reply_time":1710000000000}}]}}`))
	}))
	defer server.Close()

	desc := false
	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	first, err := p.FetchHistory(context.Background(), "teams_team-1_chan-1", chatTypeChannel, HistoryOptions{Limit: 1, OrderAsc: &desc})
	if err != nil {
		t.Fatalf("first FetchHistory() error = %v", err)
	}
	if first.Cursor != "1709999999999" {
		t.Fatalf("cursor = %q", first.Cursor)
	}
	if _, err := p.FetchHistory(context.Background(), "teams_team-1_chan-1", chatTypeChannel, HistoryOptions{Limit: 1, OrderAsc: &desc, Cursor: first.Cursor}); err != nil {
		t.Fatalf("second FetchHistory() error = %v", err)
	}
	if got := payloads[1]["end_timestamp"]; got != float64(1709999999999) {
		t.Fatalf("end_timestamp = %#v", got)
	}
	if _, ok := payloads[1]["from_timestamp"]; ok {
		t.Fatalf("descending cursor set from_timestamp: %#v", payloads[1])
	}
}

func TestPolicyGroupRequiresMentionAndAllowlist(t *testing.T) {
	p := &Platform{
		allowFrom:      "alice",
		groupAllowFrom: "g1",
		groupPolicy:    "allowlist",
		requireMention: true,
	}

	if !p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "g1", senderID: "alice", atMe: true}) {
		t.Fatal("allowed sender with mention should pass")
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "g1", senderID: "alice", atMe: false}) {
		t.Fatal("group message without mention should be denied")
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "g2", senderID: "bob", atMe: true}) {
		t.Fatal("unlisted group and user should be denied")
	}
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "g1", senderID: "bob", atMe: true}) {
		t.Fatal("allowlisted group with mention should pass")
	}
}

func TestPolicyEmptyGroupAllowlistDoesNotAllowAll(t *testing.T) {
	p := &Platform{groupPolicy: "allowlist", requireMention: false}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "g1", senderID: "bob"}) {
		t.Fatal("empty group_allow_from should not allow all groups")
	}
}

func TestPolicyWildcardUserAllowlistDoesNotOpenGroups(t *testing.T) {
	p := &Platform{
		allowFrom:      "*",
		groupAllowFrom: "trusted-group",
		groupPolicy:    "allowlist",
		requireMention: false,
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "untrusted-group", senderID: "bob"}) {
		t.Fatal("allow_from=* should not bypass group_allow_from")
	}
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "trusted-group", senderID: "bob"}) {
		t.Fatal("configured group_allow_from should allow the group")
	}
}

func TestPolicyExplicitUserAllowlistCanUseAnyMentionedGroup(t *testing.T) {
	p := &Platform{
		allowFrom:      "alice",
		groupAllowFrom: "trusted-group",
		groupPolicy:    "allowlist",
		requireMention: true,
	}
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "untrusted-group", senderID: "alice", atMe: true}) {
		t.Fatal("explicit allow_from user should be allowed in any group when mentioned")
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeGroup, chatID: "untrusted-group", senderID: "alice", atMe: false}) {
		t.Fatal("explicit allow_from user should still require mention in group chats")
	}
}

func TestPolicyOpenDoesNotBypassChannelAllowlist(t *testing.T) {
	p := &Platform{
		groupPolicy: "open",
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeChannel, teamID: "team-1", channelID: "chan-1", senderID: "bob"}) {
		t.Fatal("group_policy=open should not allow unlisted channel posts")
	}

	p.groupAllowFrom = "chan-1"
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeChannel, teamID: "team-1", channelID: "chan-1", senderID: "bob"}) {
		t.Fatal("group_allow_from should allow channel posts")
	}
}

func TestPolicyChannelRequiresMentionWhenConfigured(t *testing.T) {
	p := &Platform{
		groupAllowFrom:  "chan-1",
		groupPolicy:     "allowlist",
		requireMention:  true,
		receiveReaction: "收到",
	}
	if p.isAllowed(inboundEnvelope{chatType: chatTypeChannel, teamID: "team-1", channelID: "chan-1", senderID: "bob", atMe: false}) {
		t.Fatal("allowlisted channel post without mention should be denied")
	}
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeChannel, teamID: "team-1", channelID: "chan-1", senderID: "bob", atMe: true}) {
		t.Fatal("allowlisted channel post with mention should pass")
	}
}

func TestPolicyChannelMentionCanBeDisabled(t *testing.T) {
	p := &Platform{
		groupAllowFrom: "chan-1",
		groupPolicy:    "allowlist",
		requireMention: false,
	}
	if !p.isAllowed(inboundEnvelope{chatType: chatTypeChannel, teamID: "team-1", channelID: "chan-1", senderID: "bob", atMe: false}) {
		t.Fatal("allowlisted channel post should pass when require_mention is false")
	}
}

func TestSessionKeyUsesCoreChannelFormat(t *testing.T) {
	p := &Platform{}
	if got := p.sessionKey(inboundEnvelope{chatType: chatTypeDirect, chatID: "alice", senderID: "alice"}); got != "tuitui:alice" {
		t.Fatalf("direct session key = %q", got)
	}
	if got := p.sessionKey(inboundEnvelope{chatType: chatTypeGroup, chatID: "g1", senderID: "alice"}); got != "tuitui:g1:alice" {
		t.Fatalf("group session key = %q", got)
	}
	if got := p.sessionKey(inboundEnvelope{chatType: chatTypeChannel, chatID: "teams_team-1_chan-1_root-1", senderID: "alice"}); got != "tuitui:teams_team-1_chan-1_root-1" {
		t.Fatalf("channel session key = %q", got)
	}
}

func TestSendTextGroupPayload(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	err := p.Send(context.Background(), replyContext{chatID: "g1", chatType: chatTypeGroup}, "hi @alice")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotPath != "/robot/message/custom/send" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "appid=app") || !strings.Contains(gotQuery, "secret=secret") {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotPayload["msgtype"] != "text" {
		t.Fatalf("msgtype = %v", gotPayload["msgtype"])
	}
	text, ok := gotPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("text = %#v", gotPayload["text"])
	}
	if got := text["content"]; got != "hi @alice" {
		t.Fatalf("text content = %#v", got)
	}
	groups, ok := gotPayload["togroups"].([]any)
	if !ok || len(groups) != 1 || groups[0] != "g1" {
		t.Fatalf("togroups = %#v", gotPayload["togroups"])
	}
	ats, ok := gotPayload["at"].([]any)
	if !ok || len(ats) != 1 || ats[0] != "alice" {
		t.Fatalf("at = %#v", gotPayload["at"])
	}
}

func TestSendChannelPostBuildsTeamsMarkdownPayload(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	err := p.SendChannelPost(context.Background(), "teams_team-1_chan-1_root-1", "hello @alice", "")
	if err != nil {
		t.Fatalf("SendChannelPost() error = %v", err)
	}
	if gotPath != "/robot/message/custom/send" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotPayload["msgtype"] != "richtext/markdown" {
		t.Fatalf("msgtype = %v", gotPayload["msgtype"])
	}
	richtext, ok := gotPayload["richtext"].(map[string]any)
	if !ok {
		t.Fatalf("richtext = %#v", gotPayload["richtext"])
	}
	if richtext["markdown"] != `hello {{tuitui_at "alice"}}` {
		t.Fatalf("markdown = %#v", richtext["markdown"])
	}
	if richtext["delims_left"] != "{{" || richtext["delims_right"] != "}}" {
		t.Fatalf("richtext delimiters = %#v", richtext)
	}
	teams, ok := gotPayload["toteams"].([]any)
	if !ok || len(teams) != 1 {
		t.Fatalf("toteams = %#v", gotPayload["toteams"])
	}
	target, ok := teams[0].(map[string]any)
	if !ok {
		t.Fatalf("team target = %#v", teams[0])
	}
	if target["team_id"] != "team-1" || target["channel_id"] != "chan-1" || target["parent_id"] != "root-1" || target["post_id"] != "" {
		t.Fatalf("team target = %#v", target)
	}
}

func TestSendChannelPostAddsParentToQualifiedChannelID(t *testing.T) {
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	if err := p.SendChannelPost(context.Background(), "teams_team-1_chan-1", "thread reply", "root-1"); err != nil {
		t.Fatalf("SendChannelPost() error = %v", err)
	}
	teams := gotPayload["toteams"].([]any)
	target := teams[0].(map[string]any)
	if target["team_id"] != "team-1" || target["channel_id"] != "chan-1" || target["parent_id"] != "root-1" {
		t.Fatalf("team target = %#v", target)
	}
}

func TestSendChannelPostLooksUpTeamForPlainChannelID(t *testing.T) {
	var sendPayload map[string]any
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/robot/teams/channel/info":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode channel info payload: %v", err)
			}
			if payload["channel_id"] != "chan-1" {
				t.Fatalf("channel info payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"errcode":0,"datas":{"info":{"team_id":"team-1","name":"General"}}}`))
		case "/robot/message/custom/send":
			if err := json.NewDecoder(r.Body).Decode(&sendPayload); err != nil {
				t.Fatalf("decode send payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"errcode":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	err := p.SendChannelPost(context.Background(), "chan-1", "new post", "")
	if err != nil {
		t.Fatalf("SendChannelPost() error = %v", err)
	}
	if strings.Join(paths, ",") != "/robot/teams/channel/info,/robot/message/custom/send" {
		t.Fatalf("paths = %#v", paths)
	}
	teams, ok := sendPayload["toteams"].([]any)
	if !ok || len(teams) != 1 {
		t.Fatalf("toteams = %#v", sendPayload["toteams"])
	}
	target := teams[0].(map[string]any)
	if target["team_id"] != "team-1" || target["channel_id"] != "chan-1" {
		t.Fatalf("team target = %#v", target)
	}
	if _, ok := target["parent_id"]; ok {
		t.Fatalf("parent_id should be omitted for a new post: %#v", target)
	}
}

func TestSendMediaIDRemembersOutboundMessageID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"msgids":[{"group":"g1","msgid":"m-media"}]}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	if err := p.sendMediaID(context.Background(), replyContext{chatID: "g1", chatType: chatTypeGroup}, "media-1", "a.png", true); err != nil {
		t.Fatalf("sendMediaID() error = %v", err)
	}
	if !p.isRecentOutboundEcho(inboundEnvelope{messageID: "m-media"}) {
		t.Fatal("media message id was not remembered for echo suppression")
	}
}

type controlWriterStub struct {
	messageType int
	data        []byte
}

func (w *controlWriterStub) WriteControl(messageType int, data []byte, _ time.Time) error {
	w.messageType = messageType
	w.data = append([]byte(nil), data...)
	return nil
}

func TestWriteNormalClosureUsesControlFrame(t *testing.T) {
	w := &controlWriterStub{}
	if err := writeNormalClosure(w); err != nil {
		t.Fatalf("writeNormalClosure() error = %v", err)
	}
	if w.messageType != websocket.CloseMessage {
		t.Fatalf("message type = %d", w.messageType)
	}
}

func TestReactToMessageGroupPayload(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	p := &Platform{appID: "app", appSecret: "secret", apiBase: server.URL, client: server.Client()}
	err := p.reactToMessage(context.Background(), replyContext{chatID: "g1", chatType: chatTypeGroup, messageID: "m1"}, "收到")
	if err != nil {
		t.Fatalf("reactToMessage() error = %v", err)
	}
	if gotPath != "/robot/message/custom/modify" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotPayload["msgtype"] != "emoji_reaction" {
		t.Fatalf("msgtype = %v", gotPayload["msgtype"])
	}
	reaction, ok := gotPayload["emoji_reaction"].(map[string]any)
	if !ok {
		t.Fatalf("emoji_reaction = %#v", gotPayload["emoji_reaction"])
	}
	if reaction["emoji"] != "收到" || reaction["cancel"] != false {
		t.Fatalf("emoji_reaction = %#v", reaction)
	}
	groups, ok := gotPayload["togroups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("togroups = %#v", gotPayload["togroups"])
	}
	group, ok := groups[0].(map[string]any)
	if !ok || group["group"] != "g1" || group["msgid"] != "m1" {
		t.Fatalf("group target = %#v", groups[0])
	}
}

func TestHandleEventReactsAfterPolicyAllowsMessage(t *testing.T) {
	gotReaction := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robot/message/custom/modify" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		gotReaction <- payload
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	gotMessage := make(chan string, 1)
	p := &Platform{
		appID:           "app",
		appSecret:       "secret",
		apiBase:         server.URL,
		client:          server.Client(),
		groupAllowFrom:  "g1",
		groupPolicy:     "allowlist",
		receiveReaction: "收到",
		requireMention:  true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		gotMessage <- msg.Content
	}

	p.handleEvent(context.Background(), &tuituiFrame{
		ID: "event-1",
		Body: tuituiBody{
			Event: "group_chat",
			User:  "alice",
			Data: tuituiData{
				MsgType: "text",
				Text:    "@bot hello",
				MsgID:   "m1",
				GroupID: "g1",
				AtMe:    true,
			},
		},
	})

	select {
	case got := <-gotMessage:
		if got != "@bot hello" {
			t.Fatalf("message content = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
	select {
	case payload := <-gotReaction:
		if payload["msgtype"] != "emoji_reaction" {
			t.Fatalf("reaction payload = %#v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("receive reaction was not sent")
	}
}

func TestHandleEventAllowsOtherBotMentions(t *testing.T) {
	gotMessage := make(chan *core.Message, 1)
	p := &Platform{
		groupAllowFrom: "g1",
		groupPolicy:    "allowlist",
		requireMention: true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		gotMessage <- msg
	}

	p.handleEvent(context.Background(), &tuituiFrame{
		ID: "event-other-bot",
		Body: tuituiBody{
			Event:    "group_chat",
			User:     "bot-other",
			UserName: "Other Agent",
			Data: tuituiData{
				MsgType: "text",
				Text:    "@bot please continue",
				MsgID:   "m-other-bot",
				GroupID: "g1",
				AtMe:    true,
			},
		},
	})

	select {
	case msg := <-gotMessage:
		if msg.UserID != "bot-other" || msg.Content != "@bot please continue" {
			t.Fatalf("message = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called for other bot mention")
	}
}

func TestHandleEventIgnoresConfiguredBotAccount(t *testing.T) {
	p := &Platform{
		groupAllowFrom: "g1",
		ignoreFrom:     "bot-32x0Lkiq",
		groupPolicy:    "allowlist",
		requireMention: true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		t.Fatalf("handler called for configured ignored bot: %#v", msg)
	}

	p.handleEvent(context.Background(), &tuituiFrame{
		ID: "event-own-bot",
		Body: tuituiBody{
			Event:    "group_chat",
			User:     "bot-32x0Lkiq",
			UserName: "张志磊的群聊小助理",
			Data: tuituiData{
				MsgType: "text",
				Text:    "@bot probe",
				MsgID:   "m-own-bot",
				GroupID: "g1",
				AtMe:    true,
			},
		},
	})
}

func TestHandleEventIgnoresRecentOutboundEcho(t *testing.T) {
	var sendCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robot/message/custom/send" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		sendCalled = true
		_, _ = w.Write([]byte(`{"errcode":0,"msgids":[{"group":"g1","msgid":"m-echo"}]}`))
	}))
	defer server.Close()

	p := &Platform{
		appID:           "app",
		appSecret:       "secret",
		apiBase:         server.URL,
		client:          server.Client(),
		groupAllowFrom:  "g1",
		groupPolicy:     "allowlist",
		receiveReaction: "收到",
		requireMention:  true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		t.Fatalf("handler called for outbound echo: %#v", msg)
	}

	if err := p.Send(context.Background(), replyContext{chatID: "g1", chatType: chatTypeGroup}, "@bot probe"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sendCalled {
		t.Fatal("send API was not called")
	}

	p.handleEvent(context.Background(), &tuituiFrame{
		ID: "event-echo",
		Body: tuituiBody{
			Event:    "group_chat",
			User:     "bot-32x0Lkiq",
			UserName: "张志磊的群聊小助理",
			Data: tuituiData{
				MsgType: "text",
				Text:    "@bot probe",
				MsgID:   "m-echo",
				GroupID: "g1",
				AtMe:    true,
			},
		},
	})
}

func TestHandleEventDoesNotTreatMatchingHumanTextAsOutboundEcho(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"msgids":[{"group":"g1","msgid":"m-outbound"}]}`))
	}))
	defer server.Close()

	gotMessage := make(chan *core.Message, 1)
	p := &Platform{
		appID:          "app",
		appSecret:      "secret",
		apiBase:        server.URL,
		client:         server.Client(),
		groupAllowFrom: "g1",
		groupPolicy:    "allowlist",
		requireMention: true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) { gotMessage <- msg }

	if err := p.Send(context.Background(), replyContext{chatID: "g1", chatType: chatTypeGroup}, "@bot probe"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	p.handleEvent(context.Background(), &tuituiFrame{
		ID: "event-human",
		Body: tuituiBody{
			Event:    "group_chat",
			User:     "alice",
			UserName: "Alice",
			Data: tuituiData{
				MsgType: "text",
				Text:    "@bot probe",
				MsgID:   "m-human",
				GroupID: "g1",
				AtMe:    true,
			},
		},
	})

	select {
	case msg := <-gotMessage:
		if msg.UserID != "alice" {
			t.Fatalf("message = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called for matching human text")
	}
}

func TestExtractMentionsTrimsTrailingPunctuation(t *testing.T) {
	got := extractMentions("hi @alice, please ask @bob。and @carol!")
	want := []string{"alice", "bob", "carol"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("extractMentions() = %#v, want %#v", got, want)
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}
	rc, err := p.ReconstructReplyCtx("tuitui:teams_team-1_chan-1_root-1")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	got := rc.(replyContext)
	if got.chatType != chatTypeChannel || got.chatID != "teams_team-1_chan-1_root-1" {
		t.Fatalf("reply context = %#v", got)
	}
}

func TestRealTuiTuiConnection(t *testing.T) {
	if os.Getenv("TUITUI_REAL") != "1" {
		t.Skip("set TUITUI_REAL=1 with TUITUI_APP_ID/TUITUI_APP_SECRET to run")
	}
	appID := os.Getenv("TUITUI_APP_ID")
	appSecret := os.Getenv("TUITUI_APP_SECRET")
	if appID == "" || appSecret == "" {
		t.Fatal("TUITUI_APP_ID and TUITUI_APP_SECRET are required")
	}
	platform, err := New(map[string]any{
		"app_id":     appID,
		"app_secret": appSecret,
		"allow_from": "*",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := platform.(*Platform)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = p.runWS(ctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("runWS() failed before context timeout: %v", err)
	}
}
