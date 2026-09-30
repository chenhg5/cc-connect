package wecom

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/BurntSushi/toml"
)

func testQuota(t *testing.T, r *quotaRegistry, cfg outboundQuotaConfig, id string) *outboundQuota {
	t.Helper()
	q, err := r.register(quotaAccountKey{mode: "websocket", endpoint: "wss://example", appID: id}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	return q
}
func testLimits(minute, hour int) outboundQuotaConfig {
	return outboundQuotaConfig{enabled: true, messages: quotaLimits{minute, hour}, uploads: quotaLimits{minute, hour}}
}
func takeQuota(t *testing.T, q *outboundQuota, target quotaTarget) *quotaPermit {
	t.Helper()
	p, err := q.acquire(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func assertNoQuotaResult(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("unexpected early completion: %v", err)
	default:
	}
}

func TestOutboundQuota_Config(t *testing.T) {
	var opts map[string]any
	if _, err := toml.Decode("[outbound_quota]\nenabled=true\nmessages_per_minute=12\nmessages_per_hour=800", &opts); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseOutboundQuota(opts)
	if err != nil || !cfg.enabled || cfg.messages != (quotaLimits{12, 800}) || cfg.uploads != (quotaLimits{20, 900}) {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	cfg, err = parseOutboundQuota(nil)
	if err != nil || cfg.enabled {
		t.Fatalf("default cfg=%+v err=%v", cfg, err)
	}
	for _, v := range []any{0, -1, 1.2, math.NaN(), math.Inf(1), float64(math.MaxInt), "20", nil} {
		if _, err := parseOutboundQuota(map[string]any{"outbound_quota": map[string]any{"messages_per_minute": v}}); err == nil {
			t.Fatalf("accepted invalid quota %v", v)
		}
	}
	for _, v := range []any{int(20), int64(20), float64(20)} {
		if n, err := quotaPositiveInt(v); err != nil || n != 20 {
			t.Fatalf("integer %v: %d %v", v, n, err)
		}
	}
	for _, v := range []any{false, map[string]any{"enabled": "true"}, map[string]any{"messages_per_mintue": 20}} {
		if _, err := parseOutboundQuota(map[string]any{"outbound_quota": v}); err == nil {
			t.Fatalf("accepted malformed config %v", v)
		}
	}
}

func TestOutboundQuota_MinuteAndHourWindowsAreAtomic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 2), "bot")
		target := quotaTarget{recipient: "chat"}
		takeQuota(t, q, target).finish(true)
		done := make(chan error, 1)
		go func() { p, err := q.acquire(context.Background(), target); p.finish(true); done <- err }()
		synctest.Wait()
		time.Sleep(time.Minute - time.Nanosecond)
		assertNoQuotaResult(t, done)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		go func() { p, err := q.acquire(context.Background(), target); p.finish(true); done <- err }()
		synctest.Wait()
		time.Sleep(58 * time.Minute)
		assertNoQuotaResult(t, done)
		time.Sleep(time.Minute)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// A waiter did not consume hour capacity while waiting for its minute slot.
		if got := time.Since(start); got != time.Hour {
			t.Fatalf("elapsed=%v", got)
		}
	})
}

func TestOutboundQuota_InFlightNeverExpiresAndUnsentRefunds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 1), "bot")
		target := quotaTarget{recipient: "chat"}
		first := takeQuota(t, q, target)
		done := make(chan error, 1)
		go func() { p, err := q.acquire(context.Background(), target); p.finish(false); done <- err }()
		synctest.Wait()
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		assertNoQuotaResult(t, done)
		first.finish(false)
		first.finish(true) // exactly-once refund
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		takeQuota(t, q, target).finish(true)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := q.acquire(ctx, target); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed attempts must stay charged: %v", err)
		}
	})
}

func TestOutboundQuota_FIFOAndCancelledWaiterDoesNotConsume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 100), "bot")
		target := quotaTarget{recipient: "chat"}
		held := takeQuota(t, q, target)
		order := make(chan int, 4)
		ctx, cancel := context.WithCancel(context.Background())
		cancelled := make(chan error, 1)
		go func() { p, err := q.acquire(ctx, target); p.finish(false); cancelled <- err }()
		synctest.Wait()
		for i := range 4 {
			go func() {
				p, err := q.acquire(context.Background(), target)
				if err != nil {
					t.Error(err)
					return
				}
				order <- i
				p.finish(false)
			}()
			synctest.Wait()
		}
		cancel()
		synctest.Wait()
		if err := <-cancelled; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		held.finish(false)
		synctest.Wait()
		for i := range 4 {
			if got := <-order; got != i {
				t.Fatalf("FIFO got %d want %d", got, i)
			}
		}
	})
}

func TestOutboundQuota_SharedAccountsTargetsAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &quotaRegistry{}
		cfg := testLimits(1, 2)
		q1 := testQuota(t, r, cfg, "same")
		q2 := testQuota(t, r, cfg, "same")
		q3 := testQuota(t, r, cfg, "different")
		chat := quotaTarget{recipient: "group"}
		takeQuota(t, q1, chat).finish(true)
		takeQuota(t, q2, quotaTarget{recipient: "other-group"}).finish(true)
		takeQuota(t, q2, quotaTarget{upload: true}).finish(true)
		takeQuota(t, q3, chat).finish(true)
		blocked := make(chan error, 1)
		go func() { p, err := q2.acquire(context.Background(), chat); p.finish(true); blocked <- err }()
		synctest.Wait()
		assertNoQuotaResult(t, blocked)
		q1.close()
		q1.close()
		synctest.Wait()
		assertNoQuotaResult(t, blocked)
		q2.close()
		synctest.Wait()
		if err := <-blocked; !errors.Is(err, context.Canceled) {
			t.Fatalf("stop = %v", err)
		}
		q4 := testQuota(t, r, cfg, "same")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := q4.acquire(ctx, chat); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("recreated account forgot history: %v", err)
		}
	})
}

func TestOutboundQuota_ConcurrentReservationsDoNotOvershoot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(3, 3), "bot")
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan *quotaPermit, 20)
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := q.acquire(ctx, quotaTarget{recipient: "chat"})
				if err == nil {
					got <- p
				} else if !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			}()
		}
		synctest.Wait()
		if len(got) != 3 {
			t.Fatalf("granted %d, want 3", len(got))
		}
		cancel()
		synctest.Wait()
		wg.Wait()
		close(got)
		for p := range got {
			p.finish(true)
		}
	})
}

func TestOutboundQuota_ConfigConflictReportsDifferences(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*outboundQuotaConfig)
		want   []string
	}{
		{"enabled", func(c *outboundQuotaConfig) { c.enabled = false }, []string{"enabled=false (this project) vs true (existing account)"}},
		{"messages_per_minute", func(c *outboundQuotaConfig) { c.messages.minute = 3 }, []string{"messages_per_minute=3 (this project) vs 1 (existing account)"}},
		{"messages_per_hour", func(c *outboundQuotaConfig) { c.messages.hour = 4 }, []string{"messages_per_hour=4 (this project) vs 2 (existing account)"}},
		{"uploads_per_minute", func(c *outboundQuotaConfig) { c.uploads.minute = 5 }, []string{"uploads_per_minute=5 (this project) vs 1 (existing account)"}},
		{"uploads_per_hour", func(c *outboundQuotaConfig) { c.uploads.hour = 6 }, []string{"uploads_per_hour=6 (this project) vs 2 (existing account)"}},
		{"multiple", func(c *outboundQuotaConfig) {
			c.messages.minute = 3
			c.uploads.hour = 6
		}, []string{
			"messages_per_minute=3 (this project) vs 1 (existing account)",
			"uploads_per_hour=6 (this project) vs 2 (existing account)",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &quotaRegistry{}
			cfg := testLimits(1, 2)
			testQuota(t, r, cfg, "bot")
			tt.change(&cfg)
			_, err := r.register(quotaAccountKey{mode: "websocket", endpoint: "wss://example", appID: "bot"}, cfg)
			want := "wecom: conflicting outbound_quota settings for the same account: " + strings.Join(tt.want, "; ") + "; configure all projects identically"
			if err == nil || err.Error() != want {
				t.Fatalf("conflict error = %v, want %q", err, want)
			}
		})
	}
}

func TestOutboundQuota_ConfigConflictAndIdleCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &quotaRegistry{}
		cfg := testLimits(1, 2)
		q := testQuota(t, r, cfg, "bot")
		key := quotaAccountKey{mode: "websocket", endpoint: "wss://example", appID: "bot"}
		for _, bad := range []outboundQuotaConfig{testLimits(2, 2), {}} {
			if _, err := r.register(key, bad); err == nil {
				t.Fatal("accepted conflicting active account")
			}
		}
		takeQuota(t, q, quotaTarget{recipient: "chat"}).finish(true)
		time.Sleep(time.Hour)
		takeQuota(t, q, quotaTarget{recipient: "other"}).finish(false)
		q.account.mu.Lock()
		if _, exists := q.account.buckets[quotaTarget{recipient: "chat"}]; exists {
			t.Error("idle recipient retained")
		}
		q.account.mu.Unlock()
		q.close()
		time.Sleep(time.Minute)
		testQuota(t, r, cfg, "other-bot")
		if _, exists := r.accounts[key]; exists {
			t.Fatal("unused account retained")
		}
	})
}

func TestOutboundQuota_DisabledAndCancelledContext(t *testing.T) {
	q := testQuota(t, &quotaRegistry{}, outboundQuotaConfig{}, "bot")
	for range 100 {
		takeQuota(t, q, quotaTarget{}).finish(true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.acquire(ctx, quotaTarget{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	q.close()
	ctx, cancel = q.context(context.Background())
	defer cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("closed lifecycle did not cancel send")
	}
}

func TestOutboundQuota_EndpointIdentity(t *testing.T) {
	a := quotaEndpoint(" wss://EXAMPLE.test:443/path?b=2&a=1 ")
	b := quotaEndpoint("wss://example.test/path?a=1&b=2")
	if a != b {
		t.Fatalf("%s != %s", a, b)
	}
	if a == quotaEndpoint("wss://example.test/path?a=2&b=1") {
		t.Fatal("deployment query lost")
	}
	if a == quotaEndpoint("wss://example.test/path/?a=1&b=2") {
		t.Fatal("custom endpoint path lost")
	}
}
