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

func TestNew_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		opts    map[string]any
		wantErr bool
	}{
		{"missing token", map[string]any{"transport": "websocket", "base_url": "http://x"}, true},
		{"invalid transport", map[string]any{"token": "t", "transport": "mqtt"}, true},
		{"websocket missing url", map[string]any{"token": "t", "transport": "websocket"}, true},
		{"long_poll missing base", map[string]any{"token": "t", "transport": "long_poll"}, true},
		{"gateway missing urls", map[string]any{"token": "t", "transport": "gateway"}, true},
		{"websocket ok", map[string]any{"token": "t", "transport": "websocket", "ws_url": "ws://127.0.0.1:1/ws"}, false},
		{"long_poll ok", map[string]any{"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1"}, false},
		{"gateway ok", map[string]any{"token": "t", "transport": "gateway", "base_url": "http://127.0.0.1", "listen": ":0"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.opts)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseRegisterAck(t *testing.T) {
	raw := []byte(`{"type":"register_ack","ok":true,"capabilities":["text","image"]}`)
	caps, err := parseRegisterAck(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !caps["text"] || !caps["image"] {
		t.Fatalf("caps = %#v", caps)
	}
}

func TestBuildSessionKey(t *testing.T) {
	if got := buildSessionKey("cloud_web", "c1", "u1", false, ""); got != "cloud_web:c1:u1" {
		t.Fatalf("got %q", got)
	}
	if got := buildSessionKey("cloud_web", "c1", "u1", true, ""); got != "cloud_web:c1" {
		t.Fatalf("got %q", got)
	}
	if got := buildSessionKey("cloud_web", "c1", "u1", false, "custom:key"); got != "custom:key" {
		t.Fatalf("got %q", got)
	}
}

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
	pAny, err := New(map[string]any{
		"token":     "secret",
		"transport": "websocket",
		"ws_url":    wsURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pAny.(*Platform)

	var received sync.WaitGroup
	received.Add(1)
	err = p.Start(func(_ core.Platform, msg *core.Message) {
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

func TestLongPollIntegration(t *testing.T) {
	registered := false
	var outbound []map[string]any
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case defaultRegister:
			registered = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(wireRegisterAck{
				Type:         "register_ack",
				OK:           true,
				Capabilities: allCapabilities,
			})
		case defaultEvents:
			ev, _ := json.Marshal(wireInboundMessage{
				Type: "message", MsgID: "p1", SessionKey: "cloud_web:c:u",
				UserID: "u", Content: "ping", ReplyCtx: "ctx",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []json.RawMessage{ev}})
		case defaultSend:
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			mu.Lock()
			outbound = append(outbound, m)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	pAny, err := New(map[string]any{
		"token":     "secret",
		"transport": "long_poll",
		"base_url":  srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pAny.(*Platform)

	got := make(chan string, 1)
	err = p.Start(func(_ core.Platform, msg *core.Message) {
		got <- msg.Content
		_ = p.Reply(context.Background(), msg.ReplyCtx, "pong")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	select {
	case content := <-got:
		if content != "ping" {
			t.Fatalf("content = %q", content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !registered {
		t.Fatal("register not called")
	}
	found := false
	for _, m := range outbound {
		if m["type"] == "reply" && m["content"] == "pong" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outbound = %#v", outbound)
	}
}

func TestGatewayWebhook(t *testing.T) {
	pAny, err := New(map[string]any{
		"token":        "secret",
		"transport":    "gateway",
		"base_url":     "http://127.0.0.1:1",
		"listen":       ":0",
		"webhook_path": "/hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pAny.(*Platform)

	got := make(chan string, 1)
	if err := p.Start(func(_ core.Platform, msg *core.Message) {
		got <- msg.Content
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	gt := p.tp.(*gatewayTransport)
	if gt.listener == nil {
		t.Fatal("gateway listener not started")
	}
	url := "http://" + gt.listener.Addr().String() + gt.webhookPath
	body, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g1", SessionKey: "cloud_web:x:y",
		UserID: "y", Content: "from gateway", ReplyCtx: "ctx",
	})
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	select {
	case content := <-got:
		if content != "from gateway" {
			t.Fatalf("content = %q", content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for webhook message")
	}
}

func TestCapabilityDegradeCard(t *testing.T) {
	pAny, err := New(map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pAny.(*Platform)
	p.tp.(*pollTransport).setCaps(map[string]bool{"text": true})

	card := &core.Card{Elements: []core.CardElement{core.CardMarkdown{Content: "hello card"}}}
	err = p.SendCard(context.Background(), replyContext{SessionKey: "s", ReplyCtx: "r"}, card)
	if err == nil {
		t.Fatal("expected error when send path unreachable")
	}
}

func TestAllowFromFilter(t *testing.T) {
	pAny, _ := New(map[string]any{
		"token": "t", "transport": "websocket", "ws_url": "ws://127.0.0.1:1/ws",
		"allow_from": "user2",
	})
	p := pAny.(*Platform)
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

func TestReconstructReplyCtx(t *testing.T) {
	pAny, _ := New(map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	p := pAny.(*Platform)
	p.tp.(*pollTransport).setCaps(capabilitySet([]string{"text", "reconstruct_reply"}))
	p.storeReplyCtx("cloud_web:c:u", "stored-ctx")

	rc, err := p.ReconstructReplyCtx("cloud_web:c:u")
	if err != nil {
		t.Fatal(err)
	}
	got := rc.(replyContext)
	if got.ReplyCtx != "stored-ctx" {
		t.Fatalf("reply_ctx = %q", got.ReplyCtx)
	}
}

func TestGroupReplyRequiresMention(t *testing.T) {
	pAny, _ := New(map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
		"group_reply_all": false,
	})
	p := pAny.(*Platform)

	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	mentioned := false
	raw, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g1", UserID: "u1", Content: "hi",
		ChatType: "group", Mentioned: &mentioned, ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if called {
		t.Fatal("expected group message without mention to be ignored")
	}

	mentioned = true
	raw, _ = json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g2", UserID: "u1", Content: "hi",
		ChatType: "group", Mentioned: &mentioned, ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if !called {
		t.Fatal("expected mentioned group message to be accepted")
	}
}

func TestCardActionPermAllow(t *testing.T) {
	pAny, _ := New(map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	p := pAny.(*Platform)

	done := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { done <- msg.Content }

	raw, _ := json.Marshal(wireCardAction{
		Type: "card_action", SessionKey: "cloud_web:c:u", Action: "perm:allow", ReplyCtx: "ctx",
	})
	p.handleCardAction(raw)
	select {
	case content := <-done:
		if content != "allow" {
			t.Fatalf("content = %q, want allow", content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for perm action dispatch")
	}
}

func TestEmptyMessageIgnored(t *testing.T) {
	pAny, _ := New(map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	p := pAny.(*Platform)
	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	raw, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "e1", UserID: "u1", Content: "   ", ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if called {
		t.Fatal("expected empty message to be ignored")
	}
}

func TestDeriveBaseURL(t *testing.T) {
	if got := deriveBaseURL("https://gateway.example.com/cloud-web/v1/register"); got != "https://gateway.example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestGatewayDerivesBaseURLFromRegister(t *testing.T) {
	pAny, err := New(map[string]any{
		"token":        "t",
		"transport":    "gateway",
		"register_url": "https://gateway.example.com/cloud-web/v1/register",
		"listen":       ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	gt := pAny.(*Platform).tp.(*gatewayTransport)
	if gt.baseURL != "https://gateway.example.com" {
		t.Fatalf("baseURL = %q", gt.baseURL)
	}
}
