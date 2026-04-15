package daxiangbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type wsClient struct {
	wsURL    string
	clientID string
	botID    int64

	mu   sync.Mutex
	conn *websocket.Conn

	send    chan BridgeFrame
	onFrame func(BridgeFrame)
	onReady func()

	pingInterval time.Duration
	minBackoff   time.Duration
	maxBackoff   time.Duration
}

func newWsClient(wsURL, clientID string, botID int64, onFrame func(BridgeFrame), onReady func()) *wsClient {
	return &wsClient{
		wsURL:        wsURL,
		clientID:     clientID,
		botID:        botID,
		send:         make(chan BridgeFrame, 64),
		onFrame:      onFrame,
		onReady:      onReady,
		pingInterval: 30 * time.Second,
		minBackoff:   3 * time.Second,
		maxBackoff:   60 * time.Second,
	}
}

func (c *wsClient) run(ctx context.Context) {
	backoff := c.minBackoff
	for {
		if err := c.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("daxiangbridge: connect failed, retrying", "error", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < c.maxBackoff {
				backoff *= 2
				if backoff > c.maxBackoff {
					backoff = c.maxBackoff
				}
			}
			continue
		}
		backoff = c.minBackoff
	}
}

func (c *wsClient) connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("daxiangbridge: dial %s: %w", c.wsURL, err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		conn.Close()
	}()

	if err := c.writeFrame(conn, buildRegisterFrame(c.clientID, c.botID)); err != nil {
		return err
	}

	readErr := make(chan error, 1)
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			var frame BridgeFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				slog.Warn("daxiangbridge: unmarshal frame", "error", err)
				continue
			}
			if frame.Type == FrameTypeClientRegistered {
				if c.onReady != nil {
					c.onReady()
				}
				continue
			}
			c.onFrame(frame)
		}
	}()

	ping := time.NewTicker(c.pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		case frame := <-c.send:
			if err := c.writeFrame(conn, frame); err != nil {
				return err
			}
		case <-ping.C:
			if err := c.writeFrame(conn, buildPingFrame()); err != nil {
				return err
			}
		case err := <-readErr:
			return err
		}
	}
}

func (c *wsClient) Send(frame BridgeFrame) {
	select {
	case c.send <- frame:
	default:
		slog.Warn("daxiangbridge: send buffer full, dropping frame", "type", frame.Type)
	}
}

func (c *wsClient) writeFrame(conn *websocket.Conn, frame BridgeFrame) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, b)
}
