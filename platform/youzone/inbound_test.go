package youzone

import (
	"testing"
	"unicode/utf8"
)

func TestParseInboundDMessageExtractsNestedContent(t *testing.T) {
	raw := []byte(`{
		"id":"757C23F7-7EA5-4644-AA6A-C7BB2514B376",
		"from":"claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn",
		"to":"claw_robot-1.esn.upesn@pubaccount.im.yyuap.com",
		"contentType":2,
		"content":"{\"content\":\"hi\",\"supply\":{\"version\":2}}",
		"type":"dmessage",
		"sender":"user-1.esn.upesn",
		"version":8
	}`)

	msg, reason := parseInboundMessage(raw)
	if reason != inboundDropNone {
		t.Fatalf("parseInboundMessage() reason = %q, want inboundDropNone", reason)
	}
	if msg.MessageID != "757C23F7-7EA5-4644-AA6A-C7BB2514B376" {
		t.Fatalf("MessageID = %q", msg.MessageID)
	}
	if msg.SenderID != "user-1.esn.upesn" {
		t.Fatalf("SenderID = %q", msg.SenderID)
	}
	if msg.ConversationID != "claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn" {
		t.Fatalf("ConversationID = %q", msg.ConversationID)
	}
	if msg.Text != "hi" {
		t.Fatalf("Text = %q", msg.Text)
	}
	if msg.ContentType != 2 {
		t.Fatalf("ContentType = %d", msg.ContentType)
	}
	if msg.MessageVersion == nil || *msg.MessageVersion != 8 {
		t.Fatalf("MessageVersion = %v, want 8", msg.MessageVersion)
	}
	if msg.MessageVersionRaw != "8" {
		t.Fatalf("MessageVersionRaw = %q, want %q", msg.MessageVersionRaw, "8")
	}
}

func TestParseInboundMessageVersionFallsBackAcrossKeys(t *testing.T) {
	msg, reason := parseInboundMessage([]byte(`{"id":"1","type":"dmessage","sender":"u1","content":"hi","messageVersion":3}`))
	if reason != inboundDropNone {
		t.Fatalf("parseInboundMessage() reason = %q, want inboundDropNone", reason)
	}
	if msg.MessageVersion == nil || *msg.MessageVersion != 3 {
		t.Fatalf("MessageVersion = %v, want 3", msg.MessageVersion)
	}
	if msg.MessageVersionRaw != "3" {
		t.Fatalf("MessageVersionRaw = %q, want %q", msg.MessageVersionRaw, "3")
	}

	msg, reason = parseInboundMessage([]byte(`{"id":"1","type":"dmessage","sender":"u1","content":"hi"}`))
	if reason != inboundDropNone {
		t.Fatalf("parseInboundMessage() reason = %q, want inboundDropNone", reason)
	}
	if msg.MessageVersion != nil {
		t.Fatalf("MessageVersion = %v, want nil", msg.MessageVersion)
	}
	if msg.MessageVersionRaw != "" {
		t.Fatalf("MessageVersionRaw = %q, want empty", msg.MessageVersionRaw)
	}
}

func TestParseInboundMessageReportsHeartbeat(t *testing.T) {
	msg, reason := parseInboundMessage([]byte(`{"id":"1","type":"auth","msg":"success","version":0}`))
	if reason != inboundDropHeartbeat {
		t.Fatalf("reason = %q, want inboundDropHeartbeat", reason)
	}
	// Even on drop the parser still populates readable fields so handleInbound
	// can emit the inbound_frame_received summary without a second decode.
	if msg.MessageID != "1" || msg.Type != "auth" {
		t.Fatalf("msg = %#v, want fields populated on drop", msg)
	}
}

func TestParseInboundMessageReportsEmptyFrame(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t  \n"} {
		_, reason := parseInboundMessage([]byte(raw))
		if reason != inboundDropEmptyFrame {
			t.Fatalf("reason(%q) = %q, want inboundDropEmptyFrame", raw, reason)
		}
	}
}

func TestParseInboundMessageReportsJSONInvalid(t *testing.T) {
	_, reason := parseInboundMessage([]byte(`{not-json`))
	if reason != inboundDropJSONInvalid {
		t.Fatalf("reason = %q, want inboundDropJSONInvalid", reason)
	}
}

// TestParseInboundMessageReportsEmptyTextStillFillsFields locks in the
// single-parse contract: a frame that has all the metadata but only whitespace
// text must still surface message_id/conversation_id/sender_id, because
// handleInbound logs those in inbound_frame_received before deciding to drop.
func TestParseInboundMessageReportsEmptyTextStillFillsFields(t *testing.T) {
	raw := []byte(`{"id":"m1","conversationId":"c1","sender":"u1","type":"dmessage","content":"  "}`)
	msg, reason := parseInboundMessage(raw)
	if reason != inboundDropEmptyText {
		t.Fatalf("reason = %q, want inboundDropEmptyText", reason)
	}
	if msg.MessageID != "m1" || msg.ConversationID != "c1" || msg.SenderID != "u1" {
		t.Fatalf("msg fields = %#v, want populated on drop", msg)
	}
}

func TestExtractCommand(t *testing.T) {
	cases := map[string]string{
		"/connect pre":          "/connect",
		"/connect":              "/connect",
		"  /connect yms-dev  ":  "/connect",
		"hello /not-a-command":  "",
		"":                      "",
		"  ":                    "",
		"/help\nignored\nrest":  "/help",
	}
	for in, want := range cases {
		if got := extractCommand(in); got != want {
			t.Errorf("extractCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTextLenUsesRuneCount documents the contract that text_len in logs is
// rune count, not byte count — Chinese/emoji content otherwise prints
// uninterpretable byte counts.
func TestTextLenUsesRuneCount(t *testing.T) {
	if got := utf8.RuneCountInString("你好世界"); got != 4 {
		t.Fatalf("utf8.RuneCountInString = %d, want 4 — log field must use this", got)
	}
}

func TestSessionKeyUsesSenderScopedConversation(t *testing.T) {
	msg := inboundMessage{
		SenderID:       "user-1.esn.upesn",
		ConversationID: "conversation",
	}
	if got := sessionKey(msg); got != "youzone:conversation:user-1.esn.upesn" {
		t.Fatalf("sessionKey() = %q", got)
	}
}
