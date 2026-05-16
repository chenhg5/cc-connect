package youzone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestNewWebSocketDialerDisablesProxy locks in the direct-connect
// semantic for YouZone WebSocket dialing. If a future change replaces
// the explicit Proxy: nil with websocket.DefaultDialer (which uses
// http.ProxyFromEnvironment), this test fails — preventing a regression
// where the long-lived ws connection silently starts going through the
// user's HTTPS proxy.
func TestNewWebSocketDialerDisablesProxy(t *testing.T) {
	cfg := config{
		pingInterval:       time.Hour,
		heartbeatMode:      heartbeatWSPing,
		websocketProtocols: []string{"v10.stomp"},
	}
	d := newWebSocketDialer(cfg)
	if d.Proxy != nil {
		t.Fatalf("WebSocket dialer must have Proxy=nil for direct connection; got %T", d.Proxy)
	}
	if d.HandshakeTimeout != 15*time.Second {
		t.Errorf("HandshakeTimeout = %v, want 15s", d.HandshakeTimeout)
	}
	if len(d.Subprotocols) != 1 || d.Subprotocols[0] != "v10.stomp" {
		t.Errorf("Subprotocols = %+v", d.Subprotocols)
	}
}

// TestRunWebSocketLogsDisconnectedLifecycle verifies that the warn at
// disconnect carries the fields an operator needs to align a user's "no
// response" report with the connection lifecycle: connected_for, last_frame_at
// (the last successful ReadMessage timestamp), attempt, and the dial error.
// Without these we cannot tell whether the user's message fell in the offline
// gap.
func TestRunWebSocketLogsDisconnectedLifecycle(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send one business frame so lastFrameAt advances, then close so the
		// client's read loop sees a real disconnect.
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"id":"m1","type":"dmessage","sender":"u1","content":"hi"}`))
		time.Sleep(50 * time.Millisecond)
		_ = c.Close()
	}))
	defer srv.Close()
	wssURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	p := &Platform{}
	p.cfg.pingInterval = time.Hour
	p.cfg.heartbeatMode = heartbeatXMPPWhitespace

	cap := captureLogs(t, func() {
		// ctx is never cancelled — we want runWebSocket to observe a server-
		// side close and emit the disconnect warn (ctx.Err() == nil path).
		_ = p.runWebSocket(context.Background(), "robot-9", wssURL, 3)
	})

	r, ok := cap.findByMessage("youzone: websocket disconnected")
	if !ok {
		t.Fatal("disconnected log not emitted")
	}
	a := attrs(r)
	if a["robot_id"] != "robot-9" {
		t.Errorf("robot_id = %v", a["robot_id"])
	}
	if a["attempt"] != int64(3) && a["attempt"] != 3 {
		t.Errorf("attempt = %v, want 3", a["attempt"])
	}
	if _, present := a["connected_for"]; !present {
		t.Error("connected_for missing")
	}
	if _, present := a["last_frame_at"]; !present {
		t.Error("last_frame_at missing (server sent one frame, must surface)")
	}
	if a["err"] == nil {
		t.Error("err missing")
	}
}

// TestRunWebSocketCtxCancelSuppressesDisconnectWarn locks in the rule that
// Stop()/reload — both cancel ctx — must not produce a disconnected warn,
// since the disconnect is by design. Operators rely on the warn being a real
// signal of an anomalous disconnect.
func TestRunWebSocketCtxCancelSuppressesDisconnectWarn(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wssURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	p := &Platform{}
	p.cfg.pingInterval = time.Hour
	p.cfg.heartbeatMode = heartbeatXMPPWhitespace

	cap := captureLogs(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.runWebSocket(ctx, "robot-1", wssURL, 0) }()
		time.Sleep(50 * time.Millisecond) // let dial complete
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("runWebSocket did not return after context cancel")
		}
	})

	if _, ok := cap.findByMessage("youzone: websocket disconnected"); ok {
		t.Error("disconnected warn must be suppressed when ctx was cancelled (Stop/reload path)")
	}
}

// TestRunWebSocketReturnsOnContextCancel guards the Stop()/reload path: gorilla's
// conn.ReadMessage() ignores context cancellation, so runWebSocket must close the
// socket itself when the context is cancelled, otherwise the read goroutine and
// TCP connection leak until the server happens to disconnect.
func TestRunWebSocketReturnsOnContextCancel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	connected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		close(connected)
		// Hold the connection open and never send a business frame: the only
		// way the client's read loop ends is the client closing the socket.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wssURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	p := &Platform{}
	p.cfg.pingInterval = time.Hour // never fires during the test
	p.cfg.heartbeatMode = heartbeatXMPPWhitespace

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.runWebSocket(ctx, "robot-1", wssURL, 0) }()

	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("websocket did not connect")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runWebSocket did not return after context cancel")
	}
}
