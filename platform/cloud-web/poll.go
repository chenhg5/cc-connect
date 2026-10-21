package cloudweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type pollTransport struct {
	baseURL    string
	token      string
	name       string
	project    string
	eventsPath string
	sendPath   string
	timeout    time.Duration

	mu        sync.RWMutex
	caps      map[string]bool
	client    *http.Client
	cancel    context.CancelFunc
	onInbound inboundHandler

	previewMu       sync.Mutex
	previewRequests map[string]chan string
}

func newPollTransport(baseURL, token, name, project, eventsPath, sendPath string, timeoutMS int) *pollTransport {
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if eventsPath == "" {
		eventsPath = defaultEvents
	}
	if sendPath == "" {
		sendPath = defaultSend
	}
	return &pollTransport{
		baseURL:         strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:           token,
		name:            name,
		project:         project,
		eventsPath:      eventsPath,
		sendPath:        sendPath,
		timeout:         timeout,
		caps:            defaultCapabilities(),
		client:          &http.Client{Timeout: timeout + 15*time.Second},
		previewRequests: make(map[string]chan string),
	}
}

func (t *pollTransport) Capabilities() map[string]bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneCapabilities(t.caps)
}

func (t *pollTransport) setCaps(caps map[string]bool) {
	t.mu.Lock()
	t.caps = caps
	t.mu.Unlock()
}

func (t *pollTransport) Start(ctx context.Context, onInbound inboundHandler) error {
	t.onInbound = onInbound
	caps, err := t.register(ctx)
	if err != nil {
		return err
	}
	t.setCaps(caps)
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.pollLoop(runCtx)
	return nil
}

func (t *pollTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *pollTransport) register(ctx context.Context) (map[string]bool, error) {
	body, err := json.Marshal(buildRegisterPayload(t.name, t.project, "long_poll"))
	if err != nil {
		return nil, err
	}
	url := joinURL(t.baseURL, defaultRegister)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	authHTTP(req, t.token)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud_web: register request: %w", err)
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

func (t *pollTransport) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := t.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("cloud_web: long poll error", "error", err)
			time.Sleep(time.Second)
		}
	}
}

func (t *pollTransport) pollOnce(ctx context.Context) error {
	payload := map[string]any{
		"type":       "poll",
		"timeout_ms": int(t.timeout / time.Millisecond),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := joinURL(t.baseURL, t.eventsPath)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloud_web: events HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return t.dispatchBatch(raw)
}

func (t *pollTransport) dispatchBatch(raw []byte) error {
	if t.dispatchBatchPayload(raw) {
		return nil
	}
	t.dispatchOne(raw)
	return nil
}

func (t *pollTransport) dispatchBatchPayload(raw []byte) bool {
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

func (t *pollTransport) dispatchOne(raw []byte) {
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

func (t *pollTransport) Send(ctx context.Context, msg map[string]any) error {
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
	// Handle inline preview_ack in send response.
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

func (t *pollTransport) waitPreviewAck(refID string, timeout time.Duration) (string, error) {
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
