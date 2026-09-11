package wecom

import (
	"context"
	"sync"
	"testing"
	"time"
)

// withNow overrides the package-level nowFunc for the duration of a test and
// restores it afterwards. Returns a setter to advance the clock.
func withNow(t *testing.T) func(time.Time) {
	t.Helper()
	prev := nowFunc
	mu := sync.Mutex{}
	current := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	set := func(t2 time.Time) {
		mu.Lock()
		current = t2
		mu.Unlock()
	}
	t.Cleanup(func() { nowFunc = prev })
	return set
}

func TestSendQuota_Disabled(t *testing.T) {
	withNow(t)
	q := sendQuota{limit: 0, window: time.Second} // disabled
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Should return immediately no matter how many times called.
	for i := 0; i < 100; i++ {
		if err := q.wait(ctx); err != nil {
			t.Fatalf("call %d: wait = %v, want nil", i, err)
		}
	}
}

func TestSendQuota_AllowsUnderLimit(t *testing.T) {
	withNow(t)
	q := sendQuota{limit: 3, window: time.Minute}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := q.wait(ctx); err != nil {
			t.Fatalf("call %d: wait = %v, want nil", i, err)
		}
	}
}

func TestSendQuota_BlocksThenReleasesAfterWindow(t *testing.T) {
	set := withNow(t)
	q := sendQuota{limit: 1, window: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// First send: immediate.
	if err := q.wait(ctx); err != nil {
		t.Fatalf("first wait = %v, want nil", err)
	}

	// Second send: should block. Run it in a goroutine and confirm it does not
	// return before the window slides. We advance the injected clock to release it.
	done := make(chan error, 1)
	go func() {
		done <- q.wait(ctx)
	}()

	select {
	case err := <-done:
		t.Fatalf("second wait returned before window slid: %v", err)
	case <-time.After(20 * time.Millisecond):
		// still blocked, as expected.
	}

	// Advance the clock past the window so wait() can proceed.
	set(time.Date(2025, 1, 1, 0, 0, 0, 200_000_000, time.UTC))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second wait = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second wait did not return after window slid")
	}
}

func TestSendQuota_ContextCancel(t *testing.T) {
	withNow(t)
	q := sendQuota{limit: 1, window: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())

	// Fill the only slot.
	if err := q.wait(ctx); err != nil {
		t.Fatalf("first wait = %v, want nil", err)
	}

	// Second wait should block; cancel ctx to abort it.
	done := make(chan error, 1)
	go func() {
		done <- q.wait(ctx)
	}()

	// Give the goroutine time to enter the blocked select.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("wait = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked wait did not return after ctx cancel")
	}
}

func TestSendQuota_SlidingWindowEvicts(t *testing.T) {
	set := withNow(t)
	// limit 2 per 1s. Send 2, advance 1s+, send 2 more — all should pass.
	q := sendQuota{limit: 2, window: time.Second}
	ctx := context.Background()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	set(base)
	if err := q.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.wait(ctx); err != nil {
		t.Fatal(err)
	}

	// Advance well past the window; old timestamps must be evicted.
	set(base.Add(2 * time.Second))
	if err := q.wait(ctx); err != nil {
		t.Fatalf("after window: %v", err)
	}
	if err := q.wait(ctx); err != nil {
		t.Fatalf("after window 2: %v", err)
	}
}

func TestNew_BurstLimitParsed(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"burst_limit":      20,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pf.(*Platform)
	if p.sendQuota.limit != 20 {
		t.Errorf("limit = %d, want 20", p.sendQuota.limit)
	}
	if p.sendQuota.window != 60*time.Second {
		t.Errorf("window = %v, want 60s (default)", p.sendQuota.window)
	}
}

func TestNew_BurstLimitDisabledByDefault(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pf.(*Platform)
	if p.sendQuota.limit != 0 {
		t.Errorf("limit = %d, want 0 (disabled by default)", p.sendQuota.limit)
	}
}

func TestNew_BurstLimitCustomWindow(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":           "ww_test",
		"corp_secret":       "sec_test",
		"agent_id":          "1000002",
		"callback_token":    "cb_token",
		"callback_aes_key":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"burst_limit":       5,
		"burst_window_secs": 120,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pf.(*Platform)
	if p.sendQuota.limit != 5 {
		t.Errorf("limit = %d, want 5", p.sendQuota.limit)
	}
	if p.sendQuota.window != 120*time.Second {
		t.Errorf("window = %v, want 120s", p.sendQuota.window)
	}
}

func TestNewWebSocket_BurstLimitParsed(t *testing.T) {
	pf, err := New(map[string]any{
		"mode":              "websocket",
		"bot_id":            "bot_test",
		"bot_secret":        "sec_test",
		"burst_limit":       10,
		"burst_window_secs": 30,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pf.(*WSPlatform)
	if p.sendQuota.limit != 10 {
		t.Errorf("limit = %d, want 10", p.sendQuota.limit)
	}
	if p.sendQuota.window != 30*time.Second {
		t.Errorf("window = %v, want 30s", p.sendQuota.window)
	}
}

func TestNewWebSocket_BurstLimitDisabledByDefault(t *testing.T) {
	pf, err := New(map[string]any{
		"mode":       "websocket",
		"bot_id":     "bot_test",
		"bot_secret": "sec_test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pf.(*WSPlatform)
	if p.sendQuota.limit != 0 {
		t.Errorf("limit = %d, want 0 (disabled by default)", p.sendQuota.limit)
	}
}

func TestPickInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{int(7), 7},
		{int64(8), 8},
		{float64(9.9), 9},
		{"10", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := pickInt(c.in); got != c.want {
			t.Errorf("pickInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
