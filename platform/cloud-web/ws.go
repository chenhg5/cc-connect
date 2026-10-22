package cloudweb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsPingInterval = 25 * time.Second
	wsPongWait     = 90 * time.Second
	wsReconnectMin = 2 * time.Second
	wsReconnectMax = 30 * time.Second
)

type wsTransport struct {
	wsURL   string
	token   string
	name    string
	project string

	mu        sync.RWMutex
	caps      map[string]bool
	conn      *websocket.Conn
	writeMu   sync.Mutex
	cancel    context.CancelFunc
	onInbound      inboundHandler
	onConnected    func()
	onDisconnected func(error)

	previewMu       sync.Mutex
	previewRequests map[string]chan string
}

func newWSTransport(wsURL, token, name, project string) *wsTransport {
	return &wsTransport{
		wsURL:           wsURL,
		token:           token,
		name:            name,
		project:         project,
		caps:            defaultCapabilities(),
		previewRequests: make(map[string]chan string),
	}
}

func (t *wsTransport) Capabilities() map[string]bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneCapabilities(t.caps)
}

func (t *wsTransport) setCaps(caps map[string]bool) {
	t.mu.Lock()
	t.caps = caps
	t.mu.Unlock()
}

func (t *wsTransport) Start(ctx context.Context, onInbound inboundHandler) error {
	t.onInbound = onInbound
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.connectLoop(runCtx)
	return nil
}

func (t *wsTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Lock()
	conn := t.conn
	t.conn = nil
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func (t *wsTransport) connectLoop(ctx context.Context) {
	delay := wsReconnectMin
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := t.connectOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("cloud_web: websocket connect failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < wsReconnectMax {
			delay *= 2
			if delay > wsReconnectMax {
				delay = wsReconnectMax
			}
		}
	}
}

func (t *wsTransport) connectOnce(ctx context.Context) error {
	u, err := url.Parse(t.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	if t.token != "" {
		q := u.Query()
		if q.Get("token") == "" {
			q.Set("token", t.token)
			u.RawQuery = q.Encode()
		}
	}

	header := http.Header{}
	if t.token != "" {
		header.Set("Authorization", "Bearer "+t.token)
		header.Set("X-Cloud-Web-Token", t.token)
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return err
	}

	reg, err := json.Marshal(buildRegisterPayload(t.name, t.project, "websocket"))
	if err != nil {
		_ = conn.Close()
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, reg); err != nil {
		_ = conn.Close()
		return err
	}

	if err := conn.SetReadDeadline(time.Now().Add(wsPongWait)); err != nil {
		_ = conn.Close()
		return err
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read register_ack: %w", err)
	}
	caps, err := parseRegisterAck(raw)
	if err != nil {
		_ = conn.Close()
		return err
	}
	t.setCaps(caps)

	t.mu.Lock()
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = conn
	t.mu.Unlock()

	slog.Info("cloud_web: websocket connected", "url", t.wsURL)

	connected := true
	defer func() {
		if connected && t.onDisconnected != nil {
			t.onDisconnected(fmt.Errorf("cloud_web: websocket disconnected"))
		}
	}()
	if t.onConnected != nil {
		t.onConnected()
	}

	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				t.writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			<-pingDone
			return nil
		default:
		}
		if err := conn.SetReadDeadline(time.Now().Add(wsPongWait)); err != nil {
			_ = conn.Close()
			<-pingDone
			return err
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			<-pingDone
			return err
		}
		t.dispatch(raw)
	}
}

func (t *wsTransport) dispatch(raw []byte) {
	var base wireMsg
	if err := json.Unmarshal(raw, &base); err != nil {
		slog.Debug("cloud_web: invalid ws json", "error", err)
		return
	}
	switch base.Type {
	case "preview_ack":
		var ack wirePreviewAck
		if err := json.Unmarshal(raw, &ack); err != nil {
			return
		}
		t.previewMu.Lock()
		ch, ok := t.previewRequests[ack.RefID]
		if ok {
			delete(t.previewRequests, ack.RefID)
		}
		t.previewMu.Unlock()
		if ok {
			ch <- ack.PreviewHandle
		}
	case "pong", "register_ack", "capabilities_changed":
		if base.Type == "capabilities_changed" {
			var ch wireCapabilitiesChanged
			if err := json.Unmarshal(raw, &ch); err == nil && len(ch.Capabilities) > 0 {
				t.setCaps(capabilitySet(ch.Capabilities))
			}
		}
	default:
		if t.onInbound != nil {
			t.onInbound(raw)
		}
	}
}

func (t *wsTransport) Send(ctx context.Context, msg map[string]any) error {
	t.mu.RLock()
	conn := t.conn
	t.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("cloud_web: websocket not connected")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return conn.WriteJSON(msg)
}

func (t *wsTransport) waitPreviewAck(refID string, timeout time.Duration) (string, error) {
	ch := make(chan string, 1)
	t.previewMu.Lock()
	t.previewRequests[refID] = ch
	t.previewMu.Unlock()
	defer func() {
		t.previewMu.Lock()
		delete(t.previewRequests, refID)
		t.previewMu.Unlock()
	}()
	select {
	case handle := <-ch:
		return handle, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("cloud_web: preview_ack timeout")
	}
}

func resolveWSURL(baseURL, wsURL string) string {
	if strings.TrimSpace(wsURL) != "" {
		return wsURL
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://") + defaultWSPath
	}
	if strings.HasPrefix(base, "http://") {
		return "ws://" + strings.TrimPrefix(base, "http://") + defaultWSPath
	}
	if strings.Contains(base, "://") {
		return base + defaultWSPath
	}
	return "ws://" + base + defaultWSPath
}
