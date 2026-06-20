package googlechat

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// newTestPlatform builds a Platform directly so tests can exercise parsing and
// routing without a Pub/Sub client or service-account client.
func newTestPlatform(allowFrom, trigger, scope string) *Platform {
	return &Platform{allowFrom: allowFrom, trigger: trigger, sessionScope: scope}
}

// wrapEvent renders a Chat-app event as the Pub/Sub message payload the Chat app
// publishes: {"type":<evType>,"message":<msg>}.
func wrapEvent(t *testing.T, evType string, msg map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": evType, "message": msg})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return data
}

// messageEvent wraps msg as a MESSAGE event line.
func messageEvent(t *testing.T, msg map[string]any) []byte {
	return wrapEvent(t, "MESSAGE", msg)
}

func humanMessage(text, argumentText string) map[string]any {
	return map[string]any{
		"name":         "spaces/AAA/messages/MMM",
		"text":         text,
		"argumentText": argumentText,
		"sender":       map[string]any{"name": "users/123", "displayName": "Alice", "type": "HUMAN"},
		"space":        map[string]any{"name": "spaces/AAA"},
		"thread":       map[string]any{"name": "spaces/AAA/threads/TTT"},
	}
}

func TestParseEvent_MentionMode(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	line := messageEvent(t, humanMessage("@Claude summarize this", "summarize this"))

	msg, ok := p.parseEvent(line)
	if !ok {
		t.Fatal("expected message to be handled")
	}
	if msg.Content != "summarize this" {
		t.Errorf("Content = %q, want stripped argumentText", msg.Content)
	}
	if msg.UserID != "users/123" || msg.UserName != "Alice" {
		t.Errorf("sender = %q/%q", msg.UserID, msg.UserName)
	}
	if msg.SessionKey != "googlechat:spaces/AAA" {
		t.Errorf("SessionKey = %q", msg.SessionKey)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok || rc.space != "spaces/AAA" || rc.thread != "spaces/AAA/threads/TTT" {
		t.Errorf("ReplyCtx = %+v", msg.ReplyCtx)
	}
}

func TestParseEvent_MentionModeFallsBackToText(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	msg, ok := p.parseEvent(messageEvent(t, humanMessage("hello there", "")))
	if !ok {
		t.Fatal("expected message to be handled")
	}
	if msg.Content != "hello there" {
		t.Errorf("Content = %q, want full text fallback", msg.Content)
	}
}

func TestParseEvent_TriggerMode(t *testing.T) {
	p := newTestPlatform("*", "claude:", "space")

	msg, ok := p.parseEvent(messageEvent(t, humanMessage("claude: do the thing", "")))
	if !ok {
		t.Fatal("expected triggered message to be handled")
	}
	if msg.Content != "do the thing" {
		t.Errorf("Content = %q, want trigger stripped", msg.Content)
	}

	if _, ok := p.parseEvent(messageEvent(t, humanMessage("no trigger here", ""))); ok {
		t.Error("expected message without trigger to be ignored")
	}
}

func TestParseEvent_SkipsNonHuman(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	data := humanMessage("hi", "hi")
	data["sender"] = map[string]any{"name": "users/bot", "type": "BOT"}

	if _, ok := p.parseEvent(messageEvent(t, data)); ok {
		t.Error("expected non-human sender to be ignored")
	}
}

func TestParseEvent_IgnoresNonMessageType(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	if _, ok := p.parseEvent(wrapEvent(t, "ADDED_TO_SPACE", humanMessage("hi", "hi"))); ok {
		t.Error("expected non-MESSAGE event to be ignored")
	}
}

func TestParseEvent_AllowFromEnforced(t *testing.T) {
	p := newTestPlatform("users/999", "", "space")
	if _, ok := p.parseEvent(messageEvent(t, humanMessage("hi", "hi"))); ok {
		t.Error("expected unauthorized sender to be ignored")
	}

	p2 := newTestPlatform("users/123", "", "space")
	if _, ok := p2.parseEvent(messageEvent(t, humanMessage("hi", "hi"))); !ok {
		t.Error("expected authorized sender to be handled")
	}
}

func TestParseEvent_DropsOldMessage(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	data := humanMessage("hi", "hi")
	data["createTime"] = "2000-01-01T00:00:00Z"
	if _, ok := p.parseEvent(messageEvent(t, data)); ok {
		t.Error("expected message predating startup to be ignored")
	}
}

func TestBuildSessionKey(t *testing.T) {
	cases := []struct {
		scope, space, user, thread, want string
	}{
		{"space", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A"},
		{"thread", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A:t:spaces/A/threads/T"},
		{"thread", "spaces/A", "users/1", "", "googlechat:spaces/A"},
		{"user", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A:users/1"},
	}
	for _, c := range cases {
		p := newTestPlatform("*", "", c.scope)
		if got := p.buildSessionKey(c.space, c.user, c.thread); got != c.want {
			t.Errorf("scope=%s buildSessionKey = %q, want %q", c.scope, got, c.want)
		}
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	cases := []struct {
		key           string
		space, thread string
	}{
		{"googlechat:spaces/A", "spaces/A", ""},
		{"googlechat:spaces/A:t:spaces/A/threads/T", "spaces/A", "spaces/A/threads/T"},
		{"googlechat:spaces/A:users/1", "spaces/A", ""},
	}
	for _, c := range cases {
		got, err := p.ReconstructReplyCtx(c.key)
		if err != nil {
			t.Errorf("key=%s: %v", c.key, err)
			continue
		}
		rc := got.(replyContext)
		if rc.space != c.space || rc.thread != c.thread {
			t.Errorf("key=%s reconstructed = %+v, want space=%q thread=%q", c.key, rc, c.space, c.thread)
		}
	}

	if _, err := p.ReconstructReplyCtx("slack:foo"); err == nil {
		t.Error("expected error for non-googlechat key")
	}
}

func TestBuildSendRequest(t *testing.T) {
	// Threaded reply: URL carries the reply option, body carries the thread.
	u, body, err := buildSendRequest(replyContext{space: "spaces/A", thread: "spaces/A/threads/T"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, chatAPIBase+"spaces/A/messages") {
		t.Errorf("url = %q, want prefix %q", u, chatAPIBase+"spaces/A/messages")
	}
	parsed, _ := url.Parse(u)
	if parsed.Query().Get("messageReplyOption") != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
		t.Errorf("url = %q, want messageReplyOption query", u)
	}
	var b map[string]any
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if b["text"] != "hi" {
		t.Errorf("body text = %v", b["text"])
	}
	thread, _ := b["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("body thread = %+v", b["thread"])
	}

	// No thread: no reply option, no thread in body.
	u2, body2, err := buildSendRequest(replyContext{space: "spaces/A"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u2, "messageReplyOption") {
		t.Errorf("unexpected messageReplyOption for top-level reply: %q", u2)
	}
	var b2 map[string]any
	if err := json.Unmarshal(body2, &b2); err != nil {
		t.Fatal(err)
	}
	if _, ok := b2["thread"]; ok {
		t.Errorf("unexpected thread for top-level reply: %+v", b2)
	}
}
