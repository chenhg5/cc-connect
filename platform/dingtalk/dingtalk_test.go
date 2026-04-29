package dingtalk

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
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
