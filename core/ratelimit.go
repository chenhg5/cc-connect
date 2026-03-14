package core

import (
	"sync"
	"time"
)

// RateLimiter implements a per-key sliding-window rate limiter.
// It tracks message timestamps per key and rejects requests that exceed
// the configured limit within the time window.
type RateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*rateBucket
	maxMessages int
	windowMs    int64
	stopCh      chan struct{}
}

type rateBucket struct {
	timestamps []int64
	lastAccess int64
}

// NewRateLimiter creates a rate limiter allowing maxMessages per window duration.
// Pass maxMessages=0 to disable rate limiting.
func NewRateLimiter(maxMessages int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets:     make(map[string]*rateBucket),
		maxMessages: maxMessages,
		windowMs:    window.Milliseconds(),
		stopCh:      make(chan struct{}),
	}
	if maxMessages > 0 {
		go rl.cleanupLoop()
	}
	return rl
}

// Stop terminates the background cleanup goroutine. It is safe to call
// multiple times and on a disabled (maxMessages=0) limiter.
func (rl *RateLimiter) Stop() {
	select {
	case <-rl.stopCh:
		// already stopped
	default:
		close(rl.stopCh)
	}
}

// Allow checks whether a message from the given key is within the rate limit.
// Returns true if allowed (and records the timestamp), false if rate-limited.
func (rl *RateLimiter) Allow(key string) bool {
	if rl.maxMessages <= 0 {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().UnixMilli()
	b := rl.buckets[key]
	if b == nil {
		b = &rateBucket{}
		rl.buckets[key] = b
	}
	b.lastAccess = now

	cutoff := now - rl.windowMs
	filtered := b.timestamps[:0]
	for _, ts := range b.timestamps {
		if ts > cutoff {
			filtered = append(filtered, ts)
		}
	}
	b.timestamps = filtered

	if len(b.timestamps) >= rl.maxMessages {
		return false
	}
	b.timestamps = append(b.timestamps, now)
	return true
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now().UnixMilli()
			staleThreshold := rl.windowMs * 2
			for k, b := range rl.buckets {
				if now-b.lastAccess > staleThreshold {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}
