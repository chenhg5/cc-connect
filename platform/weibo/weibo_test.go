package weibo

import (
	"context"
	"encoding/base64"
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
		t.Error("handler should not be called for empty text without attachments")
	}
}

func TestHandleInbound_WithImage(t *testing.T) {
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

	imgData := []byte("fake-png-data")
	b64 := base64.StdEncoding.EncodeToString(imgData)

	payload := messagePayload{
		MessageID:  "img-1",
		FromUserID: "user1",
		Text:       "check this image",
		Input: []messageInputItem{{
			Type: "message",
			Role: "user",
			Content: []contentPart{{
				Type:     "input_image",
				FileName: "photo.png",
				Source:   &inputSource{Type: "base64", MediaType: "image/png", Data: b64},
			}},
		}},
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("handler not called")
	}
	if received.Content != "check this image" {
		t.Errorf("content = %q", received.Content)
	}
	if len(received.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(received.Images))
	}
	if received.Images[0].MimeType != "image/png" {
		t.Errorf("image mime = %q", received.Images[0].MimeType)
	}
	if received.Images[0].FileName != "photo.png" {
		t.Errorf("image filename = %q", received.Images[0].FileName)
	}
	if string(received.Images[0].Data) != "fake-png-data" {
		t.Errorf("image data mismatch")
	}
}

func TestHandleInbound_WithFile(t *testing.T) {
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

	fileData := []byte("hello world pdf content")
	b64 := base64.StdEncoding.EncodeToString(fileData)

	payload := messagePayload{
		MessageID:  "file-1",
		FromUserID: "user1",
		Text:       "",
		Input: []messageInputItem{{
			Type: "message",
			Role: "user",
			Content: []contentPart{
				{Type: "input_text", Text: "here is my file"},
				{
					Type:     "input_file",
					FileName: "doc.pdf",
					Source:   &inputSource{Type: "base64", MediaType: "application/pdf", Data: b64},
				},
			},
		}},
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("handler not called")
	}
	if received.Content != "here is my file" {
		t.Errorf("content = %q, want %q", received.Content, "here is my file")
	}
	if len(received.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(received.Files))
	}
	if received.Files[0].MimeType != "application/pdf" {
		t.Errorf("file mime = %q", received.Files[0].MimeType)
	}
	if received.Files[0].FileName != "doc.pdf" {
		t.Errorf("file name = %q", received.Files[0].FileName)
	}
}

func TestHandleInbound_ImageOnlyNoText(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "*",
		seen:      make(map[string]struct{}),
	}

	var received *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		received = msg
	}

	imgData := []byte("image-bytes")
	b64 := base64.StdEncoding.EncodeToString(imgData)

	payload := messagePayload{
		MessageID:  "imgonly-1",
		FromUserID: "user1",
		Text:       "",
		Input: []messageInputItem{{
			Type: "message",
			Role: "user",
			Content: []contentPart{{
				Type:   "input_image",
				Source: &inputSource{Type: "base64", MediaType: "image/jpeg", Data: b64},
			}},
		}},
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	if received == nil {
		t.Fatal("handler should be called for image-only message")
	}
	if len(received.Images) != 1 {
		t.Errorf("images = %d, want 1", len(received.Images))
	}
}

func TestHandleInbound_UnsupportedImageMime(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "*",
		seen:      make(map[string]struct{}),
	}

	var received *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		received = msg
	}

	b64 := base64.StdEncoding.EncodeToString([]byte("bmp-data"))

	payload := messagePayload{
		MessageID:  "bmp-1",
		FromUserID: "user1",
		Text:       "a bmp image",
		Input: []messageInputItem{{
			Type: "message",
			Role: "user",
			Content: []contentPart{{
				Type:   "input_image",
				Source: &inputSource{Type: "base64", MediaType: "image/bmp", Data: b64},
			}},
		}},
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	if received == nil {
		t.Fatal("handler should be called for text content")
	}
	if len(received.Images) != 0 {
		t.Errorf("unsupported image should be filtered, got %d images", len(received.Images))
	}
}

func TestHandleInbound_InputTextOverridesPayloadText(t *testing.T) {
	p := &Platform{
		name:      "weibo",
		allowFrom: "*",
		seen:      make(map[string]struct{}),
	}

	var received *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		received = msg
	}

	payload := messagePayload{
		MessageID:  "override-1",
		FromUserID: "user1",
		Text:       "payload text",
		Input: []messageInputItem{{
			Type: "message",
			Role: "user",
			Content: []contentPart{
				{Type: "input_text", Text: "input part 1"},
				{Type: "input_text", Text: "input part 2"},
			},
		}},
	}
	raw, _ := json.Marshal(payload)
	p.handleInbound(raw)

	if received == nil {
		t.Fatal("handler not called")
	}
	if received.Content != "input part 1\ninput part 2" {
		t.Errorf("content = %q, want joined input_text", received.Content)
	}
}

func TestNormalizeInboundInput_SkipsNonUserRole(t *testing.T) {
	payload := messagePayload{
		FromUserID: "user1",
		Text:       "fallback",
		Input: []messageInputItem{
			{
				Type: "message",
				Role: "assistant",
				Content: []contentPart{
					{Type: "input_text", Text: "should be ignored"},
				},
			},
		},
	}

	text, images, files := normalizeInboundInput(payload)
	if text != "fallback" {
		t.Errorf("text = %q, want fallback", text)
	}
	if len(images) != 0 || len(files) != 0 {
		t.Error("should have no attachments from non-user role")
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

func newWSTestPlatform(t *testing.T) (*Platform, chan map[string]any) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	gotMsg := make(chan map[string]any, 5)

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
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })

	p := &Platform{name: "weibo", ws: ws, seen: make(map[string]struct{})}
	return p, gotMsg
}

func TestSendImage(t *testing.T) {
	p, gotMsg := newWSTestPlatform(t)

	rctx := replyContext{fromUserID: "user1", sessionKey: "weibo:user1:user1"}
	imgData := []byte("fake-image-bytes")

	err := p.SendImage(context.Background(), rctx, core.ImageAttachment{
		MimeType: "image/png",
		Data:     imgData,
		FileName: "screenshot.png",
	})
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
		if payload["done"] != true {
			t.Errorf("done = %v", payload["done"])
		}

		input, ok := payload["input"].([]any)
		if !ok || len(input) == 0 {
			t.Fatal("input missing or empty")
		}
		item := input[0].(map[string]any)
		if item["role"] != "assistant" {
			t.Errorf("role = %v", item["role"])
		}
		content := item["content"].([]any)
		part := content[0].(map[string]any)
		if part["type"] != "input_image" {
			t.Errorf("part type = %v", part["type"])
		}
		if part["filename"] != "screenshot.png" {
			t.Errorf("filename = %v", part["filename"])
		}
		src := part["source"].(map[string]any)
		if src["media_type"] != "image/png" {
			t.Errorf("media_type = %v", src["media_type"])
		}
		decoded, _ := base64.StdEncoding.DecodeString(src["data"].(string))
		if string(decoded) != string(imgData) {
			t.Error("image data mismatch after round-trip")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSendFile(t *testing.T) {
	p, gotMsg := newWSTestPlatform(t)

	rctx := replyContext{fromUserID: "user1", sessionKey: "weibo:user1:user1"}
	fileData := []byte("pdf-content-here")

	err := p.SendFile(context.Background(), rctx, core.FileAttachment{
		MimeType: "application/pdf",
		Data:     fileData,
		FileName: "report.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-gotMsg:
		payload := m["payload"].(map[string]any)
		input := payload["input"].([]any)
		item := input[0].(map[string]any)
		content := item["content"].([]any)
		part := content[0].(map[string]any)
		if part["type"] != "input_file" {
			t.Errorf("part type = %v", part["type"])
		}
		if part["filename"] != "report.pdf" {
			t.Errorf("filename = %v", part["filename"])
		}
		src := part["source"].(map[string]any)
		if src["media_type"] != "application/pdf" {
			t.Errorf("media_type = %v", src["media_type"])
		}
		decoded, _ := base64.StdEncoding.DecodeString(src["data"].(string))
		if string(decoded) != string(fileData) {
			t.Error("file data mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSendImage_NotConnected(t *testing.T) {
	p := &Platform{name: "weibo", seen: make(map[string]struct{})}
	rctx := replyContext{fromUserID: "u1"}

	err := p.SendImage(context.Background(), rctx, core.ImageAttachment{Data: []byte("x")})
	if err == nil {
		t.Error("expected error when not connected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want 'not connected'", err.Error())
	}
}

func TestSendFile_InvalidContext(t *testing.T) {
	p := &Platform{name: "weibo", seen: make(map[string]struct{})}
	err := p.SendFile(context.Background(), "invalid", core.FileAttachment{Data: []byte("x")})
	if err == nil {
		t.Error("expected error for invalid context")
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ core.ImageSender = (*Platform)(nil)
	var _ core.FileSender = (*Platform)(nil)
}
