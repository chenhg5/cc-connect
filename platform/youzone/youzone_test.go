package youzone

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestHandleInboundPreservesReplyContext verifies that the fields cc-connect
// later uses to build the outbound reply-quote (robot, conversation, sender,
// message id + version, original text) survive the inbound -> core.Message hop.
func TestHandleInboundPreservesReplyContext(t *testing.T) {
	raw := []byte(`{
		"id":"MSG-1",
		"from":"claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn",
		"to":"claw_robot-1.esn.upesn@pubaccount.im.yyuap.com",
		"contentType":2,
		"content":"{\"content\":\"原始问题\"}",
		"type":"dmessage",
		"sender":"user-1.esn.upesn",
		"version":8
	}`)

	var got *core.Message
	p := &Platform{}
	p.cfg.robotID = "robot-1"
	p.handler = func(_ core.Platform, m *core.Message) { got = m }

	p.handleInbound(raw)
	if got == nil {
		t.Fatal("handler was not invoked")
	}
	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", got.ReplyCtx)
	}
	if rc.robotID != "robot-1" {
		t.Errorf("robotID = %q, want robot-1", rc.robotID)
	}
	if rc.conversationID != "claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn" {
		t.Errorf("conversationID = %q", rc.conversationID)
	}
	if rc.senderID != "user-1.esn.upesn" {
		t.Errorf("senderID = %q", rc.senderID)
	}
	if rc.messageID != "MSG-1" {
		t.Errorf("messageID = %q", rc.messageID)
	}
	if rc.messageVersion == nil || *rc.messageVersion != 8 {
		t.Errorf("messageVersion = %v, want 8", rc.messageVersion)
	}
	if rc.replyText != "原始问题" {
		t.Errorf("replyText = %q, want 原始问题", rc.replyText)
	}

	// And that context round-trips into a reply-quote card.
	out, err := buildOutboundMessage("answer", rc)
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)
	if card.TitleZone.Reply == nil || card.TitleZone.Reply.ReplyMessageID != "MSG-1" {
		t.Fatalf("reply quote not built from ReplyCtx: %#v", card.TitleZone)
	}
	if card.TitleZone.Reply.ReplyUser != "user-1" || card.TitleZone.Reply.ReplyText != "原始问题" {
		t.Fatalf("reply quote header = %#v", card.TitleZone.Reply)
	}
}
