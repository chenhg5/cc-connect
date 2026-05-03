package line

import (
	"sync"
	"testing"
	"time"
)

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
