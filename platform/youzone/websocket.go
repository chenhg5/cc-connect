package youzone

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type wsConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	Close() error
}

func (p *Platform) connectLoop(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		robotID, err := p.resolveRobotID(ctx)
		if err != nil {
			slog.Error("youzone: resolve robot failed", "err", err)
			p.sleepReconnect(ctx, attempt)
			attempt++
			continue
		}
		wss, err := p.client.getWSS(ctx, robotID)
		if err != nil {
			slog.Error("youzone: get wss failed", "err", err)
			p.sleepReconnect(ctx, attempt)
			attempt++
			continue
		}
		// runWebSocket is the only layer that knows connectedAt / lastFrameAt,
		// so it owns the disconnected log. We deliberately do not emit a second
		// "websocket disconnected" warn here — that would double-print and lose
		// the lifecycle context.
		if err := p.runWebSocket(ctx, robotID, wss, attempt); err != nil && ctx.Err() == nil {
			p.sleepReconnect(ctx, attempt)
			attempt++
			continue
		}
		attempt = 0
	}
}

// newWebSocketDialer builds the gorilla websocket dialer for YouZone
// connections. We pin Proxy: nil so the long-lived ws connection always
// goes direct, regardless of the user's HTTP/HTTPS proxy env. HTTP-side
// requests (sendMessage, getWss, listRobots) continue to honor
// http.ProxyFromEnvironment via the default transport — see client.go.
func newWebSocketDialer(cfg config) websocket.Dialer {
	return websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Subprotocols:     cfg.websocketProtocols,
		Proxy:            nil,
	}
}

func (p *Platform) runWebSocket(ctx context.Context, robotID, wss string, attempt int) error {
	dialer := newWebSocketDialer(p.cfg)
	header := http.Header{}
	header.Set("User-Agent", "cc-connect-youzone/0.1")
	conn, _, err := dialer.DialContext(ctx, wss, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	connectedAt := time.Now()
	slog.Info("youzone: websocket connected",
		"robot_id", robotID,
		"protocol", conn.Subprotocol(),
		"attempt", attempt,
	)

	connCtx, cancel := context.WithCancel(ctx)
	var writeMu sync.Mutex
	var wg sync.WaitGroup

	// lastFrameAt is updated on each successful ReadMessage. It is used in the
	// disconnected log so an operator can align "user reported sending at T"
	// with "last frame we got was at T-30s" → message landed in the offline
	// gap.
	var lastFrameAtUnix atomic.Int64

	wg.Add(1)
	go func() {
		defer wg.Done()
		p.pingLoop(connCtx, conn, &writeMu)
	}()

	// gorilla's conn.ReadMessage() ignores context cancellation, so the read
	// loop below cannot be interrupted by Stop()/reload on its own. This
	// watcher closes the socket when connCtx is cancelled (Stop, or the read
	// loop exiting via cancel() below), which makes the blocked ReadMessage
	// return promptly instead of leaking the goroutine and TCP connection until
	// the server happens to disconnect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-connCtx.Done()
		writeMu.Lock()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stop"), time.Now().Add(time.Second))
		writeMu.Unlock()
		_ = conn.Close()
	}()

	var readErr error
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			readErr = err
			break
		}
		lastFrameAtUnix.Store(time.Now().UnixNano())
		p.handleInbound(data)
	}

	cancel() // stop pingLoop and trigger the close watcher (no-op if Stop already did)
	_ = conn.Close()
	wg.Wait()

	// Only treat the disconnect as anomalous when our context is still live.
	// Stop()/reload/parent-cancel paths cancel ctx first and must not produce
	// a warn — the ws closing on those paths is by design.
	if ctx.Err() == nil {
		fields := []any{
			"robot_id", robotID,
			"err", errString(readErr),
			"connected_for", time.Since(connectedAt),
			"attempt", attempt,
		}
		if ns := lastFrameAtUnix.Load(); ns > 0 {
			fields = append(fields, "last_frame_at", time.Unix(0, ns).Format(time.RFC3339Nano))
		}
		slog.Warn("youzone: websocket disconnected", fields...)
	}
	return readErr
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (p *Platform) pingLoop(ctx context.Context, conn wsConn, writeMu *sync.Mutex) {
	t := time.NewTicker(p.cfg.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			writeMu.Lock()
			var err error
			if p.cfg.heartbeatMode == heartbeatWSPing {
				err = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			} else {
				err = conn.WriteMessage(websocket.TextMessage, []byte(" "))
			}
			writeMu.Unlock()
			if err != nil {
				slog.Warn("youzone: heartbeat failed", "err", err)
				return
			}
		}
	}
}

func (p *Platform) sleepReconnect(ctx context.Context, attempt int) {
	delays := p.cfg.reconnectDelays
	if len(delays) == 0 {
		delays = []time.Duration{time.Second}
	}
	if attempt >= len(delays) {
		attempt = len(delays) - 1
	}
	delay := delays[attempt]
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
