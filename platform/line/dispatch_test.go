package line

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

// fakeLineClient is a test double for lineClient.
type replyCall struct {
	token string
	msgs  []messaging_api.MessageInterface
}

type pushCall struct {
	to   string
	msgs []messaging_api.MessageInterface
}

type fakeLineClient struct {
	replyCalls []replyCall
	pushCalls  []pushCall
	replyErr   error
	pushErr    error
}

func (f *fakeLineClient) ReplyMessage(req *messaging_api.ReplyMessageRequest) (*messaging_api.ReplyMessageResponse, error) {
	f.replyCalls = append(f.replyCalls, replyCall{token: req.ReplyToken, msgs: req.Messages})
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	return &messaging_api.ReplyMessageResponse{}, nil
}

func (f *fakeLineClient) PushMessage(req *messaging_api.PushMessageRequest, retryKey string) (*messaging_api.PushMessageResponse, error) {
	f.pushCalls = append(f.pushCalls, pushCall{to: req.To, msgs: req.Messages})
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return &messaging_api.PushMessageResponse{}, nil
}

func (f *fakeLineClient) GetProfile(userId string) (*messaging_api.UserProfileResponse, error) {
	return &messaging_api.UserProfileResponse{}, nil
}

func (f *fakeLineClient) GetGroupSummary(groupId string) (*messaging_api.GroupSummaryResponse, error) {
	return &messaging_api.GroupSummaryResponse{}, nil
}

func (f *fakeLineClient) ShowLoadingAnimation(req *messaging_api.ShowLoadingAnimationRequest) (*map[string]interface{}, error) {
	return nil, nil
}

// TestCacheReplyToken_StoresAndReads verifies that cacheReplyToken stores a token
// and loadAndDeleteFreshToken can retrieve it.
func TestCacheReplyToken_StoresAndReads(t *testing.T) {
	p := &Platform{
		replyTokens: sync.Map{},
	}

	targetID := "U12345"
	token := "nHuyWiB7yP5Zw52FIkcQT"

	p.cacheReplyToken(targetID, token)

	retrieved, ok := p.loadAndDeleteFreshToken(targetID)
	if !ok {
		t.Fatal("expected loadAndDeleteFreshToken to return ok=true, got false")
	}
	if retrieved != token {
		t.Fatalf("expected token %q, got %q", token, retrieved)
	}
}

// TestLoadAndDeleteFreshToken_SingleUse verifies that loadAndDeleteFreshToken
// deletes the token after retrieval (single-use semantics).
func TestLoadAndDeleteFreshToken_SingleUse(t *testing.T) {
	p := &Platform{
		replyTokens: sync.Map{},
	}

	targetID := "U12345"
	token := "nHuyWiB7yP5Zw52FIkcQT"

	p.cacheReplyToken(targetID, token)

	// First call should succeed
	retrieved1, ok1 := p.loadAndDeleteFreshToken(targetID)
	if !ok1 {
		t.Fatal("first loadAndDeleteFreshToken should return ok=true")
	}
	if retrieved1 != token {
		t.Fatalf("expected token %q, got %q", token, retrieved1)
	}

	// Second call should fail (token was deleted)
	retrieved2, ok2 := p.loadAndDeleteFreshToken(targetID)
	if ok2 {
		t.Fatal("second loadAndDeleteFreshToken should return ok=false (token deleted)")
	}
	if retrieved2 != "" {
		t.Fatalf("expected empty string on miss, got %q", retrieved2)
	}
}

// TestLoadAndDeleteFreshToken_Expired verifies that loadAndDeleteFreshToken
// rejects expired tokens (older than replyTokenTTL).
func TestLoadAndDeleteFreshToken_Expired(t *testing.T) {
	p := &Platform{
		replyTokens: sync.Map{},
	}

	targetID := "U12345"
	token := "nHuyWiB7yP5Zw52FIkcQT"

	// Manually store an expired entry (timestamp is ~100ms ago, TTL is 50s, so it's fresh)
	// To test expiry, store an entry with a very old timestamp.
	p.replyTokens.Store(targetID, tokenEntry{
		token: token,
		at:    time.Now().Add(-replyTokenTTL - 1*time.Second),
	})

	retrieved, ok := p.loadAndDeleteFreshToken(targetID)
	if ok {
		t.Fatal("expected loadAndDeleteFreshToken to return ok=false for expired token, got true")
	}
	if retrieved != "" {
		t.Fatalf("expected empty string for expired token, got %q", retrieved)
	}
}

// TestCacheReplyToken_Overwrite verifies that caching a token for the same targetID
// overwrites the previous token (latest wins).
func TestCacheReplyToken_Overwrite(t *testing.T) {
	p := &Platform{
		replyTokens: sync.Map{},
	}

	targetID := "U12345"
	token1 := "nHuyWiB7yP5Zw52FIkcQT"
	token2 := "anotherTokenValue123456"

	// Cache first token
	p.cacheReplyToken(targetID, token1)

	// Overwrite with second token
	p.cacheReplyToken(targetID, token2)

	// Should retrieve the second token
	retrieved, ok := p.loadAndDeleteFreshToken(targetID)
	if !ok {
		t.Fatal("expected loadAndDeleteFreshToken to return ok=true, got false")
	}
	if retrieved != token2 {
		t.Fatalf("expected token %q (overwrite), got %q", token2, retrieved)
	}
}

// TestCacheReplyToken_EmptyInputsIgnored verifies that cacheReplyToken silently
// ignores empty targetID or token (does not store anything).
func TestCacheReplyToken_EmptyInputsIgnored(t *testing.T) {
	p := &Platform{
		replyTokens: sync.Map{},
	}

	// Try to cache with empty targetID
	p.cacheReplyToken("", "someToken")

	// Try to cache with empty token
	p.cacheReplyToken("U12345", "")

	// Both should result in no entries
	_, ok1 := p.loadAndDeleteFreshToken("")
	if ok1 {
		t.Fatal("empty targetID should not have been cached")
	}

	_, ok2 := p.loadAndDeleteFreshToken("U12345")
	if ok2 {
		t.Fatal("empty token should not have been cached")
	}
}

func TestDispatchReply_FreshToken_UsesReply(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-fresh")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyMessage call, got %d", len(fake.replyCalls))
	}
	if fake.replyCalls[0].token != "tok-fresh" {
		t.Errorf("token = %q, want %q", fake.replyCalls[0].token, "tok-fresh")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("expected 0 PushMessage calls, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_NoToken_UsesPush(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}

	rc := replyContext{targetID: "U999", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 0 {
		t.Errorf("expected 0 ReplyMessage calls, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 1 {
		t.Fatalf("expected 1 PushMessage call, got %d", len(fake.pushCalls))
	}
	if fake.pushCalls[0].to != "U999" {
		t.Errorf("to = %q, want %q", fake.pushCalls[0].to, "U999")
	}
}

func TestDispatchReply_TokenInvalid_FallsBackToPush(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 400, {\"message\":\"Invalid reply token\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-bad")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Errorf("expected 1 ReplyMessage attempt, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("expected fallback to PushMessage (1 call), got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_TokenExpiredString_FallsBackToPush(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 400, {\"message\":\"The reply token has expired\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-old")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("expected fallback Push, got %d push calls", len(fake.pushCalls))
	}
}

func TestDispatchReply_OtherError_NoFallback(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("network unreachable"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-x")

	rc := replyContext{targetID: "U123", targetType: "user"}
	err := p.dispatchReply(rc, []string{"hello"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("expected NO fallback Push (duplicate risk), got %d push calls", len(fake.pushCalls))
	}
}

func TestDispatchReply_500Error_NoFallback(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 500, {\"message\":\"internal server error\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-x")

	rc := replyContext{targetID: "U123", targetType: "user"}
	err := p.dispatchReply(rc, []string{"hello"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("500 should NOT fallback (ambiguous outcome), got %d push calls", len(fake.pushCalls))
	}
}

func TestDispatchReply_BatchUnder5(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Fatalf("want 1 ReplyMessage call, got %d", len(fake.replyCalls))
	}
	if got := len(fake.replyCalls[0].msgs); got != 3 {
		t.Errorf("want 3 messages in single Reply, got %d", got)
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("want 0 Push calls, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_BatchExactly5(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c", "d", "e"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 || len(fake.replyCalls[0].msgs) != 5 {
		t.Errorf("want 1 Reply with 5 msgs, got replies=%d msgs=%d", len(fake.replyCalls), len(fake.replyCalls[0].msgs))
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("want 0 Push calls at exactly 5, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_BatchOver5_OverflowToPush(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c", "d", "e", "f", "g"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 || len(fake.replyCalls[0].msgs) != 5 {
		t.Errorf("want 1 Reply with first 5 msgs, got replies=%d msgs=%d", len(fake.replyCalls), len(fake.replyCalls[0].msgs))
	}
	if len(fake.pushCalls) != 2 {
		t.Errorf("want 2 Push calls for overflow, got %d", len(fake.pushCalls))
	}
}
