package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNewConfiguresReactionEmoji(t *testing.T) {
	plat, err := New(map[string]any{
		"client_id":     "cid",
		"client_secret": "secret",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if p.reactionEmoji != "🤔Thinking" {
		t.Fatalf("default reactionEmoji = %q, want %q", p.reactionEmoji, "🤔Thinking")
	}
	if p.doneEmoji != "" {
		t.Fatalf("default doneEmoji = %q, want empty", p.doneEmoji)
	}

	plat, err = New(map[string]any{
		"client_id":      "cid",
		"client_secret":  "secret",
		"reaction_emoji": "none",
		"done_emoji":     "none",
	})
	if err != nil {
		t.Fatalf("New() with disabled emoji error = %v", err)
	}
	p = plat.(*Platform)
	if p.reactionEmoji != "" {
		t.Fatalf("disabled reactionEmoji = %q, want empty", p.reactionEmoji)
	}
	if p.doneEmoji != "" {
		t.Fatalf("disabled doneEmoji = %q, want empty", p.doneEmoji)
	}

	plat, err = New(map[string]any{
		"client_id":      "cid",
		"client_secret":  "secret",
		"reaction_emoji": "🧠Working",
		"done_emoji":     "🥳Done",
	})
	if err != nil {
		t.Fatalf("New() with custom emoji error = %v", err)
	}
	p = plat.(*Platform)
	if p.reactionEmoji != "🧠Working" || p.doneEmoji != "🥳Done" {
		t.Fatalf("emoji config = (%q, %q), want (🧠Working, 🥳Done)", p.reactionEmoji, p.doneEmoji)
	}
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

func TestFormatReplyContent_TextQuotePreservesWhitespace(t *testing.T) {
	p := &Platform{}
	repliedContent, _ := json.Marshal(repliedTextContent{Text: "  original message  "})
	richText := &richTextContent{
		Content:    "user reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "text",
			Content: repliedContent,
		},
	}
	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"  original message  \"\n\nuser reply"
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

func TestFormatReplyContent_WithQuotedInteractiveCardContent(t *testing.T) {
	p := &Platform{cardTemplateKey: "content"}
	richText := &richTextContent{
		Content:    "user reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"cardData": {
					"cardParamMap": {
						"config": "{\"autoLayout\":true}",
						"content": "bot card answer"
					}
				}
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"bot card answer\"\n\nuser reply"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_WithQuotedInteractiveCardCustomTemplateKey(t *testing.T) {
	p := &Platform{cardTemplateKey: "body"}
	richText := &richTextContent{
		Content:    "next question",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"cardData": {
					"cardParamMap": {
						"content": "default content",
						"body": "custom body content"
					}
				}
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"custom body content\"\n\nnext question"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_WithQuotedInteractiveCardNestedJSONEnvelope(t *testing.T) {
	p := &Platform{cardTemplateKey: "content"}
	richText := &richTextContent{
		Content:    "continue",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"cardData": "{\"cardParamMap\":{\"content\":\"nested card answer\"}}"
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"nested card answer\"\n\ncontinue"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_WithQuotedInteractiveCardTopLevelFallback(t *testing.T) {
	p := &Platform{}
	richText := &richTextContent{
		Content:    "what next?",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"title": "Run Summary",
				"markdown": "all checks passed"
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"all checks passed\"\n\nwhat next?"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_InteractiveCardPreservesVisibleJSONContent(t *testing.T) {
	p := &Platform{cardTemplateKey: "content"}
	richText := &richTextContent{
		Content:    "follow up",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"cardData": {
					"cardParamMap": {
						"config": "{\"autoLayout\":true}",
						"content": "{\"status\":\"ok\"}"
					}
				}
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"{\"status\":\"ok\"}\"\n\nfollow up"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_InteractiveCardTopLevelFallbackIgnoresCustomKey(t *testing.T) {
	p := &Platform{cardTemplateKey: "body"}
	richText := &richTextContent{
		Content:    "follow up",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: json.RawMessage(`{
				"body": "custom top-level body",
				"content": "top-level content"
			}`),
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expected := "引用: \"top-level content\"\n\nfollow up"
	if result != expected {
		t.Errorf("formatReplyContent() = %q, want %q", result, expected)
	}
}

func TestFormatReplyContent_TruncatesLongQuotedInteractiveCardContent(t *testing.T) {
	p := &Platform{cardTemplateKey: "content"}
	longText := strings.Repeat("x", maxQuotedMessageRunes+1)
	cardContent, err := json.Marshal(map[string]any{
		"cardData": map[string]any{
			"cardParamMap": map[string]string{
				"content": longText,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal card content: %v", err)
	}
	richText := &richTextContent{
		Content:    "short reply",
		IsReplyMsg: true,
		RepliedMsg: &repliedMessage{
			MsgType: "interactiveCard",
			Content: cardContent,
		},
	}

	result := p.formatReplyContent(richText, "fallback")
	expectedPrefix := "引用: \"" + strings.Repeat("x", maxQuotedMessageRunes) + "...\"\n\nshort reply"
	if result != expectedPrefix {
		t.Errorf("formatReplyContent() length = %d, want truncated output length %d", len([]rune(result)), len([]rune(expectedPrefix)))
	}
}

func TestOnRawMessage_QuotedInteractiveCardEnrichesMessageContent(t *testing.T) {
	var got *core.Message
	p := &Platform{
		cardTemplateKey: "content",
		handler: func(_ core.Platform, msg *core.Message) {
			got = msg
		},
	}

	p.onRawMessage(`{
		"msgtype": "text",
		"msgId": "msg-1",
		"conversationType": "2",
		"conversationId": "conv-1",
		"conversationTitle": "team chat",
		"senderStaffId": "user-1",
		"senderNick": "Alice",
		"sessionWebhook": "https://example.invalid/webhook",
		"text": {
			"content": "please continue",
			"isReplyMsg": true,
			"repliedMsg": {
				"msgType": "interactiveCard",
				"content": {
					"cardData": {
						"cardParamMap": {
							"content": "previous card answer"
						}
					}
				}
			}
		}
	}`)

	if got == nil {
		t.Fatal("handler was not called")
	}
	expected := "引用: \"previous card answer\"\n\nplease continue"
	if got.Content != expected {
		t.Errorf("message content = %q, want %q", got.Content, expected)
	}
}

func TestOnRawMessage_IncludesReactionReplyContext(t *testing.T) {
	var got *core.Message
	p := &Platform{
		handler: func(_ core.Platform, msg *core.Message) {
			got = msg
		},
	}

	p.onRawMessage(`{
		"msgtype": "text",
		"msgId": "msg-reaction-1",
		"conversationType": "2",
		"conversationId": "conv-1",
		"conversationTitle": "team chat",
		"senderStaffId": "user-1",
		"senderNick": "Alice",
		"sessionWebhook": "https://example.invalid/webhook",
		"text": {
			"content": "hi"
		}
	}`)

	if got == nil {
		t.Fatal("handler was not called")
	}
	if got.MessageID != "msg-reaction-1" {
		t.Fatalf("MessageID = %q, want %q", got.MessageID, "msg-reaction-1")
	}
	rc, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", got.ReplyCtx)
	}
	if rc.messageID != "msg-reaction-1" {
		t.Errorf("replyContext.messageID = %q, want %q", rc.messageID, "msg-reaction-1")
	}
	if rc.conversationId != "conv-1" {
		t.Errorf("replyContext.conversationId = %q, want %q", rc.conversationId, "conv-1")
	}
	if rc.senderStaffId != "user-1" {
		t.Errorf("replyContext.senderStaffId = %q, want %q", rc.senderStaffId, "user-1")
	}
	if !rc.isGroup {
		t.Error("replyContext.isGroup = false, want true")
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

type dingtalkEmotionCall struct {
	path   string
	header string
	body   map[string]any
}

type fakeDingTalkEmotionRT struct {
	calls []dingtalkEmotionCall
}

func (f *fakeDingTalkEmotionRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1.0/oauth2/accessToken" {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"accessToken":"tok-emotion","expireIn":7200}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}

	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, dingtalkEmotionCall{
		path:   req.URL.Path,
		header: req.Header.Get("x-acs-dingtalk-access-token"),
		body:   body,
	})

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestSendEmotionReply(t *testing.T) {
	rt := &fakeDingTalkEmotionRT{}
	p := &Platform{
		clientID:     "cid",
		clientSecret: "secret",
		robotCode:    "robot-code",
		httpClient:   &http.Client{Transport: rt},
	}

	err := p.sendEmotion(context.Background(), replyContext{
		messageID:      "msg-1",
		conversationId: "conv-1",
	}, "🤔Thinking", false)
	if err != nil {
		t.Fatalf("sendEmotion() error = %v", err)
	}
	if len(rt.calls) != 1 {
		t.Fatalf("HTTP emotion calls = %d, want 1", len(rt.calls))
	}
	call := rt.calls[0]
	if call.path != "/v1.0/robot/emotion/reply" {
		t.Fatalf("path = %q, want /v1.0/robot/emotion/reply", call.path)
	}
	if call.header != "tok-emotion" {
		t.Fatalf("access token header = %q, want tok-emotion", call.header)
	}
	if call.body["robotCode"] != "robot-code" {
		t.Errorf("robotCode = %v, want robot-code", call.body["robotCode"])
	}
	if call.body["openMsgId"] != "msg-1" {
		t.Errorf("openMsgId = %v, want msg-1", call.body["openMsgId"])
	}
	if call.body["openConversationId"] != "conv-1" {
		t.Errorf("openConversationId = %v, want conv-1", call.body["openConversationId"])
	}
	if call.body["emotionType"] != float64(2) {
		t.Errorf("emotionType = %v, want 2", call.body["emotionType"])
	}
	if call.body["emotionName"] != "🤔Thinking" {
		t.Errorf("emotionName = %v, want 🤔Thinking", call.body["emotionName"])
	}
	textEmotion, ok := call.body["textEmotion"].(map[string]any)
	if !ok {
		t.Fatalf("textEmotion type = %T, want object", call.body["textEmotion"])
	}
	if textEmotion["emotionId"] != "2659900" {
		t.Errorf("textEmotion.emotionId = %v, want 2659900", textEmotion["emotionId"])
	}
	if textEmotion["emotionName"] != "🤔Thinking" || textEmotion["text"] != "🤔Thinking" {
		t.Errorf("textEmotion = %#v, want matching custom text emoji", textEmotion)
	}
	if textEmotion["backgroundId"] != "im_bg_1" {
		t.Errorf("textEmotion.backgroundId = %v, want im_bg_1", textEmotion["backgroundId"])
	}
}

func TestSendEmotionRecall(t *testing.T) {
	rt := &fakeDingTalkEmotionRT{}
	p := &Platform{
		clientID:     "cid",
		clientSecret: "secret",
		robotCode:    "robot-code",
		httpClient:   &http.Client{Transport: rt},
	}

	err := p.sendEmotion(context.Background(), replyContext{
		messageID:      "msg-1",
		conversationId: "conv-1",
	}, "🤔Thinking", true)
	if err != nil {
		t.Fatalf("sendEmotion(recall) error = %v", err)
	}
	if len(rt.calls) != 1 {
		t.Fatalf("HTTP emotion calls = %d, want 1", len(rt.calls))
	}
	if rt.calls[0].path != "/v1.0/robot/emotion/recall" {
		t.Fatalf("path = %q, want /v1.0/robot/emotion/recall", rt.calls[0].path)
	}
}

func TestStartTypingAndDoneReaction(t *testing.T) {
	rt := &fakeDingTalkEmotionRT{}
	p := &Platform{
		clientID:      "cid",
		clientSecret:  "secret",
		robotCode:     "robot-code",
		reactionEmoji: "🤔Thinking",
		doneEmoji:     "🥳Done",
		httpClient:    &http.Client{Transport: rt},
	}
	rc := replyContext{messageID: "msg-1", conversationId: "conv-1"}

	stop := p.StartTyping(context.Background(), rc)
	stop()
	p.AddDoneReaction(rc)

	if len(rt.calls) != 3 {
		t.Fatalf("HTTP emotion calls = %d, want 3", len(rt.calls))
	}
	if rt.calls[0].path != "/v1.0/robot/emotion/reply" || rt.calls[0].body["emotionName"] != "🤔Thinking" {
		t.Fatalf("typing add call = (%s, %v), want reply 🤔Thinking", rt.calls[0].path, rt.calls[0].body["emotionName"])
	}
	if rt.calls[1].path != "/v1.0/robot/emotion/recall" || rt.calls[1].body["emotionName"] != "🤔Thinking" {
		t.Fatalf("typing stop call = (%s, %v), want recall 🤔Thinking", rt.calls[1].path, rt.calls[1].body["emotionName"])
	}
	if rt.calls[2].path != "/v1.0/robot/emotion/reply" || rt.calls[2].body["emotionName"] != "🥳Done" {
		t.Fatalf("done call = (%s, %v), want reply 🥳Done", rt.calls[2].path, rt.calls[2].body["emotionName"])
	}
}

func TestOnRawMessage_PictureMsgTypeNotDroppedAsEmptyText(t *testing.T) {
	// Regression test for #1128: DingTalk sometimes sends msgtype="picture"
	// for image messages. Before the fix, this fell through to the text handler,
	// which produced an empty-content message that was silently dropped by the engine.
	// After the fix, the message is routed to handleImageMessage instead.
	//
	// We cannot easily mock the full HTTP download path here. The test verifies the
	// negative: for msgtype="picture" the handler must NOT be called with empty content.
	// handleImageMessage may panic on the nil httpClient; we recover from that.
	var handlerCalledWithEmptyContent bool

	func() {
		defer func() { recover() }() // handleImageMessage panics on nil httpClient — that's OK
		p := &Platform{
			handler: func(_ core.Platform, msg *core.Message) {
				if msg.Content == "" && len(msg.Images) == 0 {
					handlerCalledWithEmptyContent = true
				}
			},
		}
		p.onRawMessage(`{
			"msgtype": "picture",
			"msgId": "msg-pic-1",
			"conversationType": "1",
			"conversationId": "conv-1",
			"conversationTitle": "test",
			"senderStaffId": "user-1",
			"senderNick": "Alice",
			"sessionWebhook": "https://example.invalid/webhook",
			"content": {"downloadCode": "some-code"}
		}`)
	}()

	// The message is routed to handleImageMessage (which fails at download — no mock),
	// not to the text handler. The handler must NOT be invoked with empty content.
	if handlerCalledWithEmptyContent {
		t.Error("msgtype=picture: handler called with empty content (image was silently dropped as text)")
	}
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

func TestOnMessageRepliesToUnauthorizedSender(t *testing.T) {
	gotReply := make(chan string, 1)
	sessionWebhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Markdown struct {
				Text string `json:"text"`
			} `json:"markdown"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode reply payload: %v", err)
		}
		gotReply <- payload.Markdown.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer sessionWebhook.Close()

	p := &Platform{
		allowFrom: "allowed-user",
		handler: func(core.Platform, *core.Message) {
			t.Fatal("handler should not run for unauthorized sender")
		},
	}

	p.onMessage(&chatbot.BotCallbackDataModel{
		MsgId:            "msg_unauthorized",
		Msgtype:          "text",
		SenderStaffId:    "blocked-user",
		SenderNick:       "Blocked User",
		ConversationId:   "cid_direct",
		ConversationType: "1",
		SessionWebhook:   sessionWebhook.URL,
		Text:             chatbot.BotCallbackDataTextModel{Content: "hello"},
	}, nil)

	select {
	case got := <-gotReply:
		if got != core.UnauthorizedAccessMessage {
			t.Fatalf("reply = %q, want %q", got, core.UnauthorizedAccessMessage)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unauthorized reply")
	}
}

func TestExtractAtUserIds(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "empty string", content: "", want: nil},
		{name: "plain text no at", content: "hello world", want: nil},
		{name: "3-digit not matched", content: "@123", want: nil},
		{name: "4-digit match", content: "@1234", want: []string{"1234"}},
		{name: "9-digit match", content: "@194252073", want: []string{"194252073"}},
		{name: "18-digit match", content: "@194252073827812352", want: []string{"194252073827812352"}},
		{name: "alphabetic no-match", content: "@user @admin", want: nil},
		{name: "dedup", content: "@1234 @1234 @1234", want: []string{"1234"}},
		{name: "multiple distinct", content: "@1234 @5678", want: []string{"1234", "5678"}},
		{name: "embedded in text", content: "hello @1234 world", want: []string{"1234"}},
		{name: "mixed valid and invalid", content: "@123 @1234 @user @567890", want: []string{"1234", "567890"}},
		{name: "only at sign no digits", content: "@ @ @@", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAtUserIds(tt.content)
			if len(got) == 0 && len(tt.want) == 0 {
				return // both nil/empty
			}
			if len(got) != len(tt.want) {
				t.Fatalf("extractAtUserIds(%q) = %v, want %v", tt.content, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractAtUserIds(%q) = %v, want %v", tt.content, got, tt.want)
				}
			}
		})
	}
}

func TestReply_IncludesExtractedAtUserIds(t *testing.T) {
	gotPayload := make(chan map[string]any, 1)
	sessionWebhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode reply payload: %v", err)
		}
		gotPayload <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer sessionWebhook.Close()

	p := &Platform{}

	rc := replyContext{sessionWebhook: sessionWebhook.URL}
	err := p.Reply(context.Background(), rc, "hello @12345678 world")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case payload := <-gotPayload:
		at, ok := payload["at"].(map[string]any)
		if !ok {
			t.Fatal("expected 'at' field in payload")
		}
		atUserIds, ok := at["atUserIds"].([]any)
		if !ok {
			t.Fatal("expected 'at.atUserIds' field in payload")
		}
		if len(atUserIds) != 1 || atUserIds[0] != "12345678" {
			t.Fatalf("atUserIds = %v, want [\"12345678\"]", atUserIds)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reply")
	}
}

func TestReply_NoAtUserIdsWhenNoMention(t *testing.T) {
	gotPayload := make(chan map[string]any, 1)
	sessionWebhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode reply payload: %v", err)
		}
		gotPayload <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer sessionWebhook.Close()

	p := &Platform{}

	rc := replyContext{sessionWebhook: sessionWebhook.URL}
	err := p.Reply(context.Background(), rc, "hello world, no mentions here")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case payload := <-gotPayload:
		if _, ok := payload["at"]; ok {
			t.Fatal("expected no 'at' field when content has no @userId")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reply")
	}
}
