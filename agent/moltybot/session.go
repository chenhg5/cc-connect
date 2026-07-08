package moltybot

import (
	"bytes"
	"context"
	"encoding/base64"
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
	source    bridgeSource
	closeOnce sync.Once
}

type bridgeMessageRequest struct {
	Source      bridgeSource       `json:"source"`
	Text        string             `json:"text"`
	Attachments []bridgeAttachment `json:"attachments,omitempty"`
}

type bridgeSource struct {
	Platform       string `json:"platform,omitempty"`
	PlatformUserID string `json:"platformUserId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
}

type bridgeAttachment struct {
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	DataBase64 string `json:"dataBase64,omitempty"`
}

type bridgeMessageResponse struct {
	OK          bool               `json:"ok"`
	SessionKey  string             `json:"sessionKey,omitempty"`
	ReplyText   string             `json:"replyText,omitempty"`
	Attachments []bridgeAttachment `json:"attachments,omitempty"`
	Error       string             `json:"error,omitempty"`
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
		source:      bridgeSourceFromSessionID(sessionID),
	}
	s.alive.Store(true)
	return s
}

func (s *session) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("moltybot: session is closed")
	}

	body := bridgeMessageRequest{
		Source:      s.Source(),
		Text:        prompt,
		Attachments: convertAttachments(images, files),
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
		return s.emitError(fmt.Errorf("moltybot: bridge returned HTTP %d: %s", resp.StatusCode, detail))
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
		return s.emitError(fmt.Errorf("moltybot: bridge rejected message: %s", detail))
	}

	if result.SessionKey != "" {
		s.mu.Lock()
		s.sessionID = result.SessionKey
		s.mu.Unlock()
	}
	resultImages, resultFiles, err := convertResponseAttachments(result.Attachments)
	if err != nil {
		return s.emitError(fmt.Errorf("moltybot: decode bridge response attachments: %w", err))
	}

	s.emit(core.Event{
		Type:      core.EventResult,
		Content:   result.ReplyText,
		Images:    resultImages,
		Files:     resultFiles,
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

func (s *session) Source() bridgeSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source
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

func (s *session) emitError(err error) error {
	s.emit(core.Event{
		Type:      core.EventError,
		Content:   err.Error(),
		SessionID: s.CurrentSessionID(),
		Done:      true,
		Error:     err,
	})
	return err
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

func bridgeSourceFromSessionID(sessionID string) bridgeSource {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return bridgeSource{}
	}
	if strings.HasPrefix(sessionID, "remote:") {
		parts := strings.SplitN(sessionID, ":", 3)
		if len(parts) == 3 {
			return bridgeSource{Platform: parts[1], PlatformUserID: parts[2]}
		}
	}
	parts := strings.Split(sessionID, ":")
	if len(parts) == 1 {
		return bridgeSource{Platform: parts[0], PlatformUserID: parts[0]}
	}
	return bridgeSource{
		Platform:       parts[0],
		PlatformUserID: parts[len(parts)-1],
	}
}

func convertAttachments(images []core.ImageAttachment, files []core.FileAttachment) []bridgeAttachment {
	if len(images) == 0 && len(files) == 0 {
		return nil
	}
	out := make([]bridgeAttachment, 0, len(images)+len(files))
	for _, image := range images {
		out = append(out, bridgeAttachment{
			Kind:       "image",
			Name:       image.FileName,
			MimeType:   image.MimeType,
			DataBase64: base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	for _, file := range files {
		out = append(out, bridgeAttachment{
			Kind:       "file",
			Name:       file.FileName,
			MimeType:   file.MimeType,
			DataBase64: base64.StdEncoding.EncodeToString(file.Data),
		})
	}
	return out
}

func convertResponseAttachments(attachments []bridgeAttachment) ([]core.ImageAttachment, []core.FileAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil, nil
	}
	images := make([]core.ImageAttachment, 0)
	files := make([]core.FileAttachment, 0)
	for _, attachment := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachment.DataBase64))
		if err != nil {
			return nil, nil, fmt.Errorf("attachment %q: invalid base64: %w", attachment.Name, err)
		}
		if len(data) == 0 {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(attachment.Kind))
		mimeType := strings.TrimSpace(attachment.MimeType)
		if kind == "" && strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			kind = "image"
		}
		switch kind {
		case "image":
			images = append(images, core.ImageAttachment{
				MimeType: mimeType,
				FileName: attachment.Name,
				Data:     data,
			})
		case "file":
			files = append(files, core.FileAttachment{
				MimeType: mimeType,
				FileName: attachment.Name,
				Data:     data,
			})
		default:
			return nil, nil, fmt.Errorf("attachment %q: unsupported kind %q", attachment.Name, attachment.Kind)
		}
	}
	return images, files, nil
}

var _ core.AgentSession = (*session)(nil)
