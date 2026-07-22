package feishu

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/chenhg5/cc-connect/core"
)

// fakeWSClient is a stand-in wsClientIface used by Start/Stop lifecycle tests.
// Its Start blocks until ctx is canceled, so Stop returning causes the
// goroutine to wind down (issue #1562 regression coverage).
type fakeWSClient struct {
	closeOnce sync.Once
	started   chan struct{}
}

func newFakeWSClient() *fakeWSClient {
	return &fakeWSClient{started: make(chan struct{})}
}

func (f *fakeWSClient) Start(ctx context.Context) error {
	f.closeOnce.Do(func() { close(f.started) })
	<-ctx.Done()
	return nil
}

// stubWSFactory replaces wsClientFactory for the lifetime of the test. The
// returned restore func must be deferred. Each invocation records the
// appID/appSecret it was called with and returns a fresh fakeWSClient so
// per-platform goroutines are independent.
func stubWSFactory(t *testing.T) (calls *[]factoryCall, fakes *[]*fakeWSClient, restore func()) {
	t.Helper()
	orig := wsClientFactory
	var mu sync.Mutex
	var cs []factoryCall
	var fs []*fakeWSClient
	wsClientFactory = func(appID, appSecret string, opts ...larkws.ClientOption) wsClientIface {
		f := newFakeWSClient()
		mu.Lock()
		cs = append(cs, factoryCall{AppID: appID, AppSecret: appSecret})
		fs = append(fs, f)
		mu.Unlock()
		return f
	}
	return &cs, &fs, func() { wsClientFactory = orig }
}

type factoryCall struct {
	AppID, AppSecret string
}

// resetSharedWS clears the package-level sharedWSGroups map so tests start
// from a clean slate.
func resetSharedWS(t *testing.T) func() {
	t.Helper()
	cleanup := func() {
		sharedWSMu.Lock()
		defer sharedWSMu.Unlock()
		sharedWSGroups = map[string]*sharedWSGroup{}
	}
	cleanup()
	return cleanup
}

// captureSlog swaps slog.Default() with a text handler backed by the returned
// buffer; the restore func puts back the original handler. Tests use this to
// assert structured slog output without touching the production logger.
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	orig := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(orig) }
}

// newMinimalPlatform constructs a Platform wired up just enough for
// Start()/Stop() to run their path: handler, dedup, lark client (for
// fetchBotOpenID), and the platformName. encryptKey is intentionally empty
// so startWebSocketMode is exercised (not startWebhookMode).
func newMinimalPlatform(name, appID, appSecret string) *Platform {
	return &Platform{
		platformName: name,
		domain:       "feishu.cn",
		appID:        appID,
		appSecret:    appSecret,
		client:       lark.NewClient(appID, appSecret),
		dedup:        &core.MessageDedup{},
	}
}

// TestStart_IndependentMode_DifferentAppIDs is the load-bearing regression
// test for issue #1562: two projects with different Feishu app_id in one
// process must each open their own WebSocket via wsClientFactory, hold
// independent sharedGroups (each containing only itself), and survive Stop()
// on one without affecting the other.
func TestStart_IndependentMode_DifferentAppIDs(t *testing.T) {
	defer resetSharedWS(t)()
	calls, fakes, restore := stubWSFactory(t)
	defer restore()

	p1 := newMinimalPlatform("feishu-a", "cli_a", "sec_a")
	p2 := newMinimalPlatform("feishu-b", "cli_b", "sec_b")

	if err := p1.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p1.Start: %v", err)
	}
	if err := p2.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p2.Start: %v", err)
	}
	t.Cleanup(func() { _ = p1.Stop(); _ = p2.Stop() })

	// wsClientFactory must have been called once per platform with distinct appIDs.
	if len(*calls) != 2 {
		t.Fatalf("wsClientFactory call count = %d, want 2", len(*calls))
	}
	if (*calls)[0].AppID != "cli_a" || (*calls)[1].AppID != "cli_b" {
		t.Errorf("factory calls in unexpected order: %+v", *calls)
	}

	// Each platform's sharedGroup must contain only itself.
	plats := p1.sharedGroup.allPlatforms()
	if len(plats) != 1 || plats[0] != p1 {
		t.Errorf("p1.sharedGroup = %v, want [p1]", plats)
	}
	plats = p2.sharedGroup.allPlatforms()
	if len(plats) != 1 || plats[0] != p2 {
		t.Errorf("p2.sharedGroup = %v, want [p2]", plats)
	}

	// Each platform owns its own wsClient.
	if len(*fakes) != 2 {
		t.Fatalf("fakes len = %d, want 2", len(*fakes))
	}
	if p1.wsClient != (*fakes)[0] {
		t.Error("p1.wsClient != fakes[0]")
	}
	if p2.wsClient != (*fakes)[1] {
		t.Error("p2.wsClient != fakes[1]")
	}

	// Stop one platform: the other must still be running.
	if err := p1.Stop(); err != nil {
		t.Fatalf("p1.Stop: %v", err)
	}
	if p2.getCancel() == nil {
		t.Error("p2.getCancel() = nil after p1.Stop; independent mode must not strand other platforms")
	}
}

// TestStart_SharedMode_SameAppID_StillWorks is a regression guard: the legacy
// share_ws=true path (multiple projects fan-out from one shared WS) must keep
// working for operators who intentionally point multiple projects at the same
// Feishu app. Only the first project calls wsClientFactory; the second
// shares the primary's connection.
func TestStart_SharedMode_SameAppID_StillWorks(t *testing.T) {
	defer resetSharedWS(t)()
	calls, _, restore := stubWSFactory(t)
	defer restore()

	p1 := newMinimalPlatform("feishu-c", "cli_x", "sec_x")
	p1.shareWS = true
	p2 := newMinimalPlatform("feishu-d", "cli_x", "sec_x")
	p2.shareWS = true

	if err := p1.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p1.Start: %v", err)
	}
	if err := p2.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p2.Start: %v", err)
	}
	t.Cleanup(func() { _ = p1.Stop(); _ = p2.Stop() })

	// Only the primary should have called wsClientFactory.
	if len(*calls) != 1 {
		t.Fatalf("wsClientFactory call count = %d, want 1 (only the primary)", len(*calls))
	}
	if (*calls)[0].AppID != "cli_x" {
		t.Errorf("primary factory call appID = %q, want cli_x", (*calls)[0].AppID)
	}

	// Both platforms must end up in the same shared group.
	if !p1.isWSPrimary {
		t.Error("p1.isWSPrimary = false, want true (first registered with shared ws)")
	}
	if p2.isWSPrimary {
		t.Error("p2.isWSPrimary = true, want false (secondary in shared ws)")
	}
	if p1.sharedGroup != p2.sharedGroup {
		t.Error("p1.sharedGroup != p2.sharedGroup in shared mode")
	}
	plats := p1.sharedGroup.allPlatforms()
	if len(plats) != 2 {
		t.Errorf("shared group platform count = %d, want 2", len(plats))
	}
}

// TestStart_DefaultIsIndependent pins the opt-in semantics: with no share_ws
// flag the platform must land in independent mode (wsClientFactory called,
// sharedGroup contains only self). This protects the new default from a
// future regression that flips it back to the legacy shared-by-default
// behavior.
func TestStart_DefaultIsIndependent(t *testing.T) {
	defer resetSharedWS(t)()
	calls, _, restore := stubWSFactory(t)
	defer restore()

	p := newMinimalPlatform("feishu-default", "cli_default", "sec_default")
	if err := p.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	if len(*calls) != 1 {
		t.Fatalf("wsClientFactory call count = %d, want 1 (default is independent mode)", len(*calls))
	}
	if !p.isWSPrimary {
		t.Error("p.isWSPrimary = false, want true in independent mode")
	}
	plats := p.sharedGroup.allPlatforms()
	if len(plats) != 1 || plats[0] != p {
		t.Errorf("p.sharedGroup = %v, want [p] in independent mode", plats)
	}
}

// TestStop_SharedPrimaryShutdownWarns ensures that tearing down a primary in
// share_ws=true mode while sibling platforms remain emits the explicit
// "shared-ws primary shutting down" warning with the right remaining count.
// Operators grep these warnings to know why their secondary lost events.
func TestStop_SharedPrimaryShutdownWarns(t *testing.T) {
	defer resetSharedWS(t)()
	_, _, restoreFactory := stubWSFactory(t)
	defer restoreFactory()
	buf, restoreSlog := captureSlog(t)
	defer restoreSlog()

	p1 := newMinimalPlatform("feishu-primary", "cli_w", "sec_w")
	p1.shareWS = true
	p2 := newMinimalPlatform("feishu-sibling", "cli_w", "sec_w")
	p2.shareWS = true

	if err := p1.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p1.Start: %v", err)
	}
	if err := p2.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("p2.Start: %v", err)
	}

	buf.Reset() // drop slog noise from Start so we only inspect Stop output.
	if err := p1.Stop(); err != nil {
		t.Fatalf("p1.Stop: %v", err)
	}
	_ = p2.Stop()

	log := buf.String()
	if !strings.Contains(log, "shared-ws primary shutting down") {
		t.Errorf("expected warning containing %q, got:\n%s", "shared-ws primary shutting down", log)
	}
	if !strings.Contains(log, "remaining=1") {
		t.Errorf("expected warning containing %q, got:\n%s", "remaining=1", log)
	}
	if !strings.Contains(log, "app_id=cli_w") {
		t.Errorf("expected warning containing %q, got:\n%s", "app_id=cli_w", log)
	}
}

// TestStop_IndependentMode_CancelsOwnWS verifies that independent-mode Stop()
// cancels the platform's own context and lets the fakeWSClient's Start
// goroutine return promptly. Replaces a goleak-style dep (which is not in
// go.mod) with a runtime.NumGoroutine delta check on a short settle window.
func TestStop_IndependentMode_CancelsOwnWS(t *testing.T) {
	defer resetSharedWS(t)()
	_, fakes, restore := stubWSFactory(t)
	defer restore()

	p := newMinimalPlatform("feishu-leak", "cli_leak", "sec_leak")
	if err := p.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(*fakes); got != 1 {
		t.Fatalf("fakes len = %d, want 1", got)
	}

	before := runtime.NumGoroutine()
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Give the goroutine(s) a moment to wind down. We allow +2 leeway for
	// runtime/test noise; the before reading must be taken before Stop so
	// the test goroutine itself isn't counted twice.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := runtime.NumGoroutine(); n <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutine count did not return to baseline within 2s after Stop (before=%d, after=%d)", before, runtime.NumGoroutine())
}
