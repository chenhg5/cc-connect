package youzone

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
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
		if err := p.runWebSocket(ctx, robotID, wss); err != nil && ctx.Err() == nil {
			slog.Warn("youzone: websocket disconnected", "err", err)
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

func (p *Platform) runWebSocket(ctx context.Context, robotID, wss string) error {
	dialer := newWebSocketDialer(p.cfg)
	header := http.Header{}
	header.Set("User-Agent", "cc-connect-youzone/0.1")
	conn, _, err := dialer.DialContext(ctx, wss, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	slog.Info("youzone: websocket connected", "robot_id", robotID, "protocol", conn.Subprotocol())

	connCtx, cancel := context.WithCancel(ctx)
	var writeMu sync.Mutex
	var wg sync.WaitGroup

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
		p.handleInbound(data)
	}

	cancel() // stop pingLoop and trigger the close watcher (no-op if Stop already did)
	_ = conn.Close()
	wg.Wait()
	return readErr
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
