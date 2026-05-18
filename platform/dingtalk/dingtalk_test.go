package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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
// mediaSendTarget: image/file/audio sends must route on rc.isGroup the
// same way sendProactiveMessage does. Without this, an image generated
// in a group session is delivered to a 1:1 DM with the original sender
// instead of being posted back to the group.
// ──────────────────────────────────────────────────────────────

func TestMediaSendTarget_GroupRoutesToGroupAPI(t *testing.T) {
	p := &Platform{robotCode: "robot-x"}
	rc := replyContext{
		isGroup:        true,
		conversationId: "cidGroupA",
		senderStaffId:  "staff_42",
	}
	url, body, err := p.mediaSendTarget(rc, "sampleImageMsg", `{"photoURL":"@media-id"}`)
	if err != nil {
		t.Fatalf("mediaSendTarget: %v", err)
	}
	if url != "https://api.dingtalk.com/v1.0/robot/groupMessages/send" {
		t.Errorf("group URL = %q, want groupMessages/send", url)
	}
	if got, ok := body["openConversationId"].(string); !ok || got != "cidGroupA" {
		t.Errorf("openConversationId = %v, want \"cidGroupA\"", body["openConversationId"])
	}
	if _, hasUserIds := body["userIds"]; hasUserIds {
		t.Errorf("group body must not include userIds: %v", body)
	}
}

func TestMediaSendTarget_DirectRoutesToOToAPI(t *testing.T) {
	p := &Platform{robotCode: "robot-x"}
	rc := replyContext{
		isGroup:       false,
		senderStaffId: "staff_42",
	}
	url, body, err := p.mediaSendTarget(rc, "sampleImageMsg", `{"photoURL":"@media-id"}`)
	if err != nil {
		t.Fatalf("mediaSendTarget: %v", err)
	}
	if url != "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend" {
		t.Errorf("direct URL = %q, want oToMessages/batchSend", url)
	}
	ids, ok := body["userIds"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "staff_42" {
		t.Errorf("userIds = %v, want [\"staff_42\"]", body["userIds"])
	}
	if _, hasOpen := body["openConversationId"]; hasOpen {
		t.Errorf("direct body must not include openConversationId: %v", body)
	}
}

func TestMediaSendTarget_GroupWithEmptyConversationIdFallsBack(t *testing.T) {
	p := &Platform{robotCode: "robot-x"}
	rc := replyContext{
		isGroup:        true,
		conversationId: "",
		senderStaffId:  "staff_99",
	}
	url, body, err := p.mediaSendTarget(rc, "sampleFile", `{"mediaId":"m"}`)
	if err != nil {
		t.Fatalf("mediaSendTarget: %v", err)
	}
	if url != "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend" {
		t.Errorf("fallback URL = %q, want oToMessages/batchSend when conversationId empty", url)
	}
	if _, ok := body["userIds"]; !ok {
		t.Errorf("fallback must use userIds, got body=%v", body)
	}
}

func TestMediaSendTarget_NoTargetReturnsError(t *testing.T) {
	p := &Platform{robotCode: "robot-x"}
	rc := replyContext{} // no isGroup, no senderStaffId
	_, _, err := p.mediaSendTarget(rc, "sampleImageMsg", `{}`)
	if err == nil {
		t.Fatal("expected error when neither group nor direct target is available")
	}
}

// stubDingTalkRT intercepts every outbound HTTP call made by the dingtalk
// Platform so a send test can run without real network. It records the URL
// path of each request, returns stub success bodies for the token / upload
// / send endpoints, and 404s anything else.
type stubDingTalkRT struct {
	mu    sync.Mutex
	paths []string
}

func (rt *stubDingTalkRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.paths = append(rt.paths, req.URL.Path)
	rt.mu.Unlock()
	mk := func(body string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}
	}
	switch {
	case strings.Contains(req.URL.Path, "/media/upload"):
		return mk(`{"errcode":0,"media_id":"@fake-media","type":"image"}`), nil
	case strings.Contains(req.URL.Path, "/oToMessages/batchSend"),
		strings.Contains(req.URL.Path, "/groupMessages/send"):
		return mk(`{"processQueryKey":"abc"}`), nil
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
	}
}

func (rt *stubDingTalkRT) hit(suffix string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, p := range rt.paths {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

func newSendTestPlatform() (*Platform, *stubDingTalkRT) {
	rt := &stubDingTalkRT{}
	p := &Platform{
		clientID:     "test",
		clientSecret: "test",
		robotCode:    "robot-x",
		httpClient:   &http.Client{Transport: rt, Timeout: 10 * time.Second},
		accessToken:  "cached-fake-token",
		tokenExpiry:  time.Now().Add(time.Hour),
	}
	return p, rt
}

func TestSendImage_GroupSessionPostsToGroupAPI(t *testing.T) {
	p, rt := newSendTestPlatform()
	rc := replyContext{isGroup: true, conversationId: "cidGroupA", senderStaffId: "staff_42"}
	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: []byte("png"), FileName: "x.png"}); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if !rt.hit("/groupMessages/send") {
		t.Errorf("group SendImage did not hit /groupMessages/send; got paths=%v", rt.paths)
	}
	if rt.hit("/oToMessages/batchSend") {
		t.Errorf("group SendImage also hit 1:1 oToMessages/batchSend (would deliver as private DM); paths=%v", rt.paths)
	}
}

func TestSendImage_DirectSessionPostsTo1on1API(t *testing.T) {
	p, rt := newSendTestPlatform()
	rc := replyContext{senderStaffId: "staff_99"}
	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: []byte("png"), FileName: "y.png"}); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if !rt.hit("/oToMessages/batchSend") {
		t.Errorf("direct SendImage did not hit /oToMessages/batchSend; got paths=%v", rt.paths)
	}
}

func TestSendFile_GroupSessionPostsToGroupAPI(t *testing.T) {
	p, rt := newSendTestPlatform()
	rc := replyContext{isGroup: true, conversationId: "cidGroupB", senderStaffId: "staff_42"}
	if err := p.SendFile(context.Background(), rc, core.FileAttachment{Data: []byte("doc"), FileName: "x.txt"}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if !rt.hit("/groupMessages/send") {
		t.Errorf("group SendFile did not hit /groupMessages/send; got paths=%v", rt.paths)
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
