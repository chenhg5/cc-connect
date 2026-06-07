package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const opencodeSSEReconnectDelay = 2 * time.Second

type opencodeSSEMessage struct {
	ID        string
	SessionID string
	Role      string
	Finish    string
}

type opencodeSSEPart struct {
	ID        string
	MessageID string
	SessionID string
	Type      string
	Text      string
}

func (s *opencodeSession) StartUnsolicitedEvents(ctx context.Context) error {
	if s.attachURL == "" || !s.alive.Load() || s.expectingContinue.Load() {
		return nil
	}
	sid := s.CurrentSessionID()
	if sid == "" {
		return nil
	}

	eventURLs, err := s.unsolicitedEventURLs()
	if err != nil {
		return err
	}
	for _, eventURL := range eventURLs {
		s.wg.Add(1)
		go func(eventURL string) {
			defer s.wg.Done()
			s.watchSSEForUnsolicitedEvents(ctx, sid, eventURL)
		}(eventURL)
	}
	return nil
}

func (s *opencodeSession) unsolicitedEventURLs() ([]string, error) {
	base, err := url.Parse(s.attachURL)
	if err != nil {
		return nil, fmt.Errorf("opencode: parse attach url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("opencode: invalid attach url %q", s.attachURL)
	}

	var candidates []string
	add := func(withDirectory bool) {
		u := *base
		u.Path = "/event"
		u.RawQuery = ""
		u.Fragment = ""
		if withDirectory && s.workDir != "" {
			q := u.Query()
			q.Set("directory", s.workDir)
			u.RawQuery = q.Encode()
		}
		candidates = append(candidates, u.String())
	}
	if s.workDir != "" {
		add(true)
	}
	add(false)

	seen := make(map[string]struct{}, len(candidates))
	urls := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		urls = append(urls, candidate)
	}
	return urls, nil
}

func (s *opencodeSession) watchSSEForUnsolicitedEvents(ctx context.Context, sid, eventURL string) {
	for {
		if !s.unsolicitedContextAlive(ctx) {
			return
		}
		err := s.streamSSEOnce(ctx, sid, eventURL)
		if !s.unsolicitedContextAlive(ctx) {
			return
		}
		if err != nil {
			slog.Warn("opencode: unsolicited SSE stream ended",
				"url", eventURL,
				"session_id", sid,
				"error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			return
		case <-time.After(opencodeSSEReconnectDelay):
		}
	}
}

func (s *opencodeSession) streamSSEOnce(ctx context.Context, sid, eventURL string) error {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-reqCtx.Done():
		}
	}()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, eventURL, nil)
	if err != nil {
		return fmt.Errorf("opencode: build SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if !s.unsolicitedContextAlive(ctx) {
			return nil
		}
		return fmt.Errorf("opencode: connect SSE: %w", err)
	}
	// Body close errors are non-fatal after a successful SSE read loop.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("opencode: SSE status %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		s.handleSSEData(ctx, sid, strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
	}

	for scanner.Scan() {
		if !s.unsolicitedContextAlive(ctx) {
			return nil
		}
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			dataLines = append(dataLines, value)
		}
	}
	flush()
	if err := scanner.Err(); err != nil && s.unsolicitedContextAlive(ctx) {
		return fmt.Errorf("opencode: read SSE: %w", err)
	}
	return nil
}

func (s *opencodeSession) handleSSEData(ctx context.Context, sid, data string) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		slog.Debug("opencode: ignore non-JSON SSE data", "data", truncate(data, 500))
		return
	}
	payload := raw
	if wrapped := asStringMap(raw["payload"]); wrapped != nil {
		payload = wrapped
	}

	eventType, _ := payload["type"].(string)
	props := asStringMap(payload["properties"])
	if props == nil {
		props = payload
	}

	switch eventType {
	case "message.part.updated", "message.part.delta":
		s.handleSSEPart(ctx, sid, eventType, props)
	case "message.updated":
		s.handleSSEMessage(ctx, sid, props)
	}
}

func (s *opencodeSession) handleSSEPart(ctx context.Context, sid, eventType string, props map[string]any) {
	part := extractSSEPart(props)
	if part.MessageID == "" {
		return
	}
	if part.SessionID != "" && part.SessionID != sid {
		return
	}
	if part.ID == "" {
		part.ID = part.MessageID
	}
	if s.isSeen("", part.ID) {
		return
	}
	if part.Type == "" && part.Text != "" {
		part.Type = "text"
	}
	if part.Type != "text" || part.Text == "" {
		return
	}

	s.sseMu.Lock()
	s.ensureSSEMapsLocked()
	existing, exists := s.sseParts[part.ID]
	if exists && eventType == "message.part.delta" {
		part.Text = existing.Text + part.Text
	}
	if exists && part.SessionID == "" {
		part.SessionID = existing.SessionID
	}
	if exists && part.MessageID == "" {
		part.MessageID = existing.MessageID
	}
	if !exists {
		s.ssePartOrder[part.MessageID] = append(s.ssePartOrder[part.MessageID], part.ID)
	}
	s.sseParts[part.ID] = part
	s.sseMu.Unlock()

	s.tryEmitSSEMessage(ctx, sid, part.MessageID)
}

func (s *opencodeSession) handleSSEMessage(ctx context.Context, sid string, props map[string]any) {
	msg := extractSSEMessage(props)
	if msg.ID == "" {
		return
	}
	if msg.SessionID != "" && msg.SessionID != sid {
		return
	}
	if msg.Role != "assistant" || msg.Finish != "stop" {
		return
	}

	s.sseMu.Lock()
	s.ensureSSEMapsLocked()
	s.sseMessages[msg.ID] = msg
	s.sseMu.Unlock()

	s.tryEmitSSEMessage(ctx, sid, msg.ID)
}

func (s *opencodeSession) tryEmitSSEMessage(ctx context.Context, sid, messageID string) {
	if messageID == "" || !s.unsolicitedContextAlive(ctx) {
		return
	}

	s.sseMu.Lock()
	msg, ok := s.sseMessages[messageID]
	if !ok || msg.Role != "assistant" || msg.Finish != "stop" || (msg.SessionID != "" && msg.SessionID != sid) {
		s.sseMu.Unlock()
		return
	}
	order := append([]string(nil), s.ssePartOrder[messageID]...)
	parts := make([]opencodeSSEPart, 0, len(order))
	for _, partID := range order {
		part, ok := s.sseParts[partID]
		if !ok || part.Type != "text" || part.Text == "" {
			continue
		}
		if part.SessionID != "" && part.SessionID != sid {
			continue
		}
		parts = append(parts, part)
	}
	s.sseMu.Unlock()

	if len(parts) == 0 {
		return
	}

	var b strings.Builder
	partIDs := make([]string, 0, len(parts))
	for _, part := range parts {
		b.WriteString(part.Text)
		if part.ID != "" {
			partIDs = append(partIDs, part.ID)
		}
	}
	text := b.String()
	if strings.TrimSpace(text) == "" {
		return
	}
	if !s.claimSeen(messageID, partIDs...) {
		return
	}

	evt := core.Event{Type: core.EventResult, SessionID: sid, Content: text, Done: true}
	if s.emitWithContext(ctx, evt) {
		slog.Info("opencode: emitted unsolicited assistant message",
			"session_id", sid,
			"message_id", messageID,
			"content_len", len(text))
	}
}

func (s *opencodeSession) emitWithContext(ctx context.Context, evt core.Event) bool {
	if !s.alive.Load() {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	default:
	}
	select {
	case s.events <- evt:
		return true
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	}
}

func (s *opencodeSession) unsolicitedContextAlive(ctx context.Context) bool {
	if !s.alive.Load() || s.expectingContinue.Load() {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	default:
		return true
	}
}

func (s *opencodeSession) markSeenFromRaw(raw map[string]any) {
	part := asStringMap(raw["part"])
	if part == nil {
		return
	}
	partID := stringField(part, "id", "partID", "partId", "part_id")
	s.markSeen("", partID)
}

func (s *opencodeSession) markSeen(msgID, partID string) {
	if msgID == "" && partID == "" {
		return
	}
	s.seenMu.Lock()
	s.ensureSeenMapsLocked()
	if msgID != "" {
		s.seenMessages[msgID] = struct{}{}
	}
	if partID != "" {
		s.seenParts[partID] = struct{}{}
	}
	s.seenMu.Unlock()
}

func (s *opencodeSession) claimSeen(msgID string, partIDs ...string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	s.ensureSeenMapsLocked()
	if msgID != "" {
		if _, ok := s.seenMessages[msgID]; ok {
			return false
		}
	}
	for _, partID := range partIDs {
		if partID == "" {
			continue
		}
		if _, ok := s.seenParts[partID]; ok {
			return false
		}
	}
	if msgID != "" {
		s.seenMessages[msgID] = struct{}{}
	}
	for _, partID := range partIDs {
		if partID != "" {
			s.seenParts[partID] = struct{}{}
		}
	}
	return true
}

func (s *opencodeSession) isSeen(msgID, partID string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if msgID != "" {
		if _, ok := s.seenMessages[msgID]; ok {
			return true
		}
	}
	if partID != "" {
		if _, ok := s.seenParts[partID]; ok {
			return true
		}
	}
	return false
}

func (s *opencodeSession) ensureSeenMapsLocked() {
	if s.seenMessages == nil {
		s.seenMessages = make(map[string]struct{})
	}
	if s.seenParts == nil {
		s.seenParts = make(map[string]struct{})
	}
}

func (s *opencodeSession) ensureSSEMapsLocked() {
	if s.sseMessages == nil {
		s.sseMessages = make(map[string]opencodeSSEMessage)
	}
	if s.sseParts == nil {
		s.sseParts = make(map[string]opencodeSSEPart)
	}
	if s.ssePartOrder == nil {
		s.ssePartOrder = make(map[string][]string)
	}
}

func extractSSEMessage(props map[string]any) opencodeSSEMessage {
	for _, key := range []string{"info", "message", "messageInfo"} {
		if m := asStringMap(props[key]); m != nil {
			return opencodeSSEMessage{
				ID:        firstString(m, props, "id", "messageID", "messageId", "message_id"),
				SessionID: firstString(m, props, "sessionID", "sessionId", "session_id"),
				Role:      strings.ToLower(firstString(m, props, "role")),
				Finish:    firstString(m, props, "finish", "reason"),
			}
		}
	}
	return opencodeSSEMessage{
		ID:        stringField(props, "id", "messageID", "messageId", "message_id"),
		SessionID: stringField(props, "sessionID", "sessionId", "session_id"),
		Role:      strings.ToLower(stringField(props, "role")),
		Finish:    stringField(props, "finish", "reason"),
	}
}

func extractSSEPart(props map[string]any) opencodeSSEPart {
	part := asStringMap(props["part"])
	if part == nil {
		part = props
	}
	delta := asStringMap(props["delta"])
	text := stringField(part, "text", "content")
	if text == "" && delta != nil {
		text = stringField(delta, "text", "content")
	}
	return opencodeSSEPart{
		ID:        firstString(part, props, "id", "partID", "partId", "part_id"),
		MessageID: firstString(part, props, "messageID", "messageId", "message_id"),
		SessionID: firstString(part, props, "sessionID", "sessionId", "session_id"),
		Type:      firstString(part, props, "type"),
		Text:      text,
	}
}

func asStringMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func firstString(primary, fallback map[string]any, keys ...string) string {
	if v := stringField(primary, keys...); v != "" {
		return v
	}
	return stringField(fallback, keys...)
}

func stringField(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}
