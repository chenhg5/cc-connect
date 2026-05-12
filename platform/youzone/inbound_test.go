package youzone

import "testing"

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

	msg, ok := parseInboundMessage(raw)
	if !ok {
		t.Fatal("parseInboundMessage() ok = false")
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
}

func TestParseInboundMessageVersionFallsBackAcrossKeys(t *testing.T) {
	msg, ok := parseInboundMessage([]byte(`{"id":"1","type":"dmessage","sender":"u1","content":"hi","messageVersion":3}`))
	if !ok {
		t.Fatal("parseInboundMessage() ok = false")
	}
	if msg.MessageVersion == nil || *msg.MessageVersion != 3 {
		t.Fatalf("MessageVersion = %v, want 3", msg.MessageVersion)
	}

	msg, ok = parseInboundMessage([]byte(`{"id":"1","type":"dmessage","sender":"u1","content":"hi"}`))
	if !ok {
		t.Fatal("parseInboundMessage() ok = false")
	}
	if msg.MessageVersion != nil {
		t.Fatalf("MessageVersion = %v, want nil", msg.MessageVersion)
	}
}

func TestParseInboundMessageIgnoresAuthFrame(t *testing.T) {
	_, ok := parseInboundMessage([]byte(`{"id":"1","type":"auth","msg":"success","version":0}`))
	if ok {
		t.Fatal("parseInboundMessage(auth) ok = true, want false")
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
