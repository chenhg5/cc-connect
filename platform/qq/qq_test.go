package qq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/gorilla/websocket"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "qq" {
		t.Errorf("Name() = %q, want %q", got, "qq")
	}
}

func TestNew_DefaultWSURL(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://127.0.0.1:3001" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://127.0.0.1:3001")
	}
}

func TestNew_CustomWSURL(t *testing.T) {
	p, err := New(map[string]any{
		"ws_url": "ws://example.com:8080",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://example.com:8080" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://example.com:8080")
	}
}

func TestNew_WithToken(t *testing.T) {
	p, err := New(map[string]any{
		"token": "my-secret-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.token != "my-secret-token" {
		t.Errorf("token = %q, want %q", platform.token, "my-secret-token")
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"allow_from": "user1,user2,*",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2,*" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2,*")
	}
}

func TestNew_ShareSessionInChannel(t *testing.T) {
	p, err := New(map[string]any{
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.shareSessionInChannel {
		t.Error("shareSessionInChannel = false, want true")
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)

// TestStart_FetchesSelfIDWithoutTimeout verifies that Start() completes
// promptly with selfID populated from the get_login_info OneBot API call.
// Regression for a bug where Start invoked callAPI BEFORE launching readLoop,
// so the API response had no consumer and callAPI always timed out after 15s
// — leaving selfID=0 and disabling the self-message filter in handleMessage.
func TestStart_FetchesSelfIDWithoutTimeout(t *testing.T) {
	const botUserID = 999999

	upgrader := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			if req["action"] == "get_login_info" {
				echo, _ := req["echo"].(string)
				resp := map[string]any{
					"status":  "ok",
					"retcode": 0,
					"echo":    echo,
					"data":    map[string]any{"user_id": botUserID, "nickname": "TestBot"},
				}
				raw, _ := json.Marshal(resp)
				_ = c.WriteMessage(websocket.TextMessage, raw)
			}
		}
	}))
	defer ts.Close()

	p := &Platform{
		wsURL: "ws" + strings.TrimPrefix(ts.URL, "http"),
	}

	done := make(chan error, 1)
	go func() {
		done <- p.Start(func(core.Platform, *core.Message) {})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = p.Stop()
		t.Fatal("Start did not complete within 5s; readLoop likely starts after callAPI, so get_login_info never gets a response")
	}
	defer p.Stop()

	if p.selfID != botUserID {
		t.Errorf("selfID = %d, want %d (self-message filter would be disabled)", p.selfID, botUserID)
	}
}

// napcatServer builds a mock NapCat exposing both WS (for receive + default
// send) and HTTP (for HTTP send fallback). It records how many times each
// surface was called so tests can assert which path Send() took.
type napcatServer struct {
	ts        *httptest.Server
	wsSends   atomic.Int32 // send_group_msg / send_private_msg over WS
	httpSends atomic.Int32 // send_group_msg / send_private_msg over HTTP
}

func newNapcatServer(t *testing.T) *napcatServer {
	t.Helper()
	ns := &napcatServer{}
	upgrader := websocket.Upgrader{}
	ns.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				_, msg, err := c.ReadMessage()
				if err != nil {
					return
				}
				var req map[string]any
				if err := json.Unmarshal(msg, &req); err != nil {
					continue
				}
				action, _ := req["action"].(string)
				switch action {
				case "get_login_info":
					echo, _ := req["echo"].(string)
					resp := map[string]any{
						"status":  "ok",
						"retcode": 0,
						"echo":    echo,
						"data":    map[string]any{"user_id": 999999, "nickname": "TestBot"},
					}
					raw, _ := json.Marshal(resp)
					_ = c.WriteMessage(websocket.TextMessage, raw)
				case "send_group_msg", "send_private_msg":
					ns.wsSends.Add(1)
					echo, _ := req["echo"].(string)
					resp := map[string]any{
						"status":  "ok",
						"retcode": 0,
						"echo":    echo,
						"data":    map[string]any{"message_id": 12345},
					}
					raw, _ := json.Marshal(resp)
					_ = c.WriteMessage(websocket.TextMessage, raw)
				}
			}
		}

		// HTTP API surface for Send() HTTP fallback.
		body, _ := io.ReadAll(r.Body)
		var params map[string]any
		_ = json.Unmarshal(body, &params)

		// Echo path on HTTP root for testing error formatting.
		_ = params

		ns.httpSends.Add(1)
		resp := map[string]any{
			"status":  "ok",
			"retcode": 0,
			"data":    map[string]any{"message_id": 67890},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ns.ts.Close)
	return ns
}

func startPlatform(t *testing.T, ns *napcatServer, opts map[string]any) *Platform {
	t.Helper()
	if opts == nil {
		opts = map[string]any{}
	}
	opts["ws_url"] = "ws" + strings.TrimPrefix(ns.ts.URL, "http")
	if _, ok := opts["http_url"]; !ok {
		opts["http_url"] = ns.ts.URL
	}
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	platform := p.(*Platform)
	if err := platform.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = platform.Stop() })
	return platform
}

func TestSend_ShortMessageUsesWebSocket(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, nil)

	rctx := &replyContext{messageType: "group", groupID: 12345}
	if err := p.Send(context.Background(), rctx, "short message"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.wsSends.Load(); got != 1 {
		t.Errorf("wsSends = %d, want 1 (short message must go via WS)", got)
	}
	if got := ns.httpSends.Load(); got != 0 {
		t.Errorf("httpSends = %d, want 0 (short message must NOT hit HTTP)", got)
	}
}

func TestSend_LongMessageFallbackHTTP(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, nil)

	rctx := &replyContext{messageType: "group", groupID: 12345}
	// Reporter's reproducer: ~2500 byte message would always hit the 15s WS
	// timeout; must now dispatch through HTTP.
	long := strings.Repeat("a", 2500)
	if err := p.Send(context.Background(), rctx, long); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.httpSends.Load(); got != 1 {
		t.Errorf("httpSends = %d, want 1 (long message must go via HTTP)", got)
	}
	if got := ns.wsSends.Load(); got != 0 {
		t.Errorf("wsSends = %d, want 0 (long message must NOT hit WS)", got)
	}
}

func TestSend_LongPrivateMessageFallbackHTTP(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, nil)

	rctx := &replyContext{messageType: "private", userID: 99999}
	long := strings.Repeat("b", 3000)
	if err := p.Send(context.Background(), rctx, long); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.httpSends.Load(); got != 1 {
		t.Errorf("httpSends = %d, want 1", got)
	}
	if got := ns.wsSends.Load(); got != 0 {
		t.Errorf("wsSends = %d, want 0", got)
	}
}

func TestSend_AtThresholdUsesWebSocket(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, nil)

	rctx := &replyContext{messageType: "group", groupID: 12345}
	// Boundary: exactly at threshold → still WS (> not >=).
	if err := p.Send(context.Background(), rctx, strings.Repeat("c", 512)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.wsSends.Load(); got != 1 {
		t.Errorf("wsSends = %d, want 1 at threshold", got)
	}
	if got := ns.httpSends.Load(); got != 0 {
		t.Errorf("httpSends = %d, want 0 at threshold", got)
	}
}

func TestSend_ModeAlwaysUsesHTTP(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, map[string]any{"send_via_http": "always"})

	rctx := &replyContext{messageType: "group", groupID: 12345}
	if err := p.Send(context.Background(), rctx, "x"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.httpSends.Load(); got != 1 {
		t.Errorf("httpSends = %d, want 1", got)
	}
	if got := ns.wsSends.Load(); got != 0 {
		t.Errorf("wsSends = %d, want 0", got)
	}
}

func TestSend_ModeNeverUsesWebSocket(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, map[string]any{"send_via_http": "never"})

	rctx := &replyContext{messageType: "group", groupID: 12345}
	long := strings.Repeat("d", 2500)
	if err := p.Send(context.Background(), rctx, long); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.wsSends.Load(); got != 1 {
		t.Errorf("wsSends = %d, want 1 (never mode must use WS even for long)", got)
	}
	if got := ns.httpSends.Load(); got != 0 {
		t.Errorf("httpSends = %d, want 0", got)
	}
}

func TestSend_AutoNoHTTPURLFallsBackToWebSocket(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, map[string]any{"http_url": ""})

	rctx := &replyContext{messageType: "group", groupID: 12345}
	long := strings.Repeat("e", 2500)
	if err := p.Send(context.Background(), rctx, long); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.wsSends.Load(); got != 1 {
		t.Errorf("wsSends = %d, want 1 (no http_url → must use WS)", got)
	}
	if got := ns.httpSends.Load(); got != 0 {
		t.Errorf("httpSends = %d, want 0", got)
	}
}

func TestSend_ModeAlwaysNoHTTPURLGracefullyFallsBackToWS(t *testing.T) {
	ns := newNapcatServer(t)
	p := startPlatform(t, ns, map[string]any{"send_via_http": "always", "http_url": ""})

	rctx := &replyContext{messageType: "group", groupID: 12345}
	// With send_via_http=always but no http_url configured, must fall back to
	// WebSocket instead of failing — better to deliver via the available path
	// than surface a config error every send. Matches SendFile() behavior.
	if err := p.Send(context.Background(), rctx, "x"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := ns.wsSends.Load(); got != 1 {
		t.Errorf("wsSends = %d, want 1 (no http_url → must fall back to WS)", got)
	}
	if got := ns.httpSends.Load(); got != 0 {
		t.Errorf("httpSends = %d, want 0", got)
	}
}

func TestNew_InvalidSendViaHTTPFallsBackToAuto(t *testing.T) {
	p, err := New(map[string]any{"send_via_http": "bogus"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	platform := p.(*Platform)
	if platform.sendViaHTTP != "auto" {
		t.Errorf("sendViaHTTP = %q, want auto", platform.sendViaHTTP)
	}
}

func TestShouldUseHTTP(t *testing.T) {
	cases := []struct {
		name     string
		sendMode string
		httpURL  string
		len      int
		wantHTTP bool
	}{
		{"auto_short_under_threshold", "auto", "http://x", 100, false},
		{"auto_long_over_threshold", "auto", "http://x", 513, true},
		{"auto_long_no_http_url", "auto", "", 5000, false},
		{"always_short_with_http", "always", "http://x", 10, true},
		{"always_short_no_http", "always", "", 10, false},
		{"never_long", "never", "http://x", 5000, false},
		{"default_empty_mode_long", "", "http://x", 1000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Platform{sendViaHTTP: c.sendMode, httpURL: c.httpURL}
			if c.sendMode == "" {
				// New() normalizes "" → "auto"; mimic that here.
				p.sendViaHTTP = "auto"
			}
			got := p.shouldUseHTTP(strings.Repeat("x", c.len))
			if got != c.wantHTTP {
				t.Errorf("shouldUseHTTP(%d) = %v, want %v", c.len, got, c.wantHTTP)
			}
		})
	}
}
