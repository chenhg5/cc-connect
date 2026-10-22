package cloudweb

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type gatewayTransport struct {
	baseURL     string
	token       string
	name        string
	project     string
	listen      string
	webhookPath string
	registerURL string
	publicURL   string
	sendPath    string

	mu        sync.RWMutex
	caps      map[string]bool
	client    *http.Client
	server    *http.Server
	listener  net.Listener
	cancel    context.CancelFunc
	onInbound inboundHandler

	previewMu       sync.Mutex
	previewRequests map[string]chan string
}

func newGatewayTransport(baseURL, token, name, project, listen, webhookPath, registerURL, publicURL, sendPath string) *gatewayTransport {
	if listen == "" {
		listen = ":8099"
	}
	if webhookPath == "" {
		webhookPath = "/cloud-web/webhook"
	} else if !strings.HasPrefix(webhookPath, "/") {
		webhookPath = "/" + webhookPath
	}
	if sendPath == "" {
		sendPath = defaultSend
	}
	return &gatewayTransport{
		baseURL:         strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:           token,
		name:            name,
		project:         project,
		listen:          listen,
		webhookPath:     webhookPath,
		registerURL:     strings.TrimSpace(registerURL),
		publicURL:       strings.TrimSpace(publicURL),
		sendPath:        sendPath,
		caps:            defaultCapabilities(),
		client:          &http.Client{Timeout: 30 * time.Second},
		previewRequests: make(map[string]chan string),
	}
}

func (t *gatewayTransport) Capabilities() map[string]bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneCapabilities(t.caps)
}

func (t *gatewayTransport) setCaps(caps map[string]bool) {
	t.mu.Lock()
	t.caps = caps
	t.mu.Unlock()
}

func (t *gatewayTransport) Start(ctx context.Context, onInbound inboundHandler) error {
	t.onInbound = onInbound
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	mux := http.NewServeMux()
	mux.HandleFunc(t.webhookPath, t.webhookHandler)
	t.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", t.listen)
	if err != nil {
		cancel()
		return fmt.Errorf("cloud_web: gateway listen: %w", err)
	}
	t.listener = ln

	go func() {
		slog.Info("cloud_web: gateway webhook listening", "addr", ln.Addr().String(), "path", t.webhookPath)
		if err := t.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("cloud_web: gateway server error", "error", err)
		}
	}()

	if t.registerURL != "" {
		caps, err := t.register(runCtx)
		if err != nil {
			slog.Warn("cloud_web: gateway register failed", "error", err)
		} else {
			t.setCaps(caps)
		}
	}
	return nil
}

func (t *gatewayTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = t.server.Shutdown(shutdownCtx)
	}
	return nil
}

func (t *gatewayTransport) register(ctx context.Context) (map[string]bool, error) {
	payload := buildRegisterPayload(t.name, t.project, "gateway")
	body, err := json.Marshal(map[string]any{
		"register":   payload,
		"public_url": t.publicURL,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.registerURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	authHTTP(req, t.token)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud_web: register HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return parseRegisterAck(raw)
}

func (t *gatewayTransport) authenticate(r *http.Request) bool {
	if t.token == "" {
		// Fail closed: a gateway transport without a configured token must
		// reject all inbound webhooks rather than accept anything. New()
		// already enforces a non-empty token; this is a defense-in-depth guard.
		return false
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return subtle.ConstantTimeCompare([]byte(auth[7:]), []byte(t.token)) == 1
	}
	if tok := r.Header.Get("X-Cloud-Web-Token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(t.token)) == 1
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(t.token)) == 1
	}
	return false
}

func (t *gatewayTransport) webhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !t.authenticate(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	go t.dispatchBatch(body)
}

func (t *gatewayTransport) dispatchBatch(raw []byte) {
	if t.dispatchBatchPayload(raw) {
		return
	}
	t.dispatchOne(raw)
}

func (t *gatewayTransport) dispatchBatchPayload(raw []byte) bool {
	var base wireMsg
	if err := json.Unmarshal(raw, &base); err == nil {
		switch base.Type {
		case "message", "card_action", "message_recall", "ping":
			t.dispatchOne(raw)
			return true
		}
	}
	var envelope struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Events) > 0 {
		for _, ev := range envelope.Events {
			t.dispatchOne(ev)
		}
		return true
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		for _, ev := range list {
			t.dispatchOne(ev)
		}
		return true
	}
	return false
}

func (t *gatewayTransport) dispatchOne(raw []byte) {
	var base wireMsg
	if err := json.Unmarshal(raw, &base); err != nil {
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
	case "capabilities_changed":
		var ch wireCapabilitiesChanged
		if err := json.Unmarshal(raw, &ch); err == nil && len(ch.Capabilities) > 0 {
			t.setCaps(capabilitySet(ch.Capabilities))
		}
	default:
		if t.onInbound != nil {
			t.onInbound(raw)
		}
	}
}

func (t *gatewayTransport) Send(ctx context.Context, msg map[string]any) error {
	if t.baseURL == "" {
		return fmt.Errorf("cloud_web: gateway mode requires base_url for outbound send")
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	url := joinURL(t.baseURL, t.sendPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	authHTTP(req, t.token)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("cloud_web: send HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var ack wirePreviewAck
	if json.Unmarshal(raw, &ack) == nil && ack.Type == "preview_ack" && ack.RefID != "" {
		t.previewMu.Lock()
		ch, ok := t.previewRequests[ack.RefID]
		if ok {
			delete(t.previewRequests, ack.RefID)
		}
		t.previewMu.Unlock()
		if ok {
			ch <- ack.PreviewHandle
		}
	}
	return nil
}

func (t *gatewayTransport) waitPreviewAck(refID string, timeout time.Duration) (string, error) {
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

// previewWaiter is implemented by transports that support async preview_ack.
type previewWaiter interface {
	waitPreviewAck(refID string, timeout time.Duration) (string, error)
}
