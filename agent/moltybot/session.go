package moltybot

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
	"sync/atomic"

	"github.com/chenhg5/cc-connect/core"
)

type session struct {
	client      *http.Client
	baseURL     string
	token       string
	sessionMode string

	ctx    context.Context
	cancel context.CancelFunc
	events chan core.Event
	alive  atomic.Bool

	mu        sync.Mutex
	sessionID string
	closeOnce sync.Once
}

type bridgeMessageRequest struct {
	SessionKey string             `json:"sessionKey,omitempty"`
	SessionID  string             `json:"sessionId,omitempty"`
	Text       string             `json:"text"`
	Images     []bridgeAttachment `json:"images,omitempty"`
	Files      []bridgeAttachment `json:"files,omitempty"`
}

type bridgeAttachment struct {
	MimeType string `json:"mimeType,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

type bridgeMessageResponse struct {
	OK         bool   `json:"ok"`
	SessionKey string `json:"sessionKey,omitempty"`
	ReplyText  string `json:"replyText,omitempty"`
	Error      string `json:"error,omitempty"`
}

func newSession(ctx context.Context, client *http.Client, baseURL, token, sessionMode, sessionID string) *session {
	sessionCtx, cancel := context.WithCancel(ctx)
	bridgeSessionID := bridgeSessionKey(sessionMode, sessionID)
	s := &session{
		client:      client,
		baseURL:     baseURL,
		token:       token,
		sessionMode: sessionMode,
		ctx:         sessionCtx,
		cancel:      cancel,
		events:      make(chan core.Event, 128),
		sessionID:   bridgeSessionID,
	}
	s.alive.Store(true)
	return s
}

func (s *session) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("moltybot: session is closed")
	}

	sessionID := s.CurrentSessionID()
	body := bridgeMessageRequest{
		SessionKey: sessionID,
		SessionID:  sessionID,
		Text:       prompt,
		Images:     convertImageAttachments(images),
		Files:      convertFileAttachments(files),
	}

	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(body); err != nil {
		return fmt.Errorf("moltybot: encode bridge request: %w", err)
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.baseURL+bridgeMessagesEndpointSuffix, &payload)
	if err != nil {
		return fmt.Errorf("moltybot: create bridge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("moltybot: post bridge message: %s", core.RedactToken(err.Error(), s.token))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(core.RedactToken(string(raw), s.token))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("moltybot: bridge returned HTTP %d: %s", resp.StatusCode, detail)
	}

	var result bridgeMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("moltybot: decode bridge response: %s", core.RedactToken(err.Error(), s.token))
	}
	if !result.OK {
		detail := strings.TrimSpace(core.RedactToken(result.Error, s.token))
		if detail == "" {
			detail = "unknown bridge error"
		}
		return fmt.Errorf("moltybot: bridge rejected message: %s", detail)
	}

	if result.SessionKey != "" {
		s.mu.Lock()
		s.sessionID = result.SessionKey
		s.mu.Unlock()
	}

	s.emit(core.Event{
		Type:      core.EventResult,
		Content:   result.ReplyText,
		SessionID: s.CurrentSessionID(),
		Done:      true,
	})
	slog.Debug("moltybot: bridge message completed", "session_id", s.CurrentSessionID())
	return nil
}

func (s *session) RespondPermission(_ string, _ core.PermissionResult) error {
	return fmt.Errorf("moltybot: permission requests are not supported: %w", core.ErrNotSupported)
}

func (s *session) Events() <-chan core.Event { return s.events }

func (s *session) CurrentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *session) Alive() bool { return s.alive.Load() }

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		s.cancel()
		close(s.events)
	})
	return nil
}

func (s *session) emit(ev core.Event) {
	defer func() { _ = recover() }()
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func bridgeSessionKey(sessionMode, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if sessionMode != sessionModePerRemoteUser || strings.HasPrefix(sessionID, "remote:") {
		return sessionID
	}
	parts := strings.Split(sessionID, ":")
	if len(parts) < 2 {
		return "remote:" + sessionID
	}
	platform := strings.TrimSpace(parts[0])
	user := strings.TrimSpace(parts[len(parts)-1])
	if platform == "" || user == "" {
		return "remote:" + sessionID
	}
	return "remote:" + platform + ":" + user
}

func convertImageAttachments(images []core.ImageAttachment) []bridgeAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]bridgeAttachment, 0, len(images))
	for _, image := range images {
		out = append(out, bridgeAttachment{
			MimeType: image.MimeType,
			FileName: image.FileName,
			Data:     image.Data,
		})
	}
	return out
}

func convertFileAttachments(files []core.FileAttachment) []bridgeAttachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]bridgeAttachment, 0, len(files))
	for _, file := range files {
		out = append(out, bridgeAttachment{
			MimeType: file.MimeType,
			FileName: file.FileName,
			Data:     file.Data,
		})
	}
	return out
}

var _ core.AgentSession = (*session)(nil)
