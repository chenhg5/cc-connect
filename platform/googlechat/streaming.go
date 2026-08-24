package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/chenhg5/cc-connect/core"
)

// previewHandle points at the in-flight streaming-preview message so
// UpdateMessage can patch it in place via spaces.messages.patch.
type previewHandle struct {
	name string // e.g. "spaces/AAA/messages/MMM"
}

// SendPreviewStart posts the initial streaming-preview message (threaded like a
// normal reply) and returns a handle for subsequent edits. Implements
// core.PreviewStarter; together with UpdateMessage it lights up the engine's
// real-time streaming preview for Google Chat.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("googlechat: invalid reply context type %T", rctx)
	}
	if rc.space == "" {
		return nil, fmt.Errorf("googlechat: missing space in reply context")
	}
	url, body, err := buildSendRequest(rc, content)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("googlechat: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("googlechat: send preview: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("googlechat: close preview response body", "error", err)
		}
	}()
	var msg struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, fmt.Errorf("googlechat: parse create response: %w", err)
	}
	// drain any bytes json.Decoder left unread so the connection is reusable
	_, _ = io.Copy(io.Discard, resp.Body)
	if msg.Name == "" {
		return nil, fmt.Errorf("googlechat: create response missing message name")
	}
	return &previewHandle{name: msg.Name}, nil
}

// buildUpdateRequest builds the PATCH URL and JSON body to update a message's text.
func buildUpdateRequest(msgName, content string) (string, []byte, error) {
	u := chatAPIBase + msgName + "?updateMask=text"
	b, err := json.Marshal(map[string]any{"text": content})
	if err != nil {
		return "", nil, fmt.Errorf("googlechat: marshal update body: %w", err)
	}
	return u, b, nil
}

// UpdateMessage patches the preview message text in place. The engine passes the
// handle returned by SendPreviewStart (not the reply context). Implements
// core.MessageUpdater.
func (p *Platform) UpdateMessage(ctx context.Context, handle any, content string) error {
	h, ok := handle.(*previewHandle)
	if !ok {
		return fmt.Errorf("googlechat: invalid preview handle type %T", handle)
	}
	url, body, err := buildUpdateRequest(h.name, content)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("googlechat: build update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.doRequest(req)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		return fmt.Errorf("googlechat: drain update response body: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("googlechat: close update response body: %w", err)
	}
	return nil
}

var (
	_ core.MessageUpdater = (*Platform)(nil)
	_ core.PreviewStarter = (*Platform)(nil)
)
