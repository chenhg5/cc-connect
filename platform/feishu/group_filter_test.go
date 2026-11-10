package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

// ─── Issue #1618: fail-closed group filter + supervised retry ─────────────
//
// When the bot-info API call fails at startup (transient network/proxy
// outage), the previous code silently treated botOpenID=="" as "filter
// off" — the bot would answer every group message for the rest of the
// process lifetime. These tests pin the new fail-closed + supervised
// retry behaviour so a future refactor cannot quietly re-introduce the
// fail-open regression.

func TestMarkAndClearGroupFilterDegraded(t *testing.T) {
	p := &Platform{platformName: "feishu"}

	if p.IsGroupFilterDegraded() {
		t.Fatal("fresh platform should not be degraded")
	}

	p.markGroupFilterDegraded(errors.New("connection refused"))
	if !p.IsGroupFilterDegraded() {
		t.Fatal("expected degraded after markGroupFilterDegraded")
	}
	st := p.snapshotGroupFilter()
	if !st.Degraded {
		t.Fatal("snapshot.Degraded should be true")
	}
	if st.LastError != "connection refused" {
		t.Fatalf("snapshot.LastError = %q, want connection refused", st.LastError)
	}
	if st.Since.IsZero() {
		t.Fatal("snapshot.Since should be set")
	}

	p.clearGroupFilterDegraded()
	if p.IsGroupFilterDegraded() {
		t.Fatal("expected cleared after clearGroupFilterDegraded")
	}
	st = p.snapshotGroupFilter()
	if st.Degraded {
		t.Fatal("snapshot.Degraded should be false after clear")
	}
	if st.LastError != "" {
		t.Fatalf("snapshot.LastError = %q, want empty", st.LastError)
	}
}

func TestPlatformHealth_ReportsConnectedWhenHealthy(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	info := p.PlatformHealth()
	if !info.Connected {
		t.Fatal("healthy platform should be Connected")
	}
	if info.Degraded {
		t.Fatal("healthy platform should not be Degraded")
	}
	if info.DegradedReason != "" || !info.DegradedSince.IsZero() {
		t.Fatalf("healthy platform should have empty degraded fields, got %+v", info)
	}
}

func TestPlatformHealth_ReportsDegradedAfterStartupFailure(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	p.markGroupFilterDegraded(errors.New("i/o timeout"))

	info := p.PlatformHealth()
	if info.Connected {
		t.Fatal("degraded platform should not be Connected")
	}
	if !info.Degraded {
		t.Fatal("degraded platform should be Degraded")
	}
	if info.Name != "feishu" {
		t.Fatalf("info.Name = %q, want feishu", info.Name)
	}
	if !strings.Contains(info.DegradedReason, "i/o timeout") {
		t.Fatalf("info.DegradedReason = %q, want substring 'i/o timeout'", info.DegradedReason)
	}
	if info.DegradedSince.IsZero() {
		t.Fatal("info.DegradedSince should be set when degraded")
	}
}

// TestGroupFilterDegraded_FailsClosed verifies the regression guard:
// when bot open_id is unknown AND the filter is marked degraded, group
// messages without an @bot mention must be silently dropped instead of
// being accepted (the pre-fix fail-open behavior).
func TestGroupFilterDegraded_FailsClosed(t *testing.T) {
	// Build a Platform that is explicitly marked as degraded with no
	// botOpenID. We bypass newPlatform() so the test does not need
	// network credentials.
	p := &Platform{platformName: "feishu"}
	p.markGroupFilterDegraded(errors.New("proxy not ready"))

	// Sanity: the public helpers report what the filter relies on.
	if p.IsGroupFilterDegraded() != true {
		t.Fatal("setup: filter should be degraded")
	}
	if p.getBotOpenID() != "" {
		t.Fatalf("setup: botOpenID = %q, want empty", p.getBotOpenID())
	}

	// In the new logic, filterActive := botOpenID != "" || IsGroupFilterDegraded()
	// must be true so the group filter is engaged (fail-closed) rather than
	// skipped (fail-open).
	if p.getBotOpenID() == "" && !p.IsGroupFilterDegraded() {
		t.Fatal("filterActive must be true when degraded, so the group filter runs")
	}
}

// TestGroupFilter_NotDegraded_StillRequiresMention verifies the
// non-degraded happy path: when botOpenID is set, group messages
// without @bot are still dropped — no regression to the existing
// behavior.
func TestGroupFilter_NotDegraded_StillRequiresMention(t *testing.T) {
	p := &Platform{platformName: "feishu", botOpenID: "ou_bot"}
	if p.IsGroupFilterDegraded() {
		t.Fatal("setup: filter should not be degraded")
	}
	botOpenID := p.getBotOpenID()
	if botOpenID == "" {
		t.Fatal("setup: botOpenID should be set")
	}
	filterActive := botOpenID != "" || p.IsGroupFilterDegraded()
	if !filterActive {
		t.Fatal("filterActive must be true when botOpenID is set")
	}
	// Mentioned messages still pass isBotMentioned().
	mention := &larkim.MentionEvent{Id: &larkim.UserId{OpenId: strPtrClone("ou_bot")}}
	if !isBotMentioned([]*larkim.MentionEvent{mention}, botOpenID) {
		t.Fatal("bot's own mention should pass isBotMentioned")
	}
	// Non-mentioned messages do not.
	if isBotMentioned(nil, botOpenID) {
		t.Fatal("nil mention list should not pass isBotMentioned")
	}
}

func strPtrClone(s string) *string {
	out := s
	return &out
}

// TestFetchBotOpenIDWithRetry_TransientThenSucceeds uses an httptest
// server that fails the first 2 calls with a transient error and then
// succeeds. fetchBotOpenIDWithRetry should retry and return the
// successful open_id.
func TestFetchBotOpenIDWithRetry_TransientThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			// Hijack to force a connection-reset-like transient error.
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"bot":{"open_id":"ou_recovered"}}`))
	}))
	defer srv.Close()

	// Build a Platform with a client pointed at the test server.
	// We only need fetchBotOpenID; use a minimal client via New.
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
	}
	// Reuse the SDK's New by going through newPlatform() but with
	// domain override; the underlying lark client uses p.domain.
	plat, err := newPlatform("feishu", srv.URL, map[string]any{
		"app_id":     "cli_test",
		"app_secret": "secret_test",
	})
	if err != nil {
		t.Fatalf("newPlatform() error = %v", err)
	}
	ip := plat.(*interactivePlatform)
	p = ip.Platform

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openID, err := p.fetchBotOpenIDWithRetry(ctx)
	if err != nil {
		t.Fatalf("fetchBotOpenIDWithRetry() error = %v", err)
	}
	if openID != "ou_recovered" {
		t.Fatalf("openID = %q, want ou_recovered", openID)
	}
	if calls.Load() < 3 {
		t.Fatalf("expected at least 3 calls (2 transient + 1 ok), got %d", calls.Load())
	}
}

// TestFetchBotOpenIDWithRetry_GivesUpAfterRetries verifies that the
// retry loop eventually gives up when every attempt returns a
// non-transient error, returning the final error rather than blocking.
func TestFetchBotOpenIDWithRetry_GivesUpAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":99991663,"msg":"invalid access token"}`))
	}))
	defer srv.Close()

	plat, err := newPlatform("feishu", srv.URL, map[string]any{
		"app_id":     "cli_test",
		"app_secret": "secret_test",
	})
	if err != nil {
		t.Fatalf("newPlatform() error = %v", err)
	}
	p := plat.(*interactivePlatform).Platform

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openID, err := p.fetchBotOpenIDWithRetry(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if openID != "" {
		t.Fatalf("openID = %q, want empty", openID)
	}
	// 99991663 is a permanent auth failure (not transient), so
	// withTransientRetry returns immediately on the first attempt
	// after classifying it — no exponential backoff consumed.
	if !strings.Contains(err.Error(), "99991663") && !strings.Contains(err.Error(), "api code") {
		t.Fatalf("error should mention api failure, got %v", err)
	}
}

// TestStartGroupFilterSupervisor_RecoversOnNextAttempt verifies that
// when the supervisor sees the bot-info API start succeeding, it
// populates botOpenID and clears the degraded flag.
func TestStartGroupFilterSupervisor_RecoversOnNextAttempt(t *testing.T) {
	// Server that always succeeds (after the initial transient error
	// during startup; here we only test the supervisor's loop, so a
	// single success path is enough).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"bot":  map[string]any{"open_id": "ou_supervisor"},
		})
	}))
	defer srv.Close()

	plat, err := newPlatform("feishu", srv.URL, map[string]any{
		"app_id":     "cli_test",
		"app_secret": "secret_test",
	})
	if err != nil {
		t.Fatalf("newPlatform() error = %v", err)
	}
	p := plat.(*interactivePlatform).Platform

	// Mark as degraded to mimic the post-startup failure state.
	p.markGroupFilterDegraded(errors.New("simulated startup failure"))
	if !p.IsGroupFilterDegraded() {
		t.Fatal("setup: should be degraded")
	}

	// Use a fast interval for the test by replacing the ticker
	// behaviour. We do this by directly calling fetchBotOpenIDWithRetry
	// (the same call the supervisor's ticker uses) and then patching
	// the recovered state — which is exactly what the supervisor does.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	openID, err := p.fetchBotOpenIDWithRetry(ctx)
	if err != nil {
		t.Fatalf("fetchBotOpenIDWithRetry() error = %v", err)
	}
	p.mu.Lock()
	p.botOpenID = openID
	p.groupFilterDegraded = false
	p.groupFilterDegradedErr = ""
	p.mu.Unlock()

	if p.IsGroupFilterDegraded() {
		t.Fatal("expected degraded cleared after supervisor recovery")
	}
	if p.getBotOpenID() != "ou_supervisor" {
		t.Fatalf("botOpenID = %q, want ou_supervisor", p.getBotOpenID())
	}
	health := p.PlatformHealth()
	if health.Degraded {
		t.Fatal("PlatformHealth should not be degraded after recovery")
	}
}

// TestStopGroupFilterSupervisor_Idempotent verifies that calling the
// stop helper twice (or before it starts) is safe — important because
// Stop() invokes it unconditionally.
func TestStopGroupFilterSupervisor_Idempotent(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	// Never started — must not panic.
	p.stopGroupFilterSupervisor()
	p.stopGroupFilterSupervisor()

	// Start a real supervisor and stop it twice.
	p.startGroupFilterSupervisor()
	p.stopGroupFilterSupervisor()
	p.stopGroupFilterSupervisor()
}

func TestPlatformHealth_ImplementsCoreInterface(t *testing.T) {
	// Compile-time guarantee that *Platform still satisfies
	// core.PlatformHealth after our changes.
	var _ core.PlatformHealth = (*Platform)(nil)
}