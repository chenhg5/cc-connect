package wpsxiezuo

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// ============================================================================
// Test helpers
// ============================================================================

// encryptEventForTest builds a valid encrypted event frame with proper
// signature, suitable for handleEvent end-to-end testing.
func encryptEventForTest(appID, appSecret, topic, operation string, payload any) wpsEventFrame {
	nonce := "testnonce1234567" // 16+ bytes

	// Serialize payload
	plain, _ := json.Marshal(payload)

	// Derive key & IV
	hash := md5.Sum([]byte(appSecret))
	key := []byte(hex.EncodeToString(hash[:]))
	iv := []byte(nonce[:16])

	// PKCS7 pad
	padLen := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// AES-CBC encrypt
	block, _ := aes.NewCipher(key)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	encryptedData := base64.StdEncoding.EncodeToString(ciphertext)
	timestamp := time.Now().Unix()

	// Sign: "access_key:topic:nonce:timestamp:encrypted_data"
	sigContent := fmt.Sprintf("%s:%s:%s:%d:%s", appID, topic, nonce, timestamp, encryptedData)
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(sigContent))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sig = strings.TrimRight(sig, "=")

	return wpsEventFrame{
		Topic:         topic,
		Operation:     operation,
		Time:          timestamp,
		Nonce:         nonce,
		Signature:     sig,
		EncryptedData: encryptedData,
		AccessKey:     appID,
	}
}

// ============================================================================
// New / Factory
// ============================================================================

func TestNew_MissingAppID(t *testing.T) {
	_, err := New(map[string]any{"app_secret": "s"})
	if err == nil {
		t.Fatal("expected error when app_id is missing")
	}
	if !strings.Contains(err.Error(), "app_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNew_MissingAppSecret(t *testing.T) {
	_, err := New(map[string]any{"app_id": "id"})
	if err == nil {
		t.Fatal("expected error when app_secret is missing")
	}
	if !strings.Contains(err.Error(), "app_secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNew_Valid(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-id",
		"app_secret": "test-secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "wps-xiezuo" {
		t.Fatalf("expected name wps-xiezuo, got %s", p.Name())
	}
}

func TestNew_CustomBaseURL(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   "https://custom.example.com/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plat := p.(*Platform)
	if plat.baseURL != "https://custom.example.com" {
		t.Fatalf("expected trimmed base_url, got %q", plat.baseURL)
	}
}

func TestNew_CleanReply(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":      "id",
		"app_secret":  "secret",
		"clean_reply": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plat := p.(*Platform)
	if !plat.cleanReply {
		t.Fatal("expected clean_reply=true")
	}
}

// ============================================================================
// Platform interface compliance
// ============================================================================

func TestPlatformImplementsInterfaces(t *testing.T) {
	var _ core.Platform = (*Platform)(nil)
	var _ core.ReplyContextReconstructor = (*Platform)(nil)
	var _ core.TypingIndicator = (*Platform)(nil)
	var _ core.TypingIndicatorDone = (*Platform)(nil)
}

// ============================================================================
// ReconstructReplyCtx
// ============================================================================

func TestReconstructReplyCtx_Valid(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("wps-xiezuo:comp123:chat456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		t.Fatal("expected replyContext type")
	}
	if rc.ChatID != "chat456" {
		t.Fatalf("expected chat456, got %s", rc.ChatID)
	}
	if rc.CompanyID != "comp123" {
		t.Fatalf("expected comp123, got %s", rc.CompanyID)
	}
}

func TestReconstructReplyCtx_P2PWithSenderKeepsActualChatID(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("wps-xiezuo:comp123:chat456:user789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		t.Fatal("expected replyContext type")
	}
	if rc.ChatID != "chat456" {
		t.Fatalf("expected chat456, got %s", rc.ChatID)
	}
	if rc.SenderID != "user789" {
		t.Fatalf("expected user789, got %s", rc.SenderID)
	}
	if rc.CompanyID != "comp123" {
		t.Fatalf("expected comp123, got %s", rc.CompanyID)
	}
}

func TestReconstructReplyCtx_InvalidPrefix(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("feishu:comp:chat")
	if err == nil {
		t.Fatal("expected error for invalid prefix")
	}
}

func TestReconstructReplyCtx_TooFewParts(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("wps-xiezuo:onlyone")
	if err == nil {
		t.Fatal("expected error for too few parts")
	}
}

// ============================================================================
// Text extraction
// ============================================================================

func TestExtractText_PlainString(t *testing.T) {
	raw := json.RawMessage(`"hello world"`)
	got := extractText(raw)
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestExtractText_TextType(t *testing.T) {
	raw := json.RawMessage(`{"type":"text","content":"hello"}`)
	got := extractText(raw)
	if got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestExtractText_TextTypeNestedDict(t *testing.T) {
	raw := json.RawMessage(`{"type":"text","content":{"content":"nested hello"}}`)
	got := extractText(raw)
	if got != "nested hello" {
		t.Fatalf("expected 'nested hello', got %q", got)
	}
}

func TestExtractText_RichText(t *testing.T) {
	raw := json.RawMessage(`{"type":"rich_text","content":[{"text":"hello"},{"text":"world"}]}`)
	got := extractText(raw)
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestExtractText_WPSRichTextRecursive(t *testing.T) {
	raw := json.RawMessage(`{"rich_text":{"elements":[{"type":"nl","elements":[{"type":"text","text_content":{"content":"第一行"}}]},{"type":"nl","elements":[{"type":"link","link_content":{"text":"说明","url":"https://example.com/doc"}}]},{"type":"nl","elements":[{"type":"mention","mention_content":{"text":"@张三"}},{"type":"text","style_text_content":{"text":" 请查看"}}]}]}}`)
	got := extractText(raw)
	want := "第一行\n说明 (https://example.com/doc)\n@张三 请查看"
	if got != want {
		t.Fatalf("rich text = %q, want %q", got, want)
	}
}

func TestParseWPSRichText_CustomEmojiImage(t *testing.T) {
	raw := json.RawMessage(`{"rich_text":{"elements":[{"type":"nl","elements":[{"type":"custom_emoji","image_content":{"name":"emoji.webp","size":12,"storage_key":"emoji-key","type":"image/webp"}}]}]}}`)
	text, images, _, err := parseWPSRichText(raw)
	if err != nil {
		t.Fatalf("parse rich text: %v", err)
	}
	if text != "" || len(images) != 1 || images[0].StorageKey != "emoji-key" {
		t.Fatalf("parsed = text:%q images:%+v, want one custom emoji image", text, images)
	}
}

func TestExtractText_FallbackContent(t *testing.T) {
	raw := json.RawMessage(`{"content":"fallback text"}`)
	got := extractText(raw)
	if got != "fallback text" {
		t.Fatalf("expected 'fallback text', got %q", got)
	}
}

func TestExtractText_Empty(t *testing.T) {
	got := extractText(nil)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// ============================================================================
// isP2P
// ============================================================================

func TestIsP2P(t *testing.T) {
	tests := []struct {
		chatType string
		want     bool
	}{
		{"p2p", true},
		{"single", true},
		{"direct", true},
		{"group", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isP2P(tt.chatType); got != tt.want {
			t.Errorf("isP2P(%q) = %v, want %v", tt.chatType, got, tt.want)
		}
	}
}

// ============================================================================
// Clean reply content
// ============================================================================

func TestCleanReplyContent(t *testing.T) {
	input := "normal line\n💭 thinking\n🔧 tool call\n🧾 output\nanother line"
	got := cleanReplyContent(input)
	want := "normal line\nanother line"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCleanReplyContent_AllFiltered(t *testing.T) {
	input := "💭 think\n🔧 tool"
	got := cleanReplyContent(input)
	if got != input {
		t.Fatalf("expected original %q, got %q", input, got)
	}
}

func TestCleanReplyContent_Empty(t *testing.T) {
	got := cleanReplyContent("")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// ============================================================================
// AES-256-CBC decryption (round-trip test)
// ============================================================================

func TestDecryptEventData_RoundTrip(t *testing.T) {
	appSecret := "test-secret-key-for-encryption"

	hash := md5.Sum([]byte(appSecret))
	key := []byte(hex.EncodeToString(hash[:]))

	nonce := "abcdefghijklmnop"
	iv := []byte(nonce[:16])

	plaintext := []byte(`{"chat_id":"c1","message_id":"m1","sender":{"sender_id":"u1","name":"Test"},"content":"hello"}`)

	padLen := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	block, _ := aes.NewCipher(key)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)

	encryptedData := base64.StdEncoding.EncodeToString(ciphertext)

	accessKey := "AK_TEST"
	timestamp := int64(1234567890)
	sigContent := fmt.Sprintf("%s:%s:%s:%d:%s", accessKey, "kso.app_chat.message", nonce, timestamp, encryptedData)
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(sigContent))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	expectedSig = strings.TrimRight(expectedSig, "=")

	event := wpsEventFrame{
		Topic:         "kso.app_chat.message",
		Operation:     "create",
		Time:          timestamp,
		Nonce:         nonce,
		Signature:     expectedSig,
		EncryptedData: encryptedData,
		AccessKey:     accessKey,
	}

	p := &Platform{appSecret: appSecret, appID: accessKey}

	if !p.verifyEventSignature(event) {
		t.Fatal("signature verification failed")
	}

	decrypted, err := p.decryptEventData(event.Nonce, event.EncryptedData)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted mismatch:\ngot:  %s\nwant: %s", decrypted, plaintext)
	}
}

// ============================================================================
// PKCS7 unpadding
// ============================================================================

func TestPKCS7Unpad_Valid(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x03, 0x03, 0x03}
	got, err := pkcs7Unpad(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected length 5, got %d", len(got))
	}
}

func TestPKCS7Unpad_InvalidPadding(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x09}
	_, err := pkcs7Unpad(data)
	if err == nil {
		t.Fatal("expected error for invalid padding")
	}
}

func TestPKCS7Unpad_EmptyData(t *testing.T) {
	_, err := pkcs7Unpad([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

// ============================================================================
// Signature verification
// ============================================================================

func TestVerifyEventSignature_Valid(t *testing.T) {
	appSecret := "my-secret"
	accessKey := "AK123"
	topic := "kso.app_chat.message"
	nonce := "nonce1234567890"
	timestamp := int64(1700000000)
	encryptedData := "dGVzdA=="

	content := fmt.Sprintf("%s:%s:%s:%d:%s", accessKey, topic, nonce, timestamp, encryptedData)
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(content))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sig = strings.TrimRight(sig, "=")

	p := &Platform{appSecret: appSecret, appID: accessKey}
	event := wpsEventFrame{
		Topic:         topic,
		Nonce:         nonce,
		Time:          timestamp,
		Signature:     sig,
		EncryptedData: encryptedData,
		AccessKey:     accessKey,
	}

	if !p.verifyEventSignature(event) {
		t.Fatal("signature should be valid")
	}
}

func TestVerifyEventSignature_Invalid(t *testing.T) {
	p := &Platform{appSecret: "secret", appID: "id"}
	event := wpsEventFrame{
		Topic:         "t",
		Nonce:         "n",
		Time:          1,
		Signature:     "badsig",
		AccessKey:     "ak",
		EncryptedData: "dA==",
	}

	if p.verifyEventSignature(event) {
		t.Fatal("signature should be invalid")
	}
}

// ============================================================================
// Raw message dispatch
// ============================================================================

func TestHandleRawMessage_ACK(t *testing.T) {
	p := &Platform{}
	raw := []byte(`{"type":"ack","nonce":"abc","code":200}`)
	p.handleRawMessage(context.Background(), raw)
}

func TestHandleRawMessage_GoAway(t *testing.T) {
	p := &Platform{}
	raw := []byte(`{"type":"goaway","reason":"server_shutdown","message":"bye"}`)
	p.handleRawMessage(context.Background(), raw)
	if p.stopped {
		t.Fatal("should not stop for server_shutdown")
	}
}

func TestHandleRawMessage_GoAwayReplaced(t *testing.T) {
	p := &Platform{}
	raw := []byte(`{"type":"goaway","reason":"connection_replaced","message":"bye"}`)
	p.handleRawMessage(context.Background(), raw)
	if !p.stopped {
		t.Fatal("should stop for connection_replaced")
	}
}

func TestHandleRawMessage_InvalidJSON(t *testing.T) {
	p := &Platform{}
	raw := []byte(`not json at all`)
	p.handleRawMessage(context.Background(), raw)
}

// ============================================================================
// KSO-1 signing
// ============================================================================

func TestSignWSHeader(t *testing.T) {
	p := &Platform{appID: "test-app", appSecret: "test-secret"}
	header, err := p.signWSHeader()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if header.Get("X-Kso-Date") == "" {
		t.Fatal("X-Kso-Date header missing")
	}
	auth := header.Get("X-Kso-Authorization")
	if !strings.HasPrefix(auth, "KSO-1 test-app:") {
		t.Fatalf("unexpected auth header: %q", auth)
	}
	if header.Get("X-Ack-Mode") != "required" {
		t.Fatal("X-Ack-Mode should be required")
	}
}

// ============================================================================
// Handle chat message (unit)
// ============================================================================

func TestHandleChatMessage_P2P(t *testing.T) {
	ch := make(chan *core.Message, 1)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	msgData := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "group1",
			Type: "p2p",
		},
		CompanyID: "comp1",
		Message: wpsMessageInfo{
			ID:      "msg1",
			Content: json.RawMessage(`{"type":"text","content":"hello"}`),
		},
	}
	msgData.Sender.ID = "user1"

	plain, _ := json.Marshal(msgData)
	p.handleChatMessage(plain)

	select {
	case received := <-ch:
		if received.SessionKey != "wps-xiezuo:comp1:group1:user1" {
			t.Fatalf("expected session key wps-xiezuo:comp1:group1:user1, got %s", received.SessionKey)
		}
		if received.Content != "hello" {
			t.Fatalf("expected content 'hello', got %q", received.Content)
		}
		rc, ok := received.ReplyCtx.(replyContext)
		if !ok {
			t.Fatal("expected replyContext")
		}
		if rc.ChatID != "group1" {
			t.Fatalf("expected reply chat id group1, got %s", rc.ChatID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected message to be delivered")
	}
}

func TestHandleChatMessage_Group(t *testing.T) {
	ch := make(chan *core.Message, 1)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	msgData := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "group1",
			Type: "group",
		},
		CompanyID: "comp1",
		Message: wpsMessageInfo{
			ID:      "msg2",
			Content: json.RawMessage(`"world"`),
		},
	}
	msgData.Sender.ID = "user1"

	plain, _ := json.Marshal(msgData)
	p.handleChatMessage(plain)

	select {
	case received := <-ch:
		if received.SessionKey != "wps-xiezuo:comp1:group1" {
			t.Fatalf("expected session key wps-xiezuo:comp1:group1, got %s", received.SessionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("expected message to be delivered")
	}
}

func TestHandleChatMessage_Duplicate(t *testing.T) {
	ch := make(chan *core.Message, 2)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	msgData := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "g1",
			Type: "group",
		},
		CompanyID: "c1",
		Message: wpsMessageInfo{
			ID:      "dup1",
			Content: json.RawMessage(`"hi"`),
		},
	}
	msgData.Sender.ID = "u1"

	plain, _ := json.Marshal(msgData)
	p.handleChatMessage(plain)
	p.handleChatMessage(plain)

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first message")
	}

	select {
	case <-ch:
		t.Fatal("expected duplicate to be filtered")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleChatMessage_EmptyContent(t *testing.T) {
	called := 0
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			called++
		},
		dedup: core.MessageDedup{},
	}

	msgData := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "g1",
			Type: "group",
		},
		CompanyID: "c1",
		Message: wpsMessageInfo{
			ID:      "msg3",
			Content: json.RawMessage(`""`),
		},
	}
	msgData.Sender.ID = "u1"

	plain, _ := json.Marshal(msgData)
	p.handleChatMessage(plain)

	if called != 0 {
		t.Fatalf("expected 0 calls for empty content, got %d", called)
	}
}

func TestHandleChatMessage_ImageAttachment(t *testing.T) {
	imageData := []byte{0xff, 0xd8, 0xff, 0xe0, 'j', 'p', 'e', 'g'}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "media-token", ExpiresIn: 7200})
		case r.URL.Path == "/v7/chats/chat-image/messages/msg-image/resources/image-key/download":
			assertWPSResourceAuth(t, r, "media-app", "media-secret", "media-token")
			if got := r.URL.Query().Get("file_name"); got != "photo.jpg" {
				t.Errorf("file_name = %q, want photo.jpg", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]string{"url": srv.URL + "/cdn/photo.jpg"},
			})
		case r.URL.Path == "/cdn/photo.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(imageData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 1)
	p := &Platform{
		appID:              "media-app",
		appSecret:          "media-secret",
		baseURL:            srv.URL,
		httpClient:         srv.Client(),
		maxAttachmentBytes: defaultMaxAttachmentBytes,
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-image", Type: "p2p"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-image",
			Type:    "image",
			Content: json.RawMessage(`{"image":{"height":1536,"name":"photo.jpg","size":8,"storage_key":"image-key","type":"image/jpeg","width":2048}}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		if len(msg.Images) != 1 || len(msg.Files) != 0 {
			t.Fatalf("attachments = images:%d files:%d, want images:1 files:0", len(msg.Images), len(msg.Files))
		}
		if got := msg.Images[0]; got.FileName != "photo.jpg" || got.MimeType != "image/jpeg" || !bytes.Equal(got.Data, imageData) {
			t.Fatalf("image = %+v, want downloaded JPEG attachment", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for image attachment")
	}
}

func TestHandleChatMessage_LocalFileAttachment(t *testing.T) {
	fileData := []byte("local file contents")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "file-token", ExpiresIn: 7200})
		case r.URL.Path == "/v7/chats/chat-file/messages/msg-file/resources/file-key/download":
			assertWPSResourceAuth(t, r, "file-app", "file-secret", "file-token")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]string{"url": srv.URL + "/cdn/report.txt"},
			})
		case r.URL.Path == "/cdn/report.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(fileData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 1)
	p := &Platform{
		appID:              "file-app",
		appSecret:          "file-secret",
		baseURL:            srv.URL,
		httpClient:         srv.Client(),
		maxAttachmentBytes: defaultMaxAttachmentBytes,
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-file", Type: "group"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-file",
			Type:    "file",
			Content: json.RawMessage(`{"file":{"local":{"name":"folder/report.txt","size":19,"storage_key":"file-key"},"type":"local"}}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		if len(msg.Files) != 1 || len(msg.Images) != 0 {
			t.Fatalf("attachments = files:%d images:%d, want files:1 images:0", len(msg.Files), len(msg.Images))
		}
		if got := msg.Files[0]; got.FileName != "report.txt" || got.MimeType != "text/plain" || !bytes.Equal(got.Data, fileData) {
			t.Fatalf("file = %+v, want downloaded text attachment", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for file attachment")
	}
}

func TestHandleChatMessage_CloudDocumentForwardsLink(t *testing.T) {
	received := make(chan *core.Message, 1)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-cloud", Type: "p2p"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-cloud",
			Type:    "file",
			Content: json.RawMessage(`{"file":{"cloud":{"id":"file-id","link_id":"link-id","link_url":"https://365.kdocs.cn/l/link-id"},"type":"cloud"}}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		if msg.Content != "https://365.kdocs.cn/l/link-id" {
			t.Fatalf("content = %q, want cloud document link", msg.Content)
		}
		if len(msg.Files) != 0 || len(msg.Images) != 0 {
			t.Fatalf("cloud document must not be presented as a downloaded attachment")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cloud document message")
	}
}

func TestHandleChatMessage_RichTextEmbeddedCloudDocument(t *testing.T) {
	received := make(chan *core.Message, 1)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-rich-doc", Type: "p2p"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-rich-doc",
			Type:    "rich_text",
			Content: json.RawMessage(`{"rich_text":{"elements":[{"alt_text":"","elements":[{"text_content":{"content":"但是我这个和你聊天的用户就具有访问权限啊"},"type":"text"}],"type":"nl"},{"alt_text":"","elements":[{"text_content":{"content":"下面这个能访问吗："},"type":"text"}],"type":"nl"},{"alt_text":"","elements":[{"doc_content":{"file":{"id":"file-id","link_id":"ctNKwQRGzbXC","link_url":"https://365.kdocs.cn/l/ctNKwQRGzbXC"},"text":"WPS协作OpenClaw插件多机器人_多Agent配置说明（官方）"},"type":"doc"}],"type":"nl"}]},"text":null}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		want := "但是我这个和你聊天的用户就具有访问权限啊\n下面这个能访问吗：\nWPS协作OpenClaw插件多机器人_多Agent配置说明（官方） (https://365.kdocs.cn/l/ctNKwQRGzbXC)"
		if msg.Content != want {
			t.Fatalf("content = %q, want %q", msg.Content, want)
		}
		if len(msg.Files) != 0 || len(msg.Images) != 0 {
			t.Fatalf("embedded cloud document must be forwarded as text and link")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for embedded cloud document")
	}
}

func TestHandleChatMessage_RichTextEmbeddedCloudDocumentReadsContent(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "doc-token", ExpiresIn: 7200})
		case "/v7/links/doc-link/meta":
			assertWPSResourceAuth(t, r, "doc-app", "doc-secret", "doc-token")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{
				"file_id": "doc-file", "drive_id": "doc-drive",
			}})
		case "/v7/drives/doc-drive/files/doc-file/content":
			assertWPSResourceAuth(t, r, "doc-app", "doc-secret", "doc-token")
			if got := r.URL.Query().Get("format"); got != "markdown" {
				t.Errorf("format = %q, want markdown", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{
				"markdown": "# 文档正文\n\n这是应用身份读取到的内容。",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 1)
	p := &Platform{
		appID:      "doc-app",
		appSecret:  "doc-secret",
		baseURL:    srv.URL,
		httpClient: srv.Client(),
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-doc-read", Type: "p2p"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-doc-read",
			Type:    "rich_text",
			Content: json.RawMessage(`{"rich_text":{"elements":[{"type":"nl","elements":[{"type":"doc","doc_content":{"text":"可读取文档","file":{"id":"doc-file","link_id":"doc-link","link_url":"https://365.kdocs.cn/l/doc-link"}}}]}]}}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		want := "可读取文档 (https://365.kdocs.cn/l/doc-link)\n\n[WPS云文档正文（已由应用授权读取，请优先基于以下正文回答，不要通过网页链接再次访问）]\n# 文档正文\n\n这是应用身份读取到的内容。"
		if msg.Content != want {
			t.Fatalf("content = %q, want %q", msg.Content, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for extracted cloud document")
	}
}

func TestHandleChatMessage_RichTextImageAttachment(t *testing.T) {
	imageData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth2/token":
			_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "rich-token", ExpiresIn: 7200})
		case r.URL.Path == "/v7/chats/chat-rich-image/messages/msg-rich-image/resources/rich-image-key/download":
			assertWPSResourceAuth(t, r, "rich-app", "rich-secret", "rich-token")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]string{"url": srv.URL + "/cdn/rich.png"},
			})
		case r.URL.Path == "/cdn/rich.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 1)
	p := &Platform{
		appID:              "rich-app",
		appSecret:          "rich-secret",
		baseURL:            srv.URL,
		httpClient:         srv.Client(),
		maxAttachmentBytes: defaultMaxAttachmentBytes,
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}
	payload := wpsMessageData{
		Chat:      wpsChatInfo{ID: "chat-rich-image", Type: "p2p"},
		CompanyID: "company",
		Message: wpsMessageInfo{
			ID:      "msg-rich-image",
			Type:    "rich_text",
			Content: json.RawMessage(`{"rich_text":{"elements":[{"type":"nl","elements":[{"type":"text","text_content":{"content":"看看这张图"}},{"type":"image","image_content":{"name":"rich.png","size":8,"storage_key":"rich-image-key","type":"image/png"}}]}]}}`),
		},
		Sender: wpsSenderInfo{ID: "user", Type: "user"},
	}
	plain, _ := json.Marshal(payload)
	p.handleChatMessage(plain)

	select {
	case msg := <-received:
		if msg.Content != "看看这张图" || len(msg.Images) != 1 {
			t.Fatalf("message = content:%q images:%d, want rich text and one image", msg.Content, len(msg.Images))
		}
		if got := msg.Images[0]; got.FileName != "rich.png" || got.MimeType != "image/png" || !bytes.Equal(got.Data, imageData) {
			t.Fatalf("image = %+v, want downloaded PNG attachment", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rich text image")
	}
}

func TestDownloadMessageResource_RejectsOversizeBody(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v7/chats/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]string{"url": srv.URL + "/cdn/too-large"},
			})
		case r.URL.Path == "/cdn/too-large":
			_, _ = w.Write([]byte("12345"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &Platform{
		appID:              "app",
		appSecret:          "secret",
		baseURL:            srv.URL,
		httpClient:         srv.Client(),
		maxAttachmentBytes: 4,
		token:              "cached-token",
		tokenExpire:        time.Now().Add(time.Hour),
	}
	_, _, err := p.downloadMessageResource("chat", "message", "key", "large.bin")
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("error = %v, want attachment size limit error", err)
	}
}

func assertWPSResourceAuth(t *testing.T, r *http.Request, appID, appSecret, token string) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q, want Bearer token", got)
	}
	date := r.Header.Get("X-Kso-Date")
	if date == "" {
		t.Fatal("X-Kso-Date is missing")
	}
	stringToSign := "KSO-1" + http.MethodGet + r.URL.RequestURI() + "" + date + ""
	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write([]byte(stringToSign))
	want := "KSO-1 " + appID + ":" + hex.EncodeToString(mac.Sum(nil))
	if got := r.Header.Get("X-Kso-Authorization"); got != want {
		t.Errorf("X-Kso-Authorization = %q, want %q", got, want)
	}
}

// ============================================================================
// Handle chat message recall
// ============================================================================

func TestHandleChatMessageRecall(t *testing.T) {
	ch := make(chan *core.Message, 1)
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
	}

	recallData := struct {
		ChatID    string `json:"chat_id"`
		ID        string `json:"id"`
		CompanyID string `json:"company_id"`
		Operator  struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"operator"`
	}{
		ChatID:    "g1",
		ID:        "msg-recall",
		CompanyID: "c1",
	}
	recallData.Operator.ID = "u1"
	recallData.Operator.Type = "user"

	plain, _ := json.Marshal(recallData)
	p.handleChatMessageRecall(plain)

	select {
	case received := <-ch:
		if !received.Recalled {
			t.Fatal("expected Recalled=true")
		}
	case <-time.After(time.Second):
		t.Fatal("expected recall message to be delivered")
	}
}

// ============================================================================
// Stop
// ============================================================================

func TestStop_Idempotent(t *testing.T) {
	p := &Platform{}
	if err := p.Stop(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("unexpected error on second stop: %v", err)
	}
}

// ============================================================================
// handleEvent — full chain: verify signature → decrypt → dispatch
// ============================================================================

func TestHandleEvent_FullChain_ChatMessage(t *testing.T) {
	appID := "AK_FULLCHAIN"
	appSecret := "fullchain-secret"

	ch := make(chan *core.Message, 1)
	p := &Platform{
		appID:     appID,
		appSecret: appSecret,
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	payload := map[string]any{
		"chat":       map[string]any{"id": "chat_fc", "type": "group"},
		"company_id": "comp_fc",
		"message":    map[string]any{"id": "msg_fc", "type": "text", "content": map[string]string{"type": "text", "content": "full chain works"}},
		"sender":     map[string]any{"id": "u_fc", "type": "user"},
	}

	event := encryptEventForTest(appID, appSecret, "kso.app_chat.message", "create", payload)
	p.handleEvent(event)

	select {
	case msg := <-ch:
		if msg.SessionKey != "wps-xiezuo:comp_fc:chat_fc" {
			t.Fatalf("expected session key wps-xiezuo:comp_fc:chat_fc, got %s", msg.SessionKey)
		}
		if msg.Content != "full chain works" {
			t.Fatalf("expected 'full chain works', got %q", msg.Content)
		}
		if msg.UserName != "u_fc" {
			t.Fatalf("expected UserName=u_fc, got %q", msg.UserName)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestHandleEvent_FullChain_Recall(t *testing.T) {
	appID := "AK_RECALL"
	appSecret := "recall-secret"

	ch := make(chan *core.Message, 1)
	p := &Platform{
		appID:     appID,
		appSecret: appSecret,
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
	}

	payload := map[string]any{
		"chat_id":    "chat_recall",
		"company_id": "comp_r",
		"id":         "msg_recall_chain",
		"operator":   map[string]string{"id": "u_recall", "type": "user"},
	}

	event := encryptEventForTest(appID, appSecret, "kso.app_chat.message.recall", "create", payload)
	p.handleEvent(event)

	select {
	case msg := <-ch:
		if !msg.Recalled {
			t.Fatal("expected Recalled=true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestHandleEvent_BadSignature(t *testing.T) {
	called := false
	p := &Platform{
		appID:     "bad",
		appSecret: "sig",
		handler: func(_ core.Platform, msg *core.Message) {
			called = true
		},
		dedup: core.MessageDedup{},
	}

	event := wpsEventFrame{
		Topic:         "kso.app_chat.message",
		Operation:     "create",
		Nonce:         "nonce1234567890",
		Signature:     "INVALID_SIGNATURE",
		EncryptedData: "dGVzdA==",
		AccessKey:     "bad",
		Time:          1,
	}

	p.handleEvent(event)
	if called {
		t.Fatal("handler should not be called for invalid signature")
	}
}

func TestHandleEvent_DoesNotLogDecryptedPayload(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	appID := "AK_LOG"
	appSecret := "log-secret"
	p := &Platform{
		appID:     appID,
		appSecret: appSecret,
		handler:   func(_ core.Platform, msg *core.Message) {},
		dedup:     core.MessageDedup{},
	}

	payload := map[string]any{
		"chat":       map[string]any{"id": "chat_log", "type": "group"},
		"company_id": "comp_log",
		"message":    map[string]any{"id": "msg_log", "type": "text", "content": map[string]string{"type": "text", "content": "SECRET_LOG_PAYLOAD"}},
		"sender":     map[string]any{"id": "u_log", "type": "user"},
	}

	event := encryptEventForTest(appID, appSecret, "kso.app_chat.message", "create", payload)
	p.handleEvent(event)

	if strings.Contains(buf.String(), "SECRET_LOG_PAYLOAD") {
		t.Fatalf("decrypted payload should not be logged, got logs: %s", buf.String())
	}
}

// ============================================================================
// allowFrom filtering
// ============================================================================

func TestHandleChatMessage_AllowFrom(t *testing.T) {
	ch := make(chan *core.Message, 1)
	p := &Platform{
		appID:     "id",
		appSecret: "secret",
		allowFrom: "allowed_user",
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	// authorized user
	msgData := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "g1",
			Type: "group",
		},
		CompanyID: "c1",
		Message: wpsMessageInfo{
			ID:      "m_allow1",
			Content: json.RawMessage(`"hi"`),
		},
	}
	msgData.Sender.ID = "allowed_user"
	plain, _ := json.Marshal(msgData)
	p.handleChatMessage(plain)

	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("authorized user should pass")
	}

	// unauthorized user
	msgData2 := wpsMessageData{
		Chat: wpsChatInfo{
			ID:   "g1",
			Type: "group",
		},
		CompanyID: "c1",
		Message: wpsMessageInfo{
			ID:      "m_allow2",
			Content: json.RawMessage(`"hi"`),
		},
	}
	msgData2.Sender.ID = "blocked_user"
	plain2, _ := json.Marshal(msgData2)
	p.handleChatMessage(plain2)

	select {
	case <-ch:
		t.Fatal("unauthorized user should be filtered")
	case <-time.After(200 * time.Millisecond):
	}
}

// ============================================================================
// sendWPSMessage via httptest server
// ============================================================================

func TestSendWPSMessage_Success(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "test-token-123", ExpiresIn: 7200})
			return
		}
		if r.URL.Path == "/v7/messages/create" {
			gotBody, _ = io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer test-token-123" {
				t.Errorf("expected Bearer token, got %s", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"code": "0"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	err := p.Reply(context.Background(), replyContext{ChatID: "chat_abc"}, "hello **world**")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gotBody) == 0 {
		t.Fatal("expected request body")
	}
	var req sendMessageRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if req.Receiver.ReceiverID != "chat_abc" {
		t.Fatalf("expected receiver_id=chat_abc, got %s", req.Receiver.ReceiverID)
	}
	if req.Content.Text.Content != "hello **world**" {
		t.Fatalf("unexpected content: %q", req.Content.Text.Content)
	}
	if req.Content.Text.Type != "markdown" {
		t.Fatalf("expected markdown type, got %s", req.Content.Text.Type)
	}
	// Regression guard: WPS v7 messages API outer Type is a strict enum
	// (text / rich_text / image / file / audio / video / card). Sending
	// "markdown" makes the API reject with 400000002. The inner
	// Content.Text.Type field is what opts into markdown rendering; outer
	// must remain "text".
	if req.Type != "text" {
		t.Fatalf("expected outer message type=text (markdown opt-in is via inner Content.Text.Type), got %q", req.Type)
	}
}

// TestSendWPSMessage_NewlinesConvertedToHardBreaks is the regression test for
// the "/status output renders as a single line" bug. cc-connect engine emits
// multi-line output separated by bare "\n", but WPS markdown (CommonMark)
// collapses single "\n" into a space, producing unreadable output. This
// confirms we transform the content to use markdown hard line breaks
// ("two spaces + \n") before sending.
func TestSendWPSMessage_NewlinesConvertedToHardBreaks(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			if err := json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200}); err != nil {
				t.Errorf("encode token response: %v", err)
			}
			return
		}
		if r.URL.Path == "/v7/messages/create" {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	// Simulated cc-connect engine /status output with bare "\n" separators.
	in := "cc-connect Status\n\nProject: foo\nAgent: claudecode\nUptime: 2m"
	if err := p.Reply(context.Background(), replyContext{ChatID: "c"}, in); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	var req sendMessageRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := "cc-connect Status  \n  \nProject: foo  \nAgent: claudecode  \nUptime: 2m"
	if req.Content.Text.Content != want {
		t.Fatalf("expected newlines converted to '  \\n', got %q", req.Content.Text.Content)
	}
}

// TestApplyWPSLineBreaks_Idempotent verifies the helper does not over-double
// newlines that already use the "  \n" hard-break form (idempotency matters
// because some upstream agents may emit markdown that already uses this).
func TestApplyWPSLineBreaks_Idempotent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no newline", "single line", "single line"},
		{"bare newlines", "a\nb\nc", "a  \nb  \nc"},
		{"already hard-broken", "a  \nb  \nc", "a  \nb  \nc"},
		{"mixed", "a\nb  \nc\nd", "a  \nb  \nc  \nd"},
		{"paragraph break (\\n\\n)", "para1\n\npara2", "para1  \n  \npara2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyWPSLineBreaks(tc.in)
			if got != tc.want {
				t.Fatalf("applyWPSLineBreaks(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Idempotency: applying twice must equal applying once.
			if again := applyWPSLineBreaks(got); again != got {
				t.Fatalf("not idempotent: %q -> %q -> %q", tc.in, got, again)
			}
		})
	}
}

func TestSendWPSMessage_CleanReply(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		if r.URL.Path == "/v7/messages/create" {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":      "id",
		"app_secret":  "secret",
		"base_url":    srv.URL,
		"clean_reply": true,
	})
	p := plat.(*Platform)

	err := p.Reply(context.Background(), replyContext{ChatID: "c1"}, "ok\n💭 think\n🔧 tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req sendMessageRequest
	json.Unmarshal(gotBody, &req)
	if req.Content.Text.Content != "ok" {
		t.Fatalf("expected cleaned content 'ok', got %q", req.Content.Text.Content)
	}
}

func TestSendWPSMessage_EmptyContent(t *testing.T) {
	plat, _ := New(map[string]any{"app_id": "id", "app_secret": "secret"})
	p := plat.(*Platform)
	err := p.Reply(context.Background(), replyContext{ChatID: "c1"}, "")
	if err != nil {
		t.Fatalf("expected nil error for empty content, got %v", err)
	}
}

func TestSendWPSMessage_ApiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		if r.URL.Path == "/v7/messages/create" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"403"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	err := p.Reply(context.Background(), replyContext{ChatID: "c1"}, "hi")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 in error, got %v", err)
	}
}

// ============================================================================
// getToken via httptest server
// ============================================================================

func TestGetToken_PrimaryEndpoint(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "primary-tok", ExpiresIn: 3600})
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	tok, err := p.getToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "primary-tok" {
		t.Fatalf("expected primary-tok, got %s", tok)
	}
	if hitPath != "/oauth2/token" {
		t.Fatalf("expected /oauth2/token, got %s", hitPath)
	}
}

func TestGetToken_FallbackEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/oauth2/token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fallback-tok", ExpiresIn: 7200})
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	tok, err := p.getToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "fallback-tok" {
		t.Fatalf("expected fallback-tok, got %s", tok)
	}
	if len(paths) != 2 || paths[0] != "/oauth2/token" || paths[1] != "/openapi/oauth2/token" {
		t.Fatalf("expected fallback path sequence, got %v", paths)
	}
}

func TestGetToken_Cached(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "cached-tok", ExpiresIn: 7200})
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	tok, err := p.getToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cached-tok" {
		t.Fatalf("expected cached-tok, got %s", tok)
	}

	tok2, err := p.getToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok2 != "cached-tok" {
		t.Fatalf("expected cached-tok, got %s", tok2)
	}

	if calls.Load() != 1 {
		t.Fatalf("expected 1 token HTTP call, got %d", calls.Load())
	}
}

func TestGetToken_AllEndpointsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	_, err := p.getToken(context.Background())
	if err == nil {
		t.Fatal("expected error when all endpoints fail")
	}
	if !strings.Contains(err.Error(), "all token endpoints failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ============================================================================
// Reaction API via httptest server
// ============================================================================

func TestAddReaction(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	err := p.addReaction(context.Background(), replyContext{
		ChatID:    "chat_r",
		MessageID: "msg_r",
	}, "emoji_busy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := "/v7/chats/chat_r/messages/msg_r/reactions/create"
	if gotPath != expectedPath {
		t.Fatalf("expected path %s, got %s", expectedPath, gotPath)
	}

	var req reactionRequest
	json.Unmarshal(gotBody, &req)
	if req.ReactionType != "emoji_busy" {
		t.Fatalf("expected emoji_busy, got %s", req.ReactionType)
	}
}

func TestDeleteReaction(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	err := p.deleteReaction(context.Background(), replyContext{
		ChatID:    "c_del",
		MessageID: "m_del",
	}, "emoji_busy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := "/v7/chats/c_del/messages/m_del/reactions/delete"
	if gotPath != expectedPath {
		t.Fatalf("expected path %s, got %s", expectedPath, gotPath)
	}
}

func TestAddReaction_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	err := p.addReaction(context.Background(), replyContext{
		ChatID:    "c",
		MessageID: "m",
	}, "emoji_busy")
	if err == nil {
		t.Fatal("expected error for 429")
	}
}

// ============================================================================
// TypingIndicator via httptest
// ============================================================================

func TestStartTyping(t *testing.T) {
	var addCalled atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		if strings.Contains(r.URL.Path, "reactions/create") {
			addCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	stop := p.StartTyping(context.Background(), replyContext{
		ChatID:    "c",
		MessageID: "m",
	})
	stop() // no-op

	if addCalled.Load() != 1 {
		t.Fatalf("expected 1 add reaction call, got %d", addCalled.Load())
	}
}

func TestStartTyping_NoMessageID(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	stop := p.StartTyping(context.Background(), replyContext{ChatID: "c"})
	stop()

	if calls.Load() != 0 {
		t.Fatalf("expected no HTTP calls without message_id, got %d", calls.Load())
	}
}

func TestAddDoneReaction(t *testing.T) {
	var delCalled atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "tok", ExpiresIn: 7200})
			return
		}
		if strings.Contains(r.URL.Path, "reactions/delete") {
			delCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	p.AddDoneReaction(replyContext{
		ChatID:    "c",
		MessageID: "m",
	})

	if delCalled.Load() != 1 {
		t.Fatalf("expected 1 delete reaction call, got %d", delCalled.Load())
	}
}

func TestAddDoneReaction_NoMessageID(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	plat, _ := New(map[string]any{
		"app_id":     "id",
		"app_secret": "secret",
		"base_url":   srv.URL,
	})
	p := plat.(*Platform)

	p.AddDoneReaction(replyContext{ChatID: "c"})

	if calls.Load() != 0 {
		t.Fatalf("expected no HTTP calls without message_id, got %d", calls.Load())
	}
}

// ============================================================================
// WebSocket integration: mock server → handleRawMessage
// ============================================================================

func TestWebSocketIntegration_ReceiveEvent(t *testing.T) {
	appID := "AK_WS"
	appSecret := "ws-secret"

	var up atomic.Int32
	upgrader := websocket.Upgrader{}
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Build encrypted event
		payload := map[string]any{
			"chat":       map[string]any{"id": "ws_chat", "type": "group"},
			"company_id": "ws_comp",
			"message":    map[string]any{"id": "ws_msg_1", "type": "text", "content": map[string]string{"type": "text", "content": "from ws"}},
			"sender":     map[string]any{"id": "ws_user", "type": "user"},
		}
		event := encryptEventForTest(appID, appSecret, "kso.app_chat.message", "create", payload)
		frameData, _ := json.Marshal(event)
		conn.WriteMessage(websocket.TextMessage, frameData)

		// Wait for ACK
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = conn.ReadMessage()
	}))
	defer wsSrv.Close()

	// Override wsEndpoint temporarily
	origEndpoint := wsEndpoint
	wsEndpoint = "ws" + strings.TrimPrefix(wsSrv.URL, "http")
	defer func() { wsEndpoint = origEndpoint }()

	ch := make(chan *core.Message, 1)
	p := &Platform{
		appID:     appID,
		appSecret: appSecret,
		handler: func(_ core.Platform, msg *core.Message) {
			ch <- msg
		},
		dedup: core.MessageDedup{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = p.runConnection(ctx)

	select {
	case msg := <-ch:
		if msg.Content != "from ws" {
			t.Fatalf("expected 'from ws', got %q", msg.Content)
		}
		if msg.SessionKey != "wps-xiezuo:ws_comp:ws_chat" {
			t.Fatalf("unexpected session key: %s", msg.SessionKey)
		}
	case <-time.After(3 * time.Second):
		if up.Load() == 0 {
			t.Skip("WebSocket server was not reached (env may not support ws dial)")
		}
		t.Fatal("timed out waiting for message from mock ws server")
	}
}
