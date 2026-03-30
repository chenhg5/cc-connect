package core

import (
	"context"
	"math"
	"sync"
	"time"
)

// OutgoingRateLimitCfg holds the resolved rate-limit parameters for outgoing messages.
type OutgoingRateLimitCfg struct {
	MaxPerSecond float64 // messages per second; 0 = disabled / unlimited
	Burst        int     // max burst size; 0 = use ceil(MaxPerSecond)
}

func (c OutgoingRateLimitCfg) disabled() bool {
	return c.MaxPerSecond <= 0
}

func (c OutgoingRateLimitCfg) effectiveBurst() int {
	if c.Burst > 0 {
		return c.Burst
	}
	return int(math.Ceil(c.MaxPerSecond))
}

// OutgoingRateLimiter throttles outgoing messages sent to platforms using a
// per-platform token bucket. It never drops messages; callers block until
// the rate budget allows the send.
type OutgoingRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket            // key = platform name
	defaults  OutgoingRateLimitCfg
	overrides map[string]OutgoingRateLimitCfg     // per-platform overrides
}

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64   // tokens per second
	lastRefill time.Time
}

// NewOutgoingRateLimiter creates a rate limiter with global defaults and
// optional per-platform overrides. If defaults.MaxPerSecond <= 0 and no
// overrides are set, the limiter is effectively disabled.
func NewOutgoingRateLimiter(defaults OutgoingRateLimitCfg, overrides map[string]OutgoingRateLimitCfg) *OutgoingRateLimiter {
	if overrides == nil {
		overrides = make(map[string]OutgoingRateLimitCfg)
	}
	return &OutgoingRateLimiter{
		buckets:   make(map[string]*tokenBucket),
		defaults:  defaults,
		overrides: overrides,
	}
}

// cfgFor returns the effective config for a platform.
func (orl *OutgoingRateLimiter) cfgFor(platform string) OutgoingRateLimitCfg {
	if c, ok := orl.overrides[platform]; ok {
		return c
	}
	return orl.defaults
}

// bucketFor returns (or lazily creates) the token bucket for a platform.
// Must be called with orl.mu held.
func (orl *OutgoingRateLimiter) bucketFor(platform string) *tokenBucket {
	if b, ok := orl.buckets[platform]; ok {
		return b
	}
	cfg := orl.cfgFor(platform)
	burst := cfg.effectiveBurst()
	b := &tokenBucket{
		tokens:     float64(burst), // start full
		maxTokens:  float64(burst),
		refillRate: cfg.MaxPerSecond,
		lastRefill: time.Now(),
	}
	orl.buckets[platform] = b
	return b
}

// refill adds tokens based on elapsed time since last refill.
func (b *tokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now
}

// Wait blocks until the rate limiter allows one message to be sent to the
// given platform. Returns nil on success, or the context error if ctx is
// cancelled while waiting.
func (orl *OutgoingRateLimiter) Wait(ctx context.Context, platform string) error {
	cfg := orl.cfgFor(platform)
	if cfg.disabled() {
		return nil
	}

	for {
		orl.mu.Lock()
		b := orl.bucketFor(platform)
		b.refill()

		if b.tokens >= 1 {
			b.tokens--
			orl.mu.Unlock()
			return nil
		}

		// Calculate wait time until 1 token is available.
		deficit := 1.0 - b.tokens
		waitSecs := deficit / b.refillRate
		orl.mu.Unlock()

		timer := time.NewTimer(time.Duration(waitSecs * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// Loop back to try consuming a token.
		}
	}
}
