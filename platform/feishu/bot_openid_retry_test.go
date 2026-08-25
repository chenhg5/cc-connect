package feishu

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// newBotInfoTestServer serves the tenant-access-token endpoint plus the bot
// info endpoint. botInfoStatus decides, per attempt, whether the bot info call
// fails; attempts counts how many times bot info was hit.
func newBotInfoTestServer(t *testing.T, openID string, failFirst int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`))
			return
		}
		if r.URL.Path == "/open-apis/bot/v3/info" {
			n := attempts.Add(1)
			if failFirst < 0 || n <= failFirst {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":500,"msg":"internal error"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","bot":{"open_id":"` + openID + `"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

func newBotOpenIDRetryPlatform(srv *httptest.Server, initial, maxDelay time.Duration) *Platform {
	return &Platform{
		platformName: "feishu",
		client: lark.NewClient("app-id", "app-secret",
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
			lark.WithEnableTokenCache(false),
		),
		botOpenIDRetryInitialDelay: initial,
		botOpenIDRetryMaxDelay:     maxDelay,
	}
}

// TestStartBotOpenIDRetry_RecoversAfterFailure covers the startup race that
// motivated this retry loop: the first fetch fails (network not ready yet) and
// a later attempt succeeds, restoring group chat filtering.
func TestStartBotOpenIDRetry_RecoversAfterFailure(t *testing.T) {
	srv, attempts := newBotInfoTestServer(t, "ou_bot_recovered", 2)
	p := newBotOpenIDRetryPlatform(srv, 10*time.Millisecond, 40*time.Millisecond)
	defer p.stopBotOpenIDRetry()

	p.startBotOpenIDRetry()

	deadline := time.Now().Add(5 * time.Second)
	for p.getBotOpenID() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := p.getBotOpenID(); got != "ou_bot_recovered" {
		t.Fatalf("botOpenID = %q after %d attempts, want %q", got, attempts.Load(), "ou_bot_recovered")
	}
	if n := attempts.Load(); n < 3 {
		t.Fatalf("attempts = %d, want at least 3 (two failures then success)", n)
	}
}

// TestStartBotOpenIDRetry_OnlyOneGoroutine ensures a second call while a retry
// loop is already running does not spawn a duplicate goroutine: a single stop
// must then silence all retry traffic.
func TestStartBotOpenIDRetry_OnlyOneGoroutine(t *testing.T) {
	srv, attempts := newBotInfoTestServer(t, "ou_bot", -1) // always fails
	p := newBotOpenIDRetryPlatform(srv, 10*time.Millisecond, 20*time.Millisecond)
	defer p.stopBotOpenIDRetry()

	p.startBotOpenIDRetry()
	p.startBotOpenIDRetry()

	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	p.stopBotOpenIDRetry()

	settled := attempts.Load()
	time.Sleep(200 * time.Millisecond)
	if after := attempts.Load(); after > settled+1 {
		t.Fatalf("a second retry goroutine survived the single stop: attempts %d -> %d", settled, after)
	}
}

// TestStopBotOpenIDRetry_StopsGoroutine verifies the retry goroutine exits on
// shutdown instead of leaking and hammering the API forever.
func TestStopBotOpenIDRetry_StopsGoroutine(t *testing.T) {
	srv, attempts := newBotInfoTestServer(t, "ou_bot", -1) // always fails
	p := newBotOpenIDRetryPlatform(srv, 10*time.Millisecond, 20*time.Millisecond)

	p.startBotOpenIDRetry()

	// Wait until the loop has actually made at least one attempt.
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if attempts.Load() == 0 {
		t.Fatal("retry goroutine never called the bot info API")
	}

	p.stopBotOpenIDRetry()
	if p.botOpenIDRetryCancel != nil {
		t.Fatal("botOpenIDRetryCancel not cleared after stop")
	}

	// Give any surviving goroutine several backoff windows to prove itself.
	settled := attempts.Load()
	time.Sleep(200 * time.Millisecond)
	after := attempts.Load()
	if after > settled+1 { // +1 tolerates one attempt already in flight
		t.Fatalf("retry goroutine still running after stop: attempts %d -> %d", settled, after)
	}

	// Stop must be idempotent.
	p.stopBotOpenIDRetry()
}

// TestStop_CancelsBotOpenIDRetry verifies Platform.Stop wires through to the
// retry goroutine, so shutdown does not leak it.
func TestStop_CancelsBotOpenIDRetry(t *testing.T) {
	srv, _ := newBotInfoTestServer(t, "ou_bot", -1) // always fails
	p := newBotOpenIDRetryPlatform(srv, 10*time.Millisecond, 20*time.Millisecond)

	p.startBotOpenIDRetry()
	if p.botOpenIDRetryCancel == nil {
		t.Fatal("retry loop did not start")
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if p.botOpenIDRetryCancel != nil {
		t.Fatal("Stop() did not cancel the bot open_id retry goroutine")
	}
}
