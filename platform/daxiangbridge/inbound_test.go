package daxiangbridge

import (
	"encoding/json"
	"testing"
)

func TestNormalizeInboundMessage(t *testing.T) {
	payload := BridgeEventPayload{
		Platform: "daxiang", ChatType: "private",
		ConversationID: "conv_001", MessageID: "msg_001",
		FromUserID: "u_001", FromUserName: "张三", Text: "帮我看下报错",
	}
	raw, _ := json.Marshal(payload)
	frame := BridgeFrame{
		Type: FrameTypeBridgeEventMessage, RequestID: "req_001",
		SessionID: "sess_001", Payload: raw,
	}

	msg, err := normalizeInboundMessage(frame)
	if err != nil {
		t.Fatal(err)
	}
	if msg.SessionKey != "sess_001" {
		t.Errorf("SessionKey: got %q", msg.SessionKey)
	}
	if msg.Platform != "daxiangbridge" {
		t.Errorf("Platform: got %q", msg.Platform)
	}
	if msg.Content != "帮我看下报错" {
		t.Errorf("Content: got %q", msg.Content)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok {
		t.Fatal("ReplyCtx is not replyContext")
	}
	if rc.requestID != "req_001" {
		t.Errorf("replyContext.requestID: got %q", rc.requestID)
	}
}

func TestNormalizeInboundMessage_NonPrivate_Error(t *testing.T) {
	payload := BridgeEventPayload{ChatType: "group", Text: "hi"}
	raw, _ := json.Marshal(payload)
	frame := BridgeFrame{Type: FrameTypeBridgeEventMessage, Payload: raw}
	_, err := normalizeInboundMessage(frame)
	if err == nil {
		t.Error("expected error for non-private chat")
	}
}
