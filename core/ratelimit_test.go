package core

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_AllowWithinLimit(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		if !rl.Allow("user1") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
}

func TestRateLimiter_BlockExceedingLimit(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		rl.Allow("user1")
	}
	if rl.Allow("user1") {
		t.Error("4th request should be blocked")
	}
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	rl.Allow("user1")
	rl.Allow("user1")

	if rl.Allow("user1") {
		t.Error("user1 should be blocked")
	}
	if !rl.Allow("user2") {
		t.Error("user2 should be allowed (independent bucket)")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewRateLimiter(2, 50*time.Millisecond)
	rl.Allow("user1")
	rl.Allow("user1")

	if rl.Allow("user1") {
		t.Error("should be blocked immediately")
	}

	time.Sleep(60 * time.Millisecond)

	if !rl.Allow("user1") {
		t.Error("should be allowed after window expires")
	}
}

func TestRateLimiter_Disabled(t *testing.T) {
	rl := NewRateLimiter(0, time.Minute)
	for i := 0; i < 100; i++ {
		if !rl.Allow("user1") {
			t.Error("should always allow when disabled")
		}
	}
}

func TestRateLimiter_Concurrent(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("user1")
		}()
	}
	wg.Wait()
}

func TestRateLimiter_Stop(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	rl.Allow("user1")

	// Stop should not panic and should be idempotent
	rl.Stop()
	rl.Stop() // second call should be safe

	// Allow should still work after Stop (just no background cleanup)
	if !rl.Allow("user2") {
		t.Error("Allow should still work after Stop")
	}
}

func TestRateLimiter_StopDisabled(t *testing.T) {
	// A disabled limiter (maxMessages=0) should also handle Stop gracefully
	rl := NewRateLimiter(0, time.Minute)
	rl.Stop()
}
