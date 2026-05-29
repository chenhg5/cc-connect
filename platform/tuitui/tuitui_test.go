package tuitui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

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
