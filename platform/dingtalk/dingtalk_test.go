package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ──────────────────────────────────────────────────────────────
// Thread safety tests for token caching
// ──────────────────────────────────────────────────────────────

type dingtalkEmotionRequest struct {
	method string
	path   string
	token  string
	body   map[string]any
}

type rewriteDingTalkTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt rewriteDingTalkTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.base.RoundTrip(clone)
}

type failRoundTripper struct {
	t *testing.T
}

func (rt failRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.t.Fatalf("unexpected HTTP call to %s", req.URL.String())
	return nil, nil
}

func newEmotionTestPlatform(t *testing.T, reactionEmoji, doneEmoji string) (*Platform, <-chan dingtalkEmotionRequest) {
	t.Helper()

	requests := make(chan dingtalkEmotionRequest, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- dingtalkEmotionRequest{
			method: r.Method,
			path:   r.URL.Path,
			token:  r.Header.Get("x-acs-dingtalk-access-token"),
			body:   body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	return &Platform{
		clientID:      "test_client",
		clientSecret:  "test_secret",
		robotCode:     "test_robot",
		httpClient:    &http.Client{Transport: rewriteDingTalkTransport{target: targetURL, base: http.DefaultTransport}},
		accessToken:   "test_token",
		tokenExpiry:   time.Now().Add(time.Hour),
		reactionEmoji: reactionEmoji,
		doneEmoji:     doneEmoji,
	}, requests
}

func waitEmotionRequest(t *testing.T, requests <-chan dingtalkEmotionRequest) dingtalkEmotionRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for emotion request")
		return dingtalkEmotionRequest{}
	}
}

func assertEmotionRequest(t *testing.T, req dingtalkEmotionRequest, path, emoji string) {
	t.Helper()
	if req.method != http.MethodPost {
		t.Fatalf("method = %q, want %q", req.method, http.MethodPost)
	}
	if req.path != path {
		t.Fatalf("path = %q, want %q", req.path, path)
	}
	if req.token != "test_token" {
		t.Fatalf("access token header = %q, want test_token", req.token)
	}
	if req.body["robotCode"] != "test_robot" {
		t.Fatalf("robotCode = %v, want test_robot", req.body["robotCode"])
	}
	if req.body["openMsgId"] != "msg_123" {
		t.Fatalf("openMsgId = %v, want msg_123", req.body["openMsgId"])
	}
	if req.body["openConversationId"] != "conv_123" {
		t.Fatalf("openConversationId = %v, want conv_123", req.body["openConversationId"])
	}
	if req.body["emotionName"] != emoji {
		t.Fatalf("emotionName = %v, want %q", req.body["emotionName"], emoji)
	}
	if req.body["emotionType"] != float64(2) {
		t.Fatalf("emotionType = %v, want 2", req.body["emotionType"])
	}
	textEmotion, ok := req.body["textEmotion"].(map[string]any)
	if !ok {
		t.Fatalf("textEmotion should be an object, got %T", req.body["textEmotion"])
	}
	if textEmotion["emotionId"] != "2659900" {
		t.Fatalf("textEmotion.emotionId = %v, want 2659900", textEmotion["emotionId"])
	}
	if textEmotion["backgroundId"] != "im_bg_1" {
		t.Fatalf("textEmotion.backgroundId = %v, want im_bg_1", textEmotion["backgroundId"])
	}
	if textEmotion["emotionName"] != emoji || textEmotion["text"] != emoji {
		t.Fatalf("textEmotion emoji fields = %#v, want %q", textEmotion, emoji)
	}
}

func validEmotionReplyContext() replyContext {
	return replyContext{
		sessionWebhook: "https://example.test/webhook",
		conversationId: "conv_123",
		senderStaffId:  "staff_123",
		messageId:      "msg_123",
	}
}

func TestNewReactionOptions(t *testing.T) {
	baseOpts := map[string]any{
		"client_id":     "test_client",
		"client_secret": "test_secret",
		"robot_code":    "test_robot",
	}

	t.Run("reaction enabled by default", func(t *testing.T) {
		p, err := New(baseOpts)
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		got := p.(*Platform)
		if got.reactionEmoji != "🤔思考中" {
			t.Fatalf("reactionEmoji = %q, want default value", got.reactionEmoji)
		}
		if got.doneEmoji != "" {
			t.Fatalf("doneEmoji = %q, want empty", got.doneEmoji)
		}
	})

	t.Run("configured", func(t *testing.T) {
		opts := map[string]any{
			"client_id":      "test_client",
			"client_secret":  "test_secret",
			"robot_code":     "test_robot",
			"reaction_emoji": "🤔思考中",
			"done_emoji":     "✅完成",
		}
		p, err := New(opts)
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		got := p.(*Platform)
		if got.reactionEmoji != "🤔思考中" {
			t.Fatalf("reactionEmoji = %q, want configured value", got.reactionEmoji)
		}
		if got.doneEmoji != "✅完成" {
			t.Fatalf("doneEmoji = %q, want configured value", got.doneEmoji)
		}
	})

	t.Run("none disables", func(t *testing.T) {
		opts := map[string]any{
			"client_id":      "test_client",
			"client_secret":  "test_secret",
			"robot_code":     "test_robot",
			"reaction_emoji": "none",
			"done_emoji":     "NONE",
		}
		p, err := New(opts)
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		got := p.(*Platform)
		if got.reactionEmoji != "" {
			t.Fatalf("reactionEmoji = %q, want empty", got.reactionEmoji)
		}
		if got.doneEmoji != "" {
			t.Fatalf("doneEmoji = %q, want empty", got.doneEmoji)
		}
	})
}

func TestStartTypingDisabledDoesNotCallHTTP(t *testing.T) {
	p := &Platform{
		httpClient:  &http.Client{Transport: failRoundTripper{t: t}},
		accessToken: "test_token",
		tokenExpiry: time.Now().Add(time.Hour),
	}

	stop := p.StartTyping(context.Background(), validEmotionReplyContext())
	stop()
}

func TestStartTypingSendsAndRecallsEmotion(t *testing.T) {
	p, requests := newEmotionTestPlatform(t, "🤔思考中", "")

	stop := p.StartTyping(context.Background(), validEmotionReplyContext())
	assertEmotionRequest(t, waitEmotionRequest(t, requests), "/v1.0/robot/emotion/reply", "🤔思考中")

	stop()
	assertEmotionRequest(t, waitEmotionRequest(t, requests), "/v1.0/robot/emotion/recall", "🤔思考中")
}

func TestAddDoneReactionSendsEmotionWhenConfigured(t *testing.T) {
	p, requests := newEmotionTestPlatform(t, "", "✅完成")

	p.AddDoneReaction(validEmotionReplyContext())
	assertEmotionRequest(t, waitEmotionRequest(t, requests), "/v1.0/robot/emotion/reply", "✅完成")
}

func TestAddDoneReactionDisabledDoesNotCallHTTP(t *testing.T) {
	p := &Platform{
		httpClient:  &http.Client{Transport: failRoundTripper{t: t}},
		accessToken: "test_token",
		tokenExpiry: time.Now().Add(time.Hour),
	}

	p.AddDoneReaction(validEmotionReplyContext())
}

func TestReactionInvalidReplyContextDoesNotCallHTTP(t *testing.T) {
	p := &Platform{
		httpClient:    &http.Client{Transport: failRoundTripper{t: t}},
		accessToken:   "test_token",
		tokenExpiry:   time.Now().Add(time.Hour),
		reactionEmoji: "🤔思考中",
		doneEmoji:     "✅完成",
	}

	stop := p.StartTyping(context.Background(), "not a reply context")
	stop()
	p.AddDoneReaction("not a reply context")
}

func TestReactionMissingMessageOrConversationDoesNotCallHTTP(t *testing.T) {
	p := &Platform{
		httpClient:    &http.Client{Transport: failRoundTripper{t: t}},
		accessToken:   "test_token",
		tokenExpiry:   time.Now().Add(time.Hour),
		reactionEmoji: "🤔思考中",
		doneEmoji:     "✅完成",
	}

	missingMessageID := validEmotionReplyContext()
	missingMessageID.messageId = ""
	stop := p.StartTyping(context.Background(), missingMessageID)
	stop()
	p.AddDoneReaction(missingMessageID)

	missingConversationID := validEmotionReplyContext()
	missingConversationID.conversationId = ""
	stop = p.StartTyping(context.Background(), missingConversationID)
	stop()
	p.AddDoneReaction(missingConversationID)
}

var _ core.TypingIndicator = (*Platform)(nil)
var _ core.TypingIndicatorDone = (*Platform)(nil)

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
		clientID:     "test_client",
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
