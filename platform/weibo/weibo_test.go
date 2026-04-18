package weibo

import (
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
		{"missing both", map[string]any{}, true},
		{"missing app_secret", map[string]any{"app_id": "id"}, true},
		{"missing app_id", map[string]any{"app_secret": "secret"}, true},
		{"valid", map[string]any{"app_id": "id", "app_secret": "secret"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := New(tt.opts)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != "weibo" {
				t.Errorf("name = %q, want %q", p.Name(), "weibo")
			}
		})
	}
}

func TestNew_CustomName(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"name":       "my-weibo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "my-weibo" {
		t.Errorf("name = %q, want %q", p.Name(), "my-weibo")
	}
}

func TestNew_CustomEndpoints(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":         "id",
		"app_secret":     "secret",
		"token_endpoint": "https://custom.example.com/token",
		"ws_endpoint":    "ws://custom.example.com/ws",
	})
	if err != nil {
		t.Fatal(err)
	}
	plat := p.(*Platform)
	if plat.tokenEndpoint != "https://custom.example.com/token" {
		t.Errorf("tokenEndpoint = %q", plat.tokenEndpoint)
	}
	if plat.wsEndpoint != "ws://custom.example.com/ws" {
		t.Errorf("wsEndpoint = %q", plat.wsEndpoint)
	}
}

func TestSplitText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		limit  int
		chunks int
	}{
		{"short", "hello", 100, 1},
		{"exact", "abcde", 5, 1},
		{"split", "abcdefgh", 3, 3},
		{"empty", "", 10, 1},
		{"unicode", "你好世界测试", 3, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitText(tt.text, tt.limit)
			if len(result) != tt.chunks {
				t.Errorf("splitText(%q, %d) = %d chunks, want %d", tt.text, tt.limit, len(result), tt.chunks)
			}
			joined := strings.Join(result, "")
			if joined != tt.text {
				t.Errorf("joined = %q, want %q", joined, tt.text)
			}
		})
	}
}

func TestIsDuplicate(t *testing.T) {
	p := &Platform{seen: make(map[string]struct{})}

	if p.isDuplicate("msg1") {
		t.Error("first occurrence should not be duplicate")
	}
	if !p.isDuplicate("msg1") {
		t.Error("second occurrence should be duplicate")
	}
	if p.isDuplicate("msg2") {
		t.Error("different message should not be duplicate")
	}
}

func TestIsDuplicate_Prune(t *testing.T) {
	p := &Platform{seen: make(map[string]struct{})}

	for i := 0; i < maxSeenMessages+100; i++ {
		p.isDuplicate(strings.Repeat("x", 10) + string(rune(i)))
	}
	if len(p.seen) > maxSeenMessages {
		t.Errorf("seen map should be pruned, got %d entries", len(p.seen))
	}
}

func TestHandleInbound(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "*",
		seen:      make(map[string]struct{}),
	}

	var received *core.Message
	var mu sync.Mutex
	p.handler = func(_ core.Platform, msg *core.Message) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}

	payload := messagePayload{
		MessageID:  "test-123",
		FromUserID: "user1",
		Text:       "hello world",
		Timestamp:  1234567890,
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("handler not called")
	}
	if received.SessionKey != "weibo:user1:user1" {
		t.Errorf("sessionKey = %q", received.SessionKey)
	}
	if received.Content != "hello world" {
		t.Errorf("content = %q", received.Content)
	}
	if received.UserID != "user1" {
		t.Errorf("userID = %q", received.UserID)
	}
	if received.MessageID != "test-123" {
		t.Errorf("messageID = %q", received.MessageID)
	}
}

func TestHandleInbound_AllowList(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "user2,user3",
		seen:      make(map[string]struct{}),
	}

	called := false
	p.handler = func(_ core.Platform, _ *core.Message) {
		called = true
	}

	payload := messagePayload{
		MessageID:  "blocked-1",
		FromUserID: "user1",
		Text:       "hello",
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	if called {
		t.Error("handler should not be called for unauthorized user")
	}
}

func TestHandleInbound_EmptyText(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "*",
		seen:      make(map[string]struct{}),
	}

	called := false
	p.handler = func(_ core.Platform, _ *core.Message) {
		called = true
	}

	payload := messagePayload{
		MessageID:  "empty-1",
		FromUserID: "user1",
		Text:       "",
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	if called {
		t.Error("handler should not be called for empty text")
	}
}

func TestRefreshToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"token":     "test-token-abc",
				"expire_in": 3600,
				"uid":       12345,
			},
		})
	}))
	defer ts.Close()

	p := &Platform{
		appID:         "test-app",
		appSecret:     "test-secret",
		tokenEndpoint: ts.URL,
		seen:          make(map[string]struct{}),
	}

	tok, err := p.refreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "test-token-abc" {
		t.Errorf("token = %q, want %q", tok, "test-token-abc")
	}
	if p.uid != "12345" {
		t.Errorf("uid = %q, want %q", p.uid, "12345")
	}
}

func TestSendMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	gotMsg := make(chan map[string]any, 1)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			json.Unmarshal(msg, &m)
			gotMsg <- m
		}
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	p := &Platform{
		name: "weibo",
		ws:   ws,
		seen: make(map[string]struct{}),
	}

	rctx := replyContext{fromUserID: "user1", sessionKey: "weibo:user1:user1"}
	err = p.sendMessage(rctx, "short message")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-gotMsg:
		if m["type"] != "send_message" {
			t.Errorf("type = %v", m["type"])
		}
		payload := m["payload"].(map[string]any)
		if payload["toUserId"] != "user1" {
			t.Errorf("toUserId = %v", payload["toUserId"])
		}
		if payload["text"] != "short message" {
			t.Errorf("text = %v", payload["text"])
		}
		if payload["done"] != true {
			t.Errorf("done = %v", payload["done"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}
