package googlechat

import (
	"encoding/json"
	"testing"
)

// newTestPlatform builds a Platform directly so tests can exercise parsing and
// routing without a gws subprocess.
func newTestPlatform(allowFrom, trigger, scope string) *Platform {
	return &Platform{allowFrom: allowFrom, trigger: trigger, sessionScope: scope}
}

// eventLine renders a chat.message.v1.created event as the single NDJSON line
// the gws subprocess would emit.
func eventLine(t *testing.T, data map[string]any) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": createdEventType,
		"data": data,
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return line
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
	line := eventLine(t, humanMessage("@Claude summarize this", "summarize this"))

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
	line := eventLine(t, humanMessage("hello there", ""))

	msg, ok := p.parseEvent(line)
	if !ok {
		t.Fatal("expected message to be handled")
	}
	if msg.Content != "hello there" {
		t.Errorf("Content = %q, want full text fallback", msg.Content)
	}
}

func TestParseEvent_TriggerMode(t *testing.T) {
	p := newTestPlatform("*", "claude:", "space")

	msg, ok := p.parseEvent(eventLine(t, humanMessage("claude: do the thing", "")))
	if !ok {
		t.Fatal("expected triggered message to be handled")
	}
	if msg.Content != "do the thing" {
		t.Errorf("Content = %q, want trigger stripped", msg.Content)
	}

	if _, ok := p.parseEvent(eventLine(t, humanMessage("no trigger here", ""))); ok {
		t.Error("expected message without trigger to be ignored")
	}
}

func TestParseEvent_SkipsNonHuman(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	data := humanMessage("hi", "hi")
	data["sender"] = map[string]any{"name": "users/bot", "type": "BOT"}

	if _, ok := p.parseEvent(eventLine(t, data)); ok {
		t.Error("expected non-human sender to be ignored")
	}
}

func TestParseEvent_IgnoresWrongType(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	line, _ := json.Marshal(map[string]any{
		"type": "google.workspace.chat.message.v1.updated",
		"data": humanMessage("hi", "hi"),
	})
	if _, ok := p.parseEvent(line); ok {
		t.Error("expected non-created event to be ignored")
	}
}

func TestParseEvent_AllowFromEnforced(t *testing.T) {
	p := newTestPlatform("users/999", "", "space")
	if _, ok := p.parseEvent(eventLine(t, humanMessage("hi", "hi"))); ok {
		t.Error("expected unauthorized sender to be ignored")
	}

	p2 := newTestPlatform("users/123", "", "space")
	if _, ok := p2.parseEvent(eventLine(t, humanMessage("hi", "hi"))); !ok {
		t.Error("expected authorized sender to be handled")
	}
}

func TestParseEvent_DropsOldMessage(t *testing.T) {
	p := newTestPlatform("*", "", "space")
	data := humanMessage("hi", "hi")
	data["createTime"] = "2000-01-01T00:00:00Z"
	if _, ok := p.parseEvent(eventLine(t, data)); ok {
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

func TestBuildCreateArgs(t *testing.T) {
	// Threaded reply: params carry the reply option, body carries the thread.
	args, err := buildCreateArgs(replyContext{space: "spaces/A", thread: "spaces/A/threads/T"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	params, body := decodeArgs(t, args)
	if params["parent"] != "spaces/A" || params["messageReplyOption"] != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
		t.Errorf("params = %+v", params)
	}
	if body["text"] != "hi" {
		t.Errorf("body text = %v", body["text"])
	}
	thread, _ := body["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("body thread = %+v", body["thread"])
	}

	// No thread: no reply option, no thread in body.
	args, err = buildCreateArgs(replyContext{space: "spaces/A"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	params, body = decodeArgs(t, args)
	if _, ok := params["messageReplyOption"]; ok {
		t.Errorf("unexpected messageReplyOption for top-level reply: %+v", params)
	}
	if _, ok := body["thread"]; ok {
		t.Errorf("unexpected thread for top-level reply: %+v", body)
	}
}

// decodeArgs extracts and parses the --params and --json JSON payloads from a
// buildCreateArgs result.
func decodeArgs(t *testing.T, args []string) (params, body map[string]any) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--params":
			if err := json.Unmarshal([]byte(args[i+1]), &params); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
		case "--json":
			if err := json.Unmarshal([]byte(args[i+1]), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
		}
	}
	if params == nil || body == nil {
		t.Fatalf("missing --params/--json in args: %v", args)
	}
	return params, body
}
