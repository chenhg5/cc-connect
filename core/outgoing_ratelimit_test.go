package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOutgoingRateLimiter_Disabled(t *testing.T) {
	orl := NewOutgoingRateLimiter(OutgoingRateLimitCfg{MaxPerSecond: 0}, nil)
	// Should return immediately when disabled.
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := orl.Wait(context.Background(), "test"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("disabled limiter should be instant, took %v", elapsed)
	}
}

func TestOutgoingRateLimiter_BurstThenThrottle(t *testing.T) {
	// 2 msgs/sec with burst=2: first 2 should be instant, 3rd should wait ~500ms.
	orl := NewOutgoingRateLimiter(OutgoingRateLimitCfg{MaxPerSecond: 2, Burst: 2}, nil)

	ctx := context.Background()
	start := time.Now()
	// Consume the burst
	for i := 0; i < 2; i++ {
		if err := orl.Wait(ctx, "p"); err != nil {
			t.Fatalf("burst msg %d: %v", i, err)
		}
	}
	burstElapsed := time.Since(start)
	if burstElapsed > 50*time.Millisecond {
		t.Fatalf("burst should be instant, took %v", burstElapsed)
	}

	// Third message should throttle
	before := time.Now()
	if err := orl.Wait(ctx, "p"); err != nil {
		t.Fatalf("throttled msg: %v", err)
	}
	waited := time.Since(before)
	if waited < 300*time.Millisecond || waited > 800*time.Millisecond {
		t.Fatalf("expected ~500ms wait, got %v", waited)
	}
}

func TestOutgoingRateLimiter_PerPlatformOverride(t *testing.T) {
	defaults := OutgoingRateLimitCfg{MaxPerSecond: 100} // fast default
	overrides := map[string]OutgoingRateLimitCfg{
		"slow": {MaxPerSecond: 2, Burst: 1},
	}
	orl := NewOutgoingRateLimiter(defaults, overrides)
	ctx := context.Background()

	// "fast" platform should be instant (burst=100)
	start := time.Now()
	for i := 0; i < 10; i++ {
		_ = orl.Wait(ctx, "fast")
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("fast platform should not throttle within burst")
	}

	// "slow" platform: burst=1, second call should wait ~500ms
	_ = orl.Wait(ctx, "slow")
	before := time.Now()
	_ = orl.Wait(ctx, "slow")
	waited := time.Since(before)
	if waited < 300*time.Millisecond {
		t.Fatalf("slow platform should throttle, waited only %v", waited)
	}
}

func TestOutgoingRateLimiter_ContextCancellation(t *testing.T) {
	orl := NewOutgoingRateLimiter(OutgoingRateLimitCfg{MaxPerSecond: 1, Burst: 1}, nil)
	ctx, cancel := context.WithCancel(context.Background())

	// Consume the single burst token
	_ = orl.Wait(ctx, "p")

	// Cancel context immediately
	cancel()
	err := orl.Wait(ctx, "p")
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestOutgoingRateLimiter_ConcurrentAccess(t *testing.T) {
	orl := NewOutgoingRateLimiter(OutgoingRateLimitCfg{MaxPerSecond: 1000, Burst: 100}, nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = orl.Wait(ctx, "p")
			}
		}()
	}
	wg.Wait()
}

func TestOutgoingRateLimiter_DefaultBurst(t *testing.T) {
	// When Burst is 0, effectiveBurst = ceil(MaxPerSecond)
	cfg := OutgoingRateLimitCfg{MaxPerSecond: 1.5, Burst: 0}
	if cfg.effectiveBurst() != 2 {
		t.Fatalf("expected burst=2 for rate 1.5, got %d", cfg.effectiveBurst())
	}

	cfg2 := OutgoingRateLimitCfg{MaxPerSecond: 5, Burst: 0}
	if cfg2.effectiveBurst() != 5 {
		t.Fatalf("expected burst=5 for rate 5, got %d", cfg2.effectiveBurst())
	}
}

func TestOutgoingRateLimiter_DisabledPlatformOverride(t *testing.T) {
	// Global rate is set but a specific platform is disabled via MaxPerSecond=0
	defaults := OutgoingRateLimitCfg{MaxPerSecond: 1, Burst: 1}
	overrides := map[string]OutgoingRateLimitCfg{
		"unlimited": {MaxPerSecond: 0},
	}
	orl := NewOutgoingRateLimiter(defaults, overrides)
	ctx := context.Background()

	// "unlimited" platform should be instant even after consuming tokens
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := orl.Wait(ctx, "unlimited"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("unlimited override platform should not throttle")
	}
}
