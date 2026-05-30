package daxiang

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseCallbackEvent_ParsesRobotSingleChatMessage(t *testing.T) {
	raw := []byte(`{
		"appId":"cli_xxx",
		"botId":123456,
		"eventTypeEnum":"ROBOT_SINGLE_CHAT_MESSAGE",
		"data":{
			"cts":1760000000000,
			"fromName":"alice",
			"fromUid":10001,
			"msgId":1212661773582049280,
			"message":"{\"text\":\"hello\"}",
			"chatId":20002,
			"conversationId":"dx-single-20002",
			"type":1
		}
	}`)

	evt, err := parseCallbackEvent(raw)
	if err != nil {
		t.Fatalf("parseCallbackEvent() error = %v", err)
	}
	if evt.EventTypeEnum != robotSingleChatMessage {
		t.Fatalf("EventTypeEnum = %q, want %q", evt.EventTypeEnum, robotSingleChatMessage)
	}
	if evt.Data.MsgID != 1212661773582049280 {
		t.Fatalf("MsgID = %d, want 1212661773582049280", evt.Data.MsgID)
	}
}

func TestNormalizeInboundMessage_BuildsCoreMessage(t *testing.T) {
	evt := callbackEvent{
		AppID:         "cli_xxx",
		BotID:         123456,
		EventTypeEnum: robotSingleChatMessage,
		Data: callbackMessageData{
			CTS:            time.Now().UnixMilli(),
			FromName:       "alice",
			FromUID:        10001,
			MsgID:          1212661773582049280,
			Message:        `{"text":"hello"}`,
			ChatID:         20002,
			ConversationID: "dx-single-20002",
			Type:           1,
		},
	}

	msg, err := normalizeInboundMessage(evt)
	if err != nil {
		t.Fatalf("normalizeInboundMessage() error = %v", err)
	}
	if msg.Platform != "daxiang" {
		t.Fatalf("Platform = %q, want daxiang", msg.Platform)
	}
	if msg.Content != "hello" {
		t.Fatalf("Content = %q, want hello", msg.Content)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", msg.ReplyCtx)
	}
	if rc.senderID != 10001 {
		t.Fatalf("senderID = %d, want 10001", rc.senderID)
	}
}

func TestNormalizeInboundMessage_RejectsNonText(t *testing.T) {
	evt := callbackEvent{
		EventTypeEnum: robotSingleChatMessage,
		Data:          callbackMessageData{Type: 4, Message: `{"url":"x"}`},
	}

	_, err := normalizeInboundMessage(evt)
	if err == nil {
		t.Fatal("normalizeInboundMessage() error = nil, want unsupported type error")
	}
}

// TestCallbackService_EventCallback_ParsesAndDispatches verifies that
// callbackService.EventCallback correctly parses the JSON payload and
// routes it through handleCallbackEvent so the handler is invoked.
func TestCallbackService_EventCallback_ParsesAndDispatches(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456}
	called := false
	p.handler = func(_ core.Platform, msg *core.Message) {
		called = true
		if msg.Content != "hi from callback service" {
			t.Errorf("Content = %q, want %q", msg.Content, "hi from callback service")
		}
	}

	svc := &callbackService{platform: p}
	cts := time.Now().UnixMilli()
	jsonEvent := fmt.Sprintf(`{
		"appId":"cli_xxx",
		"botId":123456,
		"eventTypeEnum":"ROBOT_SINGLE_CHAT_MESSAGE",
		"data":{
			"cts":%d,
			"fromName":"bob",
			"fromUid":20001,
			"msgId":9999000000000001,
			"message":"{\"text\":\"hi from callback service\"}",
			"chatId":30001,
			"conversationId":"dx-single-30001",
			"type":1
		}
	}`, cts)

	resp, err := svc.EventCallback(context.Background(), 1, jsonEvent)
	if err != nil {
		t.Fatalf("EventCallback() error = %v", err)
	}
	if resp == nil || resp.Status == nil {
		t.Fatal("EventCallback() resp or resp.Status = nil")
	}
	if resp.Status.Code != 0 {
		t.Fatalf("resp.Status.Code = %d, want 0", resp.Status.Code)
	}
	if !called {
		t.Fatal("handler was not called by EventCallback")
	}
}

// TestCallbackService_EventCallback_ReturnsErrorOnBadJSON verifies that
// malformed JSON causes EventCallback to return an error.
func TestCallbackService_EventCallback_ReturnsErrorOnBadJSON(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456}
	svc := &callbackService{platform: p}

	_, err := svc.EventCallback(context.Background(), 1, "not-json")
	if err == nil {
		t.Fatal("EventCallback() error = nil, want parse error")
	}
}

func TestCallbackService_EventCallback_IgnoresNonTextMessage(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456}
	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }
	svc := &callbackService{platform: p}

	jsonEvent := fmt.Sprintf(`{
		"appId":"cli_xxx",
		"botId":123456,
		"eventTypeEnum":"ROBOT_SINGLE_CHAT_MESSAGE",
		"data":{
			"cts":%d,
			"fromName":"bob",
			"fromUid":20001,
			"msgId":9999000000000002,
			"message":"{\"url\":\"x\"}",
			"chatId":30001,
			"conversationId":"dx-single-30001",
			"type":4
		}
	}`, time.Now().UnixMilli())

	resp, err := svc.EventCallback(context.Background(), 1, jsonEvent)
	if err != nil {
		t.Fatalf("EventCallback() error = %v, want nil", err)
	}
	if resp == nil || resp.Status == nil || resp.Status.Code != 0 {
		t.Fatalf("EventCallback() resp = %#v, want ok status", resp)
	}
	if called {
		t.Fatal("handler called for non-text callback message")
	}
}
