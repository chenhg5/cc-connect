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
	go func() { done <- p.runWebSocket(ctx, "robot-1", wssURL) }()

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
