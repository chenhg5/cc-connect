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
)

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("New() error = nil, want missing credentials error")
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
	groups, ok := gotPayload["togroups"].([]any)
	if !ok || len(groups) != 1 || groups[0] != "g1" {
		t.Fatalf("togroups = %#v", gotPayload["togroups"])
	}
	ats, ok := gotPayload["at"].([]any)
	if !ok || len(ats) != 1 || ats[0] != "alice" {
		t.Fatalf("at = %#v", gotPayload["at"])
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}
	rc, err := p.ReconstructReplyCtx("tuitui:channel:teams_team-1_chan-1_root-1")
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
