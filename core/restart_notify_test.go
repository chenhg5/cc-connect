package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// restartNotifyStub is a Platform that records Send calls and can be made
// to fail the first N sends. It also implements ReplyContextReconstructor
// so the post-restart notify path can find a reply context for the
// session key.
type restartNotifyStub struct {
	mu             sync.Mutex
	name           string
	sent           []string
	failFirstN     int32
	failNextError  error
	ready          atomic.Bool
	reconstructRCT string
}

func (p *restartNotifyStub) Name() string { return p.name }
func (p *restartNotifyStub) Start(MessageHandler) error {
	return nil
}
func (p *restartNotifyStub) Stop() error { return nil }
func (p *restartNotifyStub) Reply(_ context.Context, _ any, content string) error {
	return p.recordSend(content)
}
func (p *restartNotifyStub) Send(_ context.Context, _ any, content string) error {
	return p.recordSend(content)
}

func (p *restartNotifyStub) recordSend(content string) error {
	p.mu.Lock()
	failCount := p.failFirstN
	failErr := p.failNextError
	p.mu.Unlock()
	if atomic.LoadInt32(&failCount) > 0 {
		atomic.AddInt32(&p.failFirstN, -1)
		if failErr != nil {
			return failErr
		}
		return errors.New("simulated send failure")
	}
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	return nil
}

func (p *restartNotifyStub) sentTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.sent))
	copy(out, p.sent)
	return out
}

func (p *restartNotifyStub) ReconstructReplyCtx(sessionKey string) (any, error) {
	if p.reconstructRCT != "" {
		return p.reconstructRCT, nil
	}
	return "rctx-" + sessionKey, nil
}

// markReady simulates the engine's onPlatformReady transition. The
// engine's lookupReadyPlatform checks the platformReady map, which is
// normally mutated by the real engine; tests below use the public
// OnPlatformReady path to do the same.
func (p *restartNotifyStub) markReady(t *testing.T, e *Engine) {
	t.Helper()
	if !p.ready.CompareAndSwap(false, true) {
		return
	}
	e.OnPlatformReady(p)
}

// waitForSent polls until the stub has recorded at least n sent
// messages, or fails the test after timeout. Used so tests don't
// have to know the exact dispatch timing.
func (p *restartNotifyStub) waitForSent(t *testing.T, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := p.sentTexts(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return p.sentTexts()
}

// TestRestartNotify_DispatchesAfterPlatformReady covers the core
// issue #1383 fix: the post-restart notify must wait for the
// platform to be ready, not fire immediately at startup.
func TestRestartNotify_DispatchesAfterPlatformReady(t *testing.T) {
	plat := &restartNotifyStub{name: "telegram"}
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)

	// Queue the notify BEFORE marking ready — this mirrors the real
	// startup order in cmd/cc-connect/main.go where SetPendingRestartNotify
	// is called right after e.Start() returns.
	engine.SetPendingRestartNotify(&RestartRequest{
		Platform:   "telegram",
		SessionKey: "session-1",
	})

	// At this point the platform is not ready; nothing should have been
	// sent yet.
	if got := plat.sentTexts(); len(got) != 0 {
		t.Fatalf("notify fired before platform ready: %v", got)
	}

	// Simulate the 2.6s Telegram connect window described in the issue.
	time.Sleep(300 * time.Millisecond) // simulate async startup delay
	plat.markReady(t, engine)

	// Notify should now arrive.
	got := plat.waitForSent(t, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 sent message after ready, got %d (%v)", len(got), got)
	}
	if !strings.Contains(got[0], "restarted successfully") {
		t.Errorf("expected restart success text, got %q", got[0])
	}

	// Pending slot should be cleared after dispatch.
	if engine.ConsumePendingRestartNotify() != nil {
		t.Error("pending restart notify was not cleared after dispatch")
	}
}

// TestRestartNotify_AlreadyReadySucceedsImmediately covers the
// non-async platform case: if the platform is marked ready before
// the notify is queued (e.g. synchronous platforms), the dispatch
// still happens without a 10s wait.
func TestRestartNotify_AlreadyReadySucceedsImmediately(t *testing.T) {
	plat := &restartNotifyStub{name: "feishu"}
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)

	// Mark ready first.
	plat.markReady(t, engine)

	engine.SetPendingRestartNotify(&RestartRequest{
		Platform:   "feishu",
		SessionKey: "session-1",
	})

	got := plat.waitForSent(t, 1, 1*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 sent message, got %d (%v)", len(got), got)
	}
}

// TestRestartNotify_RetriesOnSendFailure verifies the retry path:
// first attempt fails, second attempt succeeds, message lands.
func TestRestartNotify_RetriesOnSendFailure(t *testing.T) {
	plat := &restartNotifyStub{
		name:       "telegram",
		failFirstN: 1, // fail first attempt, succeed on second
	}
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)
	plat.markReady(t, engine)

	engine.SetPendingRestartNotify(&RestartRequest{
		Platform:   "telegram",
		SessionKey: "session-1",
	})

	// With backoff (0 + 500ms), the second send should land within 2s.
	got := plat.waitForSent(t, 1, 3*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 sent message after retry, got %d (%v)", len(got), got)
	}
}

// TestRestartNotify_ExhaustsRetriesNoHang verifies that when every
// attempt fails, the dispatcher gives up (does not loop forever)
// and clears the pending slot.
func TestRestartNotify_ExhaustsRetriesNoHang(t *testing.T) {
	plat := &restartNotifyStub{
		name:       "telegram",
		failFirstN: 100, // effectively never succeed within 3 attempts
	}
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)
	plat.markReady(t, engine)

	engine.SetPendingRestartNotify(&RestartRequest{
		Platform:   "telegram",
		SessionKey: "session-1",
	})

	// backoffs = 0, 500ms, 1500ms → total ~2s. Give a generous 4s
	// window for the dispatch to give up and clear the pending slot.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if engine.ConsumePendingRestartNotify() == nil {
			// pending cleared → dispatcher exited cleanly
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pending restart notify was not cleared after retries exhausted")
}

// TestRestartNotify_TimesOutIfPlatformNeverReady verifies the
// safety timeout: if the target platform never reaches ready,
// the notify is dropped with a warning (we don't hang forever).
// Uses a short timeout via SetPendingRestartTimeout to keep the
// test fast.
func TestRestartNotify_TimesOutIfPlatformNeverReady(t *testing.T) {
	plat := &restartNotifyStub{name: "telegram"} // never marked ready
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)
	engine.SetPendingRestartTimeout(300 * time.Millisecond)

	engine.SetPendingRestartNotify(&RestartRequest{
		Platform:   "telegram",
		SessionKey: "session-1",
	})

	// Wait for the timeout path to fire. The dispatcher logs a warn
	// and clears the slot. We poll until the slot is empty, with
	// generous margin beyond the 300ms timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if engine.ConsumePendingRestartNotify() == nil {
			// slot cleared → timeout fired, dispatcher exited cleanly
			if got := plat.sentTexts(); len(got) != 0 {
				t.Fatalf("nothing should be sent when platform never ready, got %v", got)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pending restart notify was not cleared by safety timeout")
}

// TestRestartNotify_NilNotifyIgnored is a defensive check: passing
// a nil notify to SetPendingRestartNotify should be a no-op (does
// not panic, does not crash the engine).
func TestRestartNotify_NilNotifyIgnored(t *testing.T) {
	plat := &restartNotifyStub{name: "telegram"}
	engine := NewEngine("test", &stubAgent{}, []Platform{plat}, "", LangEnglish)
	plat.markReady(t, engine)

	engine.SetPendingRestartNotify(nil)

	// Wait briefly to ensure no dispatch fires.
	time.Sleep(200 * time.Millisecond)
	if got := plat.sentTexts(); len(got) != 0 {
		t.Fatalf("nil notify should not send anything, got %v", got)
	}
	if engine.ConsumePendingRestartNotify() != nil {
		t.Error("nil notify should not occupy the pending slot")
	}
}
