package daxiangbridge

import (
	"encoding/json"
	"testing"
)

func TestBridgeFrame_RoundTrip(t *testing.T) {
	frame := BridgeFrame{
		Type:      FrameTypeBridgeEventMessage,
		RequestID: "req_001",
		SessionID: "sess_001",
		Ts:        1770000000000,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var got BridgeFrame
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != FrameTypeBridgeEventMessage {
		t.Errorf("type: got %q want %q", got.Type, FrameTypeBridgeEventMessage)
	}
	if got.RequestID != "req_001" {
		t.Errorf("requestId: got %q want %q", got.RequestID, "req_001")
	}
}

func TestBridgeEventPayload_Unmarshal(t *testing.T) {
	raw := `{"platform":"daxiang","chatType":"private","conversationId":"conv_1","messageId":"msg_1","fromUserId":"u_1","fromUserName":"张三","text":"帮我看下报错"}`
	var p BridgeEventPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Text != "帮我看下报错" {
		t.Errorf("text: got %q", p.Text)
	}
}
