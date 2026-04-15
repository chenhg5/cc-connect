package daxiangbridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func TestPlatform_ReceivesMessage(t *testing.T) {
	received := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()

		_, raw, _ := conn.ReadMessage()
		var frame BridgeFrame
		json.Unmarshal(raw, &frame)
		if frame.Type != FrameTypeClientRegister {
			t.Errorf("expected register, got %q", frame.Type)
		}

		ack := BridgeFrame{Type: FrameTypeClientRegistered}
		b, _ := json.Marshal(ack)
		conn.WriteMessage(websocket.TextMessage, b)

		payload := BridgeEventPayload{
			Platform: "daxiang", ChatType: "private",
			ConversationID: "conv_1", MessageID: "msg_1",
			FromUserID: "u_1", FromUserName: "张三", Text: "hello",
		}
		raw2, _ := json.Marshal(payload)
		event := BridgeFrame{
			Type: FrameTypeBridgeEventMessage, RequestID: "req_1",
			SessionID: "sess_1", Payload: raw2,
		}
		b2, _ := json.Marshal(event)
		conn.WriteMessage(websocket.TextMessage, b2)
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	p, err := New(map[string]any{
		"ws_url":        wsURL,
		"client_id":     "test-client",
		"client_secret": "0123456789abcdef0123456789abcdef",
		"bot_id":        int64(123),
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Start(func(_ core.Platform, msg *core.Message) {
		received <- msg
	})
	defer p.Stop()

	select {
	case msg := <-received:
		if msg.Content != "hello" {
			t.Errorf("content: got %q", msg.Content)
		}
		if msg.UserName != "张三" {
			t.Errorf("userName: got %q", msg.UserName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}
