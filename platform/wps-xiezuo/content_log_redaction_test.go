package wpsxiezuo

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type protectedLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *protectedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *protectedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestWebSocketLogsOmitRawFrame(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"unknown":"`+marker+`"}`)); err != nil {
			t.Errorf("write frame: %v", err)
		}
	}))
	defer server.Close()
	oldEndpoint := wsEndpoint
	wsEndpoint = "ws" + strings.TrimPrefix(server.URL, "http")
	t.Cleanup(func() { wsEndpoint = oldEndpoint })
	var logs protectedLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{appID: "AK", appSecret: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.runConnection(ctx)
	if strings.Contains(logs.String(), marker) {
		t.Fatal("WPS Xiezuo log exposed raw WebSocket frame")
	}
}
