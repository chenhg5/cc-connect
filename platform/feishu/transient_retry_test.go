package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// ─── isTransientError unit tests ───────────────────────────────────────────

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "connection reset by peer", err: fmt.Errorf("write tcp: connection reset by peer"), want: true},
		{name: "broken pipe", err: fmt.Errorf("write: broken pipe"), want: true},
		{name: "i/o timeout", err: fmt.Errorf("dial tcp: i/o timeout"), want: true},
		{name: "TLS handshake timeout", err: fmt.Errorf("net/http: TLS handshake timeout"), want: true},
		{name: "connection refused", err: fmt.Errorf("dial tcp 127.0.0.1:443: connection refused"), want: true},
		{name: "no such host (permanent)", err: fmt.Errorf("dial tcp: lookup example.invalid: no such host"), want: false},
		{name: "server misbehaving", err: fmt.Errorf("lookup example.com: server misbehaving"), want: true},
		{name: "EOF", err: io.EOF, want: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped EOF", err: fmt.Errorf("feishu: reply api call: %w", io.EOF), want: true},
		{name: "syscall ECONNRESET", err: fmt.Errorf("write tcp: %w", syscall.ECONNRESET), want: true},
		{name: "syscall EPIPE", err: fmt.Errorf("write tcp: %w", syscall.EPIPE), want: true},
		{name: "rate limited", err: fmt.Errorf("feishu: reply failed code=230001 msg=rate limited"), want: false},
		{name: "invalid token", err: fmt.Errorf("feishu: reply failed code=99991663"), want: false},
		{name: "generic error", err: fmt.Errorf("something went wrong"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientError(tt.err); got != tt.want {
				t.Fatalf("isTransientError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsTransientError_NetTimeout(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: &timeoutError{}}
	if !isTransientError(err) {
		t.Fatal("expected net timeout to be transient")
	}
}

type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// ─── withTransientRetry unit tests ─────────────────────────────────────────

func TestWithTransientRetry_SucceedsFirstAttempt(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	calls := 0
	err := p.withTransientRetry(context.Background(), "test", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestWithTransientRetry_RetriesOnTransientThenSucceeds(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	calls := 0
	err := p.withTransientRetry(context.Background(), "test", func() error {
		calls++
		if calls <= 2 {
			return fmt.Errorf("write tcp: connection reset by peer")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestWithTransientRetry_DoesNotRetryNonTransient(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	calls := 0
	err := p.withTransientRetry(context.Background(), "test", func() error {
		calls++
		return fmt.Errorf("feishu: reply failed code=230001 msg=rate limited")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry for non-transient errors)", calls)
	}
}

func TestWithTransientRetry_GivesUpAfterMaxRetries(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	calls := 0
	err := p.withTransientRetry(context.Background(), "test", func() error {
		calls++
		return io.EOF
	})
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	// 1 initial + 3 retries = 4 total calls
	if calls != maxTransientRetries+1 {
		t.Fatalf("calls = %d, want %d", calls, maxTransientRetries+1)
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Fatalf("error = %q, want 'failed after' message", err.Error())
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error should wrap io.EOF, got %v", err)
	}
}

func TestWithTransientRetry_RespectsContextCancellation(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// Cancel after first call to trigger cancellation during backoff wait
	err := p.withTransientRetry(ctx, "test", func() error {
		calls++
		if calls == 1 {
			cancel()
		}
		return fmt.Errorf("connection reset by peer")
	})
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
	if !strings.Contains(err.Error(), "retry cancelled") {
		t.Fatalf("error = %q, want retry cancelled message", err.Error())
	}
}

// ─── Integration tests: transient retry with Feishu API ────────────────────

func TestReplyRetriesOnTransientNetworkError(t *testing.T) {
	const appID = "cli_transient_retry"
	const appSecret = "secret"

	var replyCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "valid-token",
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			call := replyCalls.Add(1)
			if call <= 2 {
				// Simulate transient error by closing connection abruptly
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatalf("response writer does not support hijacking")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack failed: %v", err)
				}
				conn.Close()
				return
			}
			// 3rd call succeeds
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"message_id": "om_reply_ok"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.Reply(context.Background(), replyContext{messageID: "om_root", chatID: "oc_chat"}, "hello")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if got := replyCalls.Load(); got < 3 {
		t.Fatalf("replyCalls = %d, want >= 3 (initial + retries)", got)
	}
}

func TestCreateMessageRetriesOnTransientNetworkError(t *testing.T) {
	const appID = "cli_transient_create"
	const appSecret = "secret"

	var createCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "valid-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages":
			call := createCalls.Add(1)
			if call == 1 {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatalf("response writer does not support hijacking")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack failed: %v", err)
				}
				conn.Close()
				return
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"message_id": "om_create_ok"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.sendNewMessageToChat(context.Background(), replyContext{chatID: "oc_chat"}, "text", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("sendNewMessageToChat() error = %v", err)
	}
	if got := createCalls.Load(); got < 2 {
		t.Fatalf("createCalls = %d, want >= 2 (initial + retry)", got)
	}
}

func TestReplyDoesNotRetryOnNonTransientAPIError(t *testing.T) {
	const appID = "cli_no_transient_retry"
	const appSecret = "secret"

	var replyCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "valid-token",
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			replyCalls.Add(1)
			// Return a non-transient API error (rate limit)
			writeJSON(t, w, map[string]any{
				"code": 230001,
				"msg":  "send too fast, please retry later",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.Reply(context.Background(), replyContext{messageID: "om_root", chatID: "oc_chat"}, "hello")
	if err == nil {
		t.Fatal("Reply() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "send too fast") {
		t.Fatalf("Reply() error = %v, want rate limited error", err)
	}
	// Should only make 1 attempt (no retry on API-level errors)
	if got := replyCalls.Load(); got != 1 {
		t.Fatalf("replyCalls = %d, want 1 (no retry)", got)
	}
}

func TestPatchMessageRetriesOnTransientError(t *testing.T) {
	const appID = "cli_patch_retry"
	const appSecret = "secret"

	var patchCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "valid-token",
			})
		case strings.Contains(r.URL.Path, "/messages/") && r.Method == http.MethodPatch:
			call := patchCalls.Add(1)
			if call == 1 {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatalf("response writer does not support hijacking")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack failed: %v", err)
				}
				conn.Close()
				return
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
			})
		default:
			// Allow other paths (e.g. token fetch)
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.UpdateMessage(context.Background(), &feishuPreviewHandle{messageID: "om_card_1", chatID: "oc_chat"}, "updated content")
	if err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}
	if got := patchCalls.Load(); got < 2 {
		t.Fatalf("patchCalls = %d, want >= 2", got)
	}
}

// ─── Test: transient retry + token refresh work together ───────────────────

func TestReplyTransientRetryThenTokenRefresh(t *testing.T) {
	// Scenario: first call gets connection reset (transient), retry gets
	// invalid token error, which triggers token refresh, then succeeds.
	const appID = "cli_combined_retry"
	const appSecret = "secret"

	var authCalls, replyCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			authCalls.Add(1)
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "fresh-token",
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			call := replyCalls.Add(1)
			switch call {
			case 1:
				// First attempt: transient error
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatalf("hijack not supported")
				}
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			case 2:
				// Second attempt (after transient retry): invalid token
				writeJSON(t, w, map[string]any{
					"code": 99991663,
					"msg":  "Invalid access token for authorization",
				})
			case 3:
				// Third attempt (after token refresh): success
				writeJSON(t, w, map[string]any{
					"code": 0,
					"msg":  "success",
					"data": map[string]any{"message_id": "om_reply_ok"},
				})
			default:
				t.Fatalf("unexpected reply call %d", call)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.Reply(context.Background(), replyContext{messageID: "om_root", chatID: "oc_chat"}, "hello")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if got := replyCalls.Load(); got != 3 {
		t.Fatalf("replyCalls = %d, want 3", got)
	}
}

// ─── Timing test: verify backoff delay is reasonable ───────────────────────

func TestWithTransientRetry_BackoffTiming(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	start := time.Now()
	calls := 0
	_ = p.withTransientRetry(context.Background(), "test", func() error {
		calls++
		if calls <= 2 {
			return io.EOF
		}
		return nil
	})
	elapsed := time.Since(start)
	// 2 retries: base delays 500ms + 1000ms = ~1500ms, plus up to 25% jitter each.
	// Allow generous margin for CI environments.
	if elapsed < 500*time.Millisecond {
		t.Fatalf("elapsed = %v, expected at least 500ms of backoff", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("elapsed = %v, backoff took unreasonably long", elapsed)
	}
}
