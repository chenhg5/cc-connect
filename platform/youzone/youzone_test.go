package youzone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if rc.messageVersionRaw != "8" {
		t.Errorf("messageVersionRaw = %q, want %q", rc.messageVersionRaw, "8")
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
		t.Fatalf("reply quote header = %#v", card.TitleZone)
	}
}

// TestHandleInboundLogsAcceptedAndFrameReceived locks in the inbound
// observability contract: every accepted user message produces both an
// inbound_frame_received summary (so we can see the frame arrive) and an
// inbound_message_accepted record (so we know it was dispatched to the
// engine).
func TestHandleInboundLogsAcceptedAndFrameReceived(t *testing.T) {
	raw := []byte(`{
		"id":"MSG-2","type":"dmessage","sender":"user-2",
		"from":"conv-2","contentType":2,"version":11,
		"content":"/connect pre"
	}`)
	p := &Platform{}
	p.cfg.robotID = "robot-1"
	p.handler = func(_ core.Platform, _ *core.Message) {}

	cap := captureLogs(t, func() { p.handleInbound(raw) })

	frame, ok := cap.findByMessage("youzone: inbound frame received")
	if !ok {
		t.Fatal("missing inbound_frame_received log")
	}
	f := attrs(frame)
	if f["message_id"] != "MSG-2" || f["sender_id"] != "user-2" {
		t.Errorf("frame log fields = %#v", f)
	}
	if f["command"] != "/connect" {
		t.Errorf("command = %v, want /connect", f["command"])
	}
	if f["message_version"] != "11" {
		t.Errorf("message_version = %v, want %q", f["message_version"], "11")
	}

	accepted, ok := cap.findByMessage("youzone: inbound message accepted")
	if !ok {
		t.Fatal("missing inbound_message_accepted log")
	}
	a := attrs(accepted)
	if a["session"] != "youzone:conv-2:user-2" {
		t.Errorf("session = %v", a["session"])
	}
	if a["message_version"] != "11" {
		t.Errorf("message_version = %v", a["message_version"])
	}
}

func TestHandleInboundDropsDuplicateWithReason(t *testing.T) {
	raw := []byte(`{"id":"dup-1","type":"dmessage","sender":"u","content":"hi"}`)
	p := &Platform{}
	p.handler = func(_ core.Platform, _ *core.Message) {}

	// First call primes the dedup; second call must drop with reason.
	p.handleInbound(raw)
	cap := captureLogs(t, func() { p.handleInbound(raw) })

	r, ok := cap.findByMessage("youzone: inbound message dropped")
	if !ok {
		t.Fatal("missing dropped log on duplicate")
	}
	a := attrs(r)
	if a["reason"] != string(inboundDropDuplicate) {
		t.Errorf("reason = %v, want %q", a["reason"], inboundDropDuplicate)
	}
	if a["message_id"] != "dup-1" {
		t.Errorf("message_id = %v", a["message_id"])
	}
}

func TestHandleInboundDropsUnauthorizedWithReason(t *testing.T) {
	raw := []byte(`{"id":"m","type":"dmessage","sender":"intruder","content":"hi"}`)
	p := &Platform{}
	p.cfg.allowFrom = "alice,bob"
	p.handler = func(_ core.Platform, _ *core.Message) {}

	cap := captureLogs(t, func() { p.handleInbound(raw) })

	r, ok := cap.findByMessage("youzone: inbound message dropped")
	if !ok {
		t.Fatal("missing dropped log on unauthorized user")
	}
	a := attrs(r)
	if a["reason"] != string(inboundDropUnauthorizedUser) {
		t.Errorf("reason = %v, want %q", a["reason"], inboundDropUnauthorizedUser)
	}
	if a["sender_id"] != "intruder" {
		t.Errorf("sender_id = %v", a["sender_id"])
	}
}

func TestHandleInboundDropsMissingHandlerWithReason(t *testing.T) {
	raw := []byte(`{"id":"m","type":"dmessage","sender":"u","content":"hi"}`)
	p := &Platform{}
	// p.handler is intentionally nil here — simulates a frame arriving before
	// Start() wired the handler.

	cap := captureLogs(t, func() { p.handleInbound(raw) })

	r, ok := cap.findByMessage("youzone: inbound message dropped")
	if !ok {
		t.Fatal("missing dropped log when handler is nil")
	}
	a := attrs(r)
	if a["reason"] != string(inboundDropMissingHandler) {
		t.Errorf("reason = %v, want %q", a["reason"], inboundDropMissingHandler)
	}
}

// TestHandleInboundJSONInvalidSkipsFrameReceived covers the special case: if
// the parser can't unmarshal the frame at all, we have no readable fields to
// summarize, so inbound_frame_received is suppressed and inbound_message_
// dropped (reason=json_invalid) carries the only meaningful info (raw_len).
func TestHandleInboundJSONInvalidSkipsFrameReceived(t *testing.T) {
	p := &Platform{}
	cap := captureLogs(t, func() { p.handleInbound([]byte(`{not-json`)) })

	if _, ok := cap.findByMessage("youzone: inbound frame received"); ok {
		t.Error("inbound_frame_received must NOT fire on json_invalid")
	}
	r, ok := cap.findByMessage("youzone: inbound message dropped")
	if !ok {
		t.Fatal("missing dropped log on json_invalid")
	}
	a := attrs(r)
	if a["reason"] != string(inboundDropJSONInvalid) {
		t.Errorf("reason = %v, want %q", a["reason"], inboundDropJSONInvalid)
	}
	if a["raw_len"] == nil {
		t.Error("raw_len missing")
	}
}

// TestPlatformSendLogsSucceededWithCorrelationFields locks in the outbound
// observability contract: a successful send carries the inbound-side fields
// (session, conversation_id, sender_id, reply_to_message_id, message_version)
// plus the HTTP-side fields. Without these correlations an operator can't
// tell "send succeeded but client never showed it" from "send API failed".
func TestPlatformSendLogsSucceededWithCorrelationFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"packetId": "packet-abc"},
		})
	}))
	defer server.Close()

	p := &Platform{
		cfg:    testClientConfig(server.URL),
		client: newClient(testClientConfig(server.URL), server.Client()),
	}
	rc := replyContext{
		robotID:           "robot-1",
		conversationID:    "conv-7",
		senderID:          "user-7",
		messageID:         "MSG-7",
		messageVersionRaw: "42",
		replyText:         "/connect pre",
	}

	cap := captureLogs(t, func() {
		if err := p.Send(context.Background(), rc, "回复内容"); err != nil {
			t.Fatalf("Send: %v", err)
		}
	})

	r, ok := cap.findByMessage("youzone: send message succeeded")
	if !ok {
		t.Fatal("missing send_message_succeeded log")
	}
	a := attrs(r)
	if a["session"] != "youzone:conv-7:user-7" {
		t.Errorf("session = %v", a["session"])
	}
	if a["reply_to_message_id"] != "MSG-7" {
		t.Errorf("reply_to_message_id = %v", a["reply_to_message_id"])
	}
	if a["message_version"] != "42" {
		t.Errorf("message_version = %v, want %q (raw string, not *int)", a["message_version"], "42")
	}
	if a["packet_id"] != "packet-abc" {
		t.Errorf("packet_id = %v", a["packet_id"])
	}
	if a["business_code"] != int64(200) && a["business_code"] != 200 {
		t.Errorf("business_code = %v", a["business_code"])
	}
	if a["http_status"] != int64(200) && a["http_status"] != 200 {
		t.Errorf("http_status = %v", a["http_status"])
	}
	// content_len must be rune count, not byte length (4 runes for "回复内容").
	if a["content_len"] != int64(4) && a["content_len"] != 4 {
		t.Errorf("content_len = %v, want 4 (rune count)", a["content_len"])
	}
}

func TestPlatformSendLogsFailedWithCorrelationFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 9001, "message": "boom"})
	}))
	defer server.Close()

	p := &Platform{
		cfg:    testClientConfig(server.URL),
		client: newClient(testClientConfig(server.URL), server.Client()),
	}
	rc := replyContext{
		robotID:           "robot-1",
		conversationID:    "conv-7",
		senderID:          "user-7",
		messageID:         "MSG-7",
		messageVersionRaw: "42",
	}

	cap := captureLogs(t, func() {
		// Send must surface the error so core.Engine's generic "platform send
		// failed" still fires too — the youzone log is additive, not a
		// replacement.
		if err := p.Send(context.Background(), rc, "x"); err == nil {
			t.Fatal("Send should have failed")
		}
	})

	r, ok := cap.findByMessage("youzone: send message failed")
	if !ok {
		t.Fatal("missing send_message_failed log")
	}
	a := attrs(r)
	if a["reply_to_message_id"] != "MSG-7" {
		t.Errorf("reply_to_message_id = %v", a["reply_to_message_id"])
	}
	if a["err"] == nil {
		t.Error("err missing on failed send")
	}
	if a["http_status"] != int64(500) && a["http_status"] != 500 {
		t.Errorf("http_status = %v, want 500", a["http_status"])
	}
}
