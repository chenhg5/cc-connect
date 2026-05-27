package dingtalk

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

// ──────────────────────────────────────────────────────────────
// Thread safety tests for token caching
// ──────────────────────────────────────────────────────────────

func TestGetAccessToken_ConcurrentAccess(t *testing.T) {
	// This test verifies that concurrent calls to getAccessToken
	// with a pre-cached token are properly synchronized by the mutex

	p := &Platform{
		clientID:     "test_client",
		clientSecret: "test_secret",
		httpClient:   &http.Client{}, // Valid HTTP client
		accessToken:  "test_token",   // Pre-cache a token
		tokenExpiry:  time.Now().Add(1 * time.Hour),
	}

	// Launch multiple goroutines to stress-test the mutex
	const numGoroutines = 100
	var wg sync.WaitGroup
	successCount := 0
	var countMu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := p.getAccessToken()
			if err == nil && token == "test_token" {
				countMu.Lock()
				successCount++
				countMu.Unlock()
			}
		}()
	}

	wg.Wait()

	// All goroutines should have gotten the cached token
	if successCount != numGoroutines {
		t.Errorf("expected %d successful token retrievals, got %d", numGoroutines, successCount)
	}

	t.Logf("Completed %d concurrent token requests without deadlock", numGoroutines)
}

func TestGetAccessToken_MutexExists(t *testing.T) {
	// Verify that the tokenMu mutex field exists and works
	p := &Platform{
		clientID:    "test_client",
		clientSecret: "test_secret",
	}

	// Test that we can lock/unlock the mutex (verify no panic under lock)
	p.tokenMu.Lock()
	_ = p.clientID // SA2001: intentional empty section to verify Lock/Unlock work
	p.tokenMu.Unlock()

	// Test with defer
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	t.Log("tokenMu mutex is functional")
}

func TestGetAccessToken_CachedTokenAccess(t *testing.T) {
	// Test that cached token access is thread-safe
	p := &Platform{
		clientID:     "test_client",
		clientSecret: "test_secret",
		accessToken:  "cached_token",
		tokenExpiry:  time.Now().Add(1 * time.Hour),
	}

	const numGoroutines = 50
	var wg sync.WaitGroup
	tokens := make([]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			token, err := p.getAccessToken()
			if err == nil {
				tokens[idx] = token
			}
		}(i)
	}

	wg.Wait()

	// Verify all goroutines got the same cached token
	for i, token := range tokens {
		if token != "" && token != "cached_token" {
			t.Errorf("goroutine %d: expected cached token 'cached_token', got %q", i, token)
		}
	}

	t.Logf("All %d goroutines safely accessed cached token", numGoroutines)
}

func TestPlatform_MutexFieldExists(t *testing.T) {
	// Verify the Platform struct has the tokenMu field
	p := &Platform{}

	// Verify no panic under lock (test will fail to compile if tokenMu doesn't exist)
	p.tokenMu.Lock()
	_ = p.clientID // SA2001: intentional empty section to verify Lock/Unlock work
	p.tokenMu.Unlock()

	t.Log("Platform.tokenMu field exists")
}

func TestPlatform_AccessTokenFieldsExist(t *testing.T) {
	// Verify the Platform struct has the token caching fields
	p := &Platform{}

	// Set the fields
	p.accessToken = "test_token"
	p.tokenExpiry = time.Now().Add(1 * time.Hour)

	// Verify they're set
	if p.accessToken != "test_token" {
		t.Errorf("expected accessToken 'test_token', got %q", p.accessToken)
	}

	t.Log("Platform token caching fields exist and are accessible")
}

// ──────────────────────────────────────────────────────────────
// ReconstructReplyCtx tests
// ──────────────────────────────────────────────────────────────

func TestReconstructReplyCtx_GroupSharedSession(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("dingtalk:g:conv123")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.conversationId != "conv123" {
		t.Errorf("conversationId = %q, want %q", rc.conversationId, "conv123")
	}
	if rc.senderStaffId != "" {
		t.Errorf("senderStaffId = %q, want empty", rc.senderStaffId)
	}
	if !rc.isGroup {
		t.Error("isGroup = false, want true for group session")
	}
	if !rc.proactive {
		t.Error("proactive = false, want true")
	}
}

func TestReconstructReplyCtx_GroupPerUserSession(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("dingtalk:g:conv123:user456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.conversationId != "conv123" {
		t.Errorf("conversationId = %q, want %q", rc.conversationId, "conv123")
	}
	if rc.senderStaffId != "user456" {
		t.Errorf("senderStaffId = %q, want %q", rc.senderStaffId, "user456")
	}
	if !rc.isGroup {
		t.Error("isGroup = false, want true for group session")
	}
}

func TestReconstructReplyCtx_DirectSession(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("dingtalk:d:conv789:user111")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.conversationId != "conv789" {
		t.Errorf("conversationId = %q, want %q", rc.conversationId, "conv789")
	}
	if rc.senderStaffId != "user111" {
		t.Errorf("senderStaffId = %q, want %q", rc.senderStaffId, "user111")
	}
	if rc.isGroup {
		t.Error("isGroup = true, want false for direct session")
	}
	if !rc.proactive {
		t.Error("proactive = false, want true")
	}
}

func TestReconstructReplyCtx_InvalidPrefix(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("telegram:g:conv123")
	if err == nil {
		t.Fatal("expected error for non-dingtalk prefix")
	}
}

func TestReconstructReplyCtx_InvalidConvType(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("dingtalk:x:conv123")
	if err == nil {
		t.Fatal("expected error for invalid conversation type")
	}
}

func TestReconstructReplyCtx_EmptyConversationId(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("dingtalk:g:")
	if err == nil {
		t.Fatal("expected error for empty conversationId")
	}
}

func TestReconstructReplyCtx_TooFewParts(t *testing.T) {
	p := &Platform{}
	_, err := p.ReconstructReplyCtx("dingtalk:")
	if err == nil {
		t.Fatal("expected error for too few parts")
	}
}

// ──────────────────────────────────────────────────────────────
// formatReplyContent tests
// ──────────────────────────────────────────────────────────────

func TestFormatReplyContent_WithQuotedText(t *testing.T) {
	p := &Platform{}
	repliedContent, _ := json.Marshal(repliedTextContent{Text: "original message"})
	richText := &richTextContent{
		Content:    "user reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "text",
			Content: repliedContent,
		},
	}
	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"original message\"\n\nuser reply"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_EmptyContent_UsesFallback(t *testing.T) {
	p := &Platform{}
	repliedContent, _ := json.Marshal(repliedTextContent{Text: "quoted"})
	richText := &richTextContent{
		Content:    "",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "text",
			Content: repliedContent,
		},
	}
	result := p.formatReplyContent(richText, "fallback text")
	expected := "引用: \"quoted\"\n\nfallback text"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_NilRepliedMsg(t *testing.T) {
	p := &Platform{}
	richText := &richTextContent{
		Content:    "just a message",
		IsReplyMsg: true,
		RepliedMsg: nil,
	}
	result := p.formatReplyContent(richText, "fallback")
	if result != "just a message" {
		t.Errorf("formatReplyContent() = %q, want %q", result, "just a message")
	}
}

func TestFormatReplyContent_NonTextMsgType(t *testing.T) {
	p := &Platform{}
	richText := &richTextContent{
		Content:    "user reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "image",
			Content: json.RawMessage(`{}`),
		},
	}
	result := p.formatReplyContent(richText, "fallback")
	if result != "user reply" {
		t.Errorf("formatReplyContent() = %q, want %q", result, "user reply")
	}
}

func TestFormatReplyContent_EmptyQuotedText(t *testing.T) {
	p := &Platform{}
	repliedContent, _ := json.Marshal(repliedTextContent{Text: ""})
	richText := &richTextContent{
		Content:    "user reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "text",
			Content: repliedContent,
		},
	}
	result := p.formatReplyContent(richText, "fallback")
	if result != "user reply" {
		t.Errorf("formatReplyContent() = %q, want %q", result, "user reply")
	}
}

// ──────────────────────────────────────────────────────────────
// Proactive routing tests
// ──────────────────────────────────────────────────────────────

func TestProactiveRouting_GroupSessionUsesGroupAPI(t *testing.T) {
	// Verify that a group session key produces a replyContext with isGroup=true,
	// which sendProactiveMessage would route to groupMessages/send.
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("dingtalk:g:conv123:user456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if !rc.isGroup || rc.conversationId == "" {
		t.Errorf("group routing: isGroup=%v, conversationId=%q; want isGroup=true with non-empty conversationId", rc.isGroup, rc.conversationId)
	}
}

func TestProactiveRouting_DirectSessionUsesDirectAPI(t *testing.T) {
	// Verify that a direct session key produces a replyContext with isGroup=false,
	// which sendProactiveMessage would route to oToMessages/batchSend.
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("dingtalk:d:conv789:user111")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.isGroup {
		t.Error("direct routing: isGroup=true, want false for 1:1 session")
	}
	if rc.senderStaffId != "user111" {
		t.Errorf("direct routing: senderStaffId=%q, want %q", rc.senderStaffId, "user111")
	}
}

// ──────────────────────────────────────────────────────────────
// onMessage: richText branch must propagate isGroup so that proactive
// sends after sessionWebhook expiry route to the group API rather than
// to a 1:1 DM with the original sender.
// ──────────────────────────────────────────────────────────────

func TestOnMessage_RichTextInGroup_SetsIsGroup(t *testing.T) {
	var captured *core.Message
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			captured = msg
		},
	}

	data := &chatbot.BotCallbackDataModel{
		MsgId:            "msg-rt-group-1",
		Msgtype:          "richText",
		ConversationId:   "conv-group",
		ConversationType: "2",
		SenderStaffId:    "user1",
		SenderNick:       "Alice",
		Content: map[string]interface{}{
			"richText": []interface{}{
				map[string]interface{}{"text": "hello"},
			},
		},
	}
	p.onMessage(data, &richTextContent{})

	if captured == nil {
		t.Fatal("handler was not invoked for richText message")
	}
	rc, ok := captured.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx = %T, want replyContext", captured.ReplyCtx)
	}
	if !rc.isGroup {
		t.Errorf("richText in ConversationType=2 group: isGroup=false, want true (proactive send would misroute to 1:1 DM)")
	}
}

func TestOnMessage_RichTextDirect_KeepsIsGroupFalse(t *testing.T) {
	var captured *core.Message
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			captured = msg
		},
	}

	data := &chatbot.BotCallbackDataModel{
		MsgId:            "msg-rt-direct-1",
		Msgtype:          "richText",
		ConversationId:   "conv-direct",
		ConversationType: "1",
		SenderStaffId:    "user2",
		SenderNick:       "Bob",
		Content: map[string]interface{}{
			"richText": []interface{}{
				map[string]interface{}{"text": "hi"},
			},
		},
	}
	p.onMessage(data, &richTextContent{})

	if captured == nil {
		t.Fatal("handler was not invoked for richText message")
	}
	rc, ok := captured.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx = %T, want replyContext", captured.ReplyCtx)
	}
	if rc.isGroup {
		t.Errorf("richText in 1:1 chat: isGroup=true, want false")
	}
}

// ──────────────────────────────────────────────────────────────
// extractRichText tests (from main: richText message type support)
// ──────────────────────────────────────────────────────────────

func TestExtractRichText(t *testing.T) {
	tests := []struct {
		name    string
		content interface{}
		want    string
	}{
		{
			name:    "nil content",
			content: nil,
			want:    "",
		},
		{
			name:    "non-map content",
			content: "not a map",
			want:    "",
		},
		{
			name: "empty richText array",
			content: map[string]interface{}{
				"richText": []interface{}{},
			},
			want: "",
		},
		{
			name: "single text element",
			content: map[string]interface{}{
				"richText": []interface{}{
					map[string]interface{}{"text": "Hello World"},
				},
			},
			want: "Hello World",
		},
		{
			name: "multiple text elements",
			content: map[string]interface{}{
				"richText": []interface{}{
					map[string]interface{}{"text": "Hello "},
					map[string]interface{}{"text": "World"},
				},
			},
			want: "Hello World",
		},
		{
			name: "text with attrs (bold etc) — attrs ignored, text extracted",
			content: map[string]interface{}{
				"richText": []interface{}{
					map[string]interface{}{"text": "normal "},
					map[string]interface{}{"text": "bold", "attrs": map[string]interface{}{"bold": true}},
				},
			},
			want: "normal bold",
		},
		{
			name: "mixed text and picture elements — pictures skipped",
			content: map[string]interface{}{
				"richText": []interface{}{
					map[string]interface{}{"text": "See image: "},
					map[string]interface{}{"pictureDownloadCode": "abc123"},
					map[string]interface{}{"text": "done"},
				},
			},
			want: "See image: done",
		},
		{
			name: "missing richText key",
			content: map[string]interface{}{
				"other": "data",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRichText(tt.content)
			if got != tt.want {
				t.Errorf("extractRichText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────
// Token expiry fallback when server returns missing/invalid expireIn
// ──────────────────────────────────────────────────────────────

// fakeAccessTokenRT serves a single canned /oauth2/accessToken response
// regardless of the request URL — enough to exercise getAccessToken's
// caching arithmetic without hitting the real DingTalk API.
type fakeAccessTokenRT struct {
	body string
}

func (f *fakeAccessTokenRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestGetAccessToken_ZeroExpireIn_FallsBackToDefault(t *testing.T) {
	p := &Platform{
		clientID:     "test_client",
		clientSecret: "test_secret",
		httpClient: &http.Client{
			Transport: &fakeAccessTokenRT{body: `{"accessToken":"tok-zero","expireIn":0}`},
		},
	}

	before := time.Now()
	tok, err := p.getAccessToken()
	if err != nil {
		t.Fatalf("getAccessToken() error = %v", err)
	}
	if tok != "tok-zero" {
		t.Fatalf("token = %q, want %q", tok, "tok-zero")
	}

	// Without the fallback, tokenExpiry would land at "before" (now+0s), making
	// time.Now().Before(tokenExpiry) immediately false — every subsequent call
	// would re-fetch a token. Assert the cache window is meaningful (>= 1h).
	gotWindow := p.tokenExpiry.Sub(before)
	if gotWindow < time.Hour {
		t.Errorf("tokenExpiry window = %v from response, want >= 1h (zero-expireIn should fall back, not cache for 0s)", gotWindow)
	}
}

func TestGetAccessToken_NegativeExpireIn_FallsBackToDefault(t *testing.T) {
	p := &Platform{
		clientID:     "test_client",
		clientSecret: "test_secret",
		httpClient: &http.Client{
			Transport: &fakeAccessTokenRT{body: `{"accessToken":"tok-neg","expireIn":-1}`},
		},
	}

	before := time.Now()
	if _, err := p.getAccessToken(); err != nil {
		t.Fatalf("getAccessToken() error = %v", err)
	}
	if p.tokenExpiry.Sub(before) < time.Hour {
		t.Errorf("tokenExpiry window for expireIn=-1 = %v, want >= 1h", p.tokenExpiry.Sub(before))
	}
}

func TestGetAccessToken_NormalExpireIn_AppliesBuffer(t *testing.T) {
	p := &Platform{
		clientID:     "test_client",
		clientSecret: "test_secret",
		httpClient: &http.Client{
			Transport: &fakeAccessTokenRT{body: `{"accessToken":"tok-7200","expireIn":7200}`},
		},
	}

	before := time.Now()
	if _, err := p.getAccessToken(); err != nil {
		t.Fatalf("getAccessToken() error = %v", err)
	}
	// 7200 - 300 buffer = 6900s = 115min. Allow tolerance for elapsed time.
	gotWindow := p.tokenExpiry.Sub(before)
	if gotWindow < 100*time.Minute || gotWindow > 116*time.Minute {
		t.Errorf("tokenExpiry window for expireIn=7200 = %v, want ~6900s (100-116min)", gotWindow)
	}
}
