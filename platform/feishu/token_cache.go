package feishu

import (
	"context"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// invalidTokenCode is the error code returned by Feishu when tenant_access_token is invalid
const invalidTokenCode = 99991663

// tokenCache wraps the lark SDK's token cache with automatic invalidation
// when the server rejects a token (error 99991663).
//
// The lark SDK has a bug where it retries requests with an invalid token
// without clearing the cached token first. This wrapper solves that by:
// 1. Providing the standard Cache interface for SDK token storage
// 2. Tracking when tokens are rejected by the server
// 3. Returning empty (forcing refresh) when invalidated tokens are requested
//
// The wrapper monitors writes - when a response body contains error 99991663,
// it invalidates the cached token so subsequent Get() calls return empty,
// forcing the SDK to fetch a fresh token on retry.
type tokenCache struct {
	mu       sync.RWMutex
	tokens   map[string]*tokenEntry
	delegate larkcore.Cache // optional underlying cache
}

type tokenEntry struct {
	value       string
	expireTime  time.Time
	invalidated bool // true if server rejected this token
}

func newTokenCache() *tokenCache {
	return &tokenCache{
		tokens: make(map[string]*tokenEntry),
	}
}

// Set stores a token with the given TTL. Implements larkcore.Cache.
func (c *tokenCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[key] = &tokenEntry{
		value:       value,
		expireTime:  time.Now().Add(ttl),
		invalidated: false,
	}
	// Also set in delegate if present
	if c.delegate != nil {
		return c.delegate.Set(ctx, key, value, ttl)
	}
	return nil
}

// Get retrieves a token. Returns empty string if expired or invalidated.
// Implements larkcore.Cache.
func (c *tokenCache) Get(ctx context.Context, key string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.tokens[key]
	if !ok {
		// Not in our cache, try delegate
		if c.delegate != nil {
			return c.delegate.Get(ctx, key)
		}
		return "", nil
	}
	// If the token was invalidated by the server, force refresh
	if entry.invalidated {
		return "", nil
	}
	// If expired, return empty to trigger refresh
	if entry.expireTime.Before(time.Now()) {
		return "", nil
	}
	return entry.value, nil
}

// Invalidate marks a token as invalid, forcing refresh on next use.
// This should be called when the server returns error 99991663 (invalid token).
func (c *tokenCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.tokens[key]; ok {
		entry.invalidated = true
	}
	// Also clear from delegate if present
	if c.delegate != nil {
		// Note: larkcore.Cache doesn't have a Delete method, so we can't clear it
		// But our wrapper will return empty on Get() anyway
	}
}

// InvalidateAll invalidates all cached tokens for the given appID.
// This is used when we detect an invalid token error but don't know exact key.
func (c *tokenCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.tokens {
		entry.invalidated = true
	}
}

// Clear removes a token from the cache entirely.
func (c *tokenCache) Clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, key)
}

// tokenCacheKey generates a cache key matching the lark SDK's format.
func tokenCacheKey(appID string) string {
	return "tenant_access_token-" + appID + "-"
}