package cloudweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestWebSocketIntegration(t *testing.T) {
	var mu sync.Mutex
	var outbound []map[string]any
	done := make(chan struct{}, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, regRaw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var reg wireRegister
		if json.Unmarshal(regRaw, &reg) != nil || reg.Type != "register" {
			return
		}
		_ = conn.WriteJSON(wireRegisterAck{
			Type:         "register_ack",
			OK:           true,
			Capabilities: []string{"text", "reconstruct_reply"},
		})
		msg, _ := json.Marshal(wireInboundMessage{
			Type:       "message",
			MsgID:      "m1",
			SessionKey: "cloud_web:chat1:user1",
			UserID:     "user1",
			Content:    "hello",
			ReplyCtx:   "ctx-1",
		})
		_ = conn.WriteMessage(websocket.TextMessage, msg)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				mu.Lock()
				outbound = append(outbound, m)
				mu.Unlock()
				if m["type"] == "reply" {
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	p := mustNew(t, map[string]any{
		"token":     "secret",
		"transport": "websocket",
		"ws_url":    wsURL,
	})

	var received sync.WaitGroup
	received.Add(1)
	err := p.Start(func(_ core.Platform, msg *core.Message) {
		if msg.Content != "hello" {
			t.Errorf("content = %q", msg.Content)
		}
		received.Done()
		_ = p.Reply(context.Background(), msg.ReplyCtx, "world")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	received.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reply")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(outbound) == 0 || outbound[len(outbound)-1]["type"] != "reply" {
		t.Fatalf("outbound = %#v", outbound)
	}
}

func TestAllowFromFilter(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "websocket", "ws_url": "ws://127.0.0.1:1/ws",
		"allow_from": "user2",
	})
	p.allowFrom = "user2"

	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	raw, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "a1", UserID: "user1", Content: "x", ReplyCtx: "r",
	})
	p.handleMessage(raw)
	if called {
		t.Fatal("expected blocked user")
	}
}
