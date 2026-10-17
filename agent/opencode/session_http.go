package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func normalizeConnectionURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("opencode: invalid connection_url %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

func newOpencodeHTTPSession(ctx context.Context, connectionURL, username, password, workDir, model, mode, agentName, resumeID string, extraEnv []string) (*opencodeSession, error) {
	s, err := newOpencodeSession(ctx, "", nil, workDir, model, mode, agentName, resumeID, extraEnv)
	if err != nil {
		return nil, err
	}
	// Regular API calls need a timeout so a hung opencode server doesn't
	// block cc-connect forever. The event stream uses a separate client
	// without a timeout because it is intentionally long-lived.
	s.httpClient = &http.Client{Timeout: 60 * time.Second}
	s.streamClient = &http.Client{}
	s.connectionURL = connectionURL
	s.username = username
	s.password = password
	s.pendingQuestions = make(map[string][]core.UserQuestion)
	s.httpPartText = make(map[string]string)
	s.httpAssistantMsg = make(map[string]struct{})
	if s.username == "" {
		s.username = opencodeEnv(extraEnv, "OPENCODE_SERVER_USERNAME")
		if s.username == "" {
			s.username = "opencode"
		}
	}
	if s.password == "" {
		s.password = opencodeEnv(extraEnv, "OPENCODE_SERVER_PASSWORD")
	}

	if s.CurrentSessionID() == "" {
		var created struct {
			ID string `json:"id"`
		}
		if err := s.doHTTPJSON(http.MethodPost, "/session", map[string]any{}, &created); err != nil {
			s.cancel()
			return nil, fmt.Errorf("opencode: create HTTP session: %w", err)
		}
		if created.ID == "" {
			s.cancel()
			return nil, fmt.Errorf("opencode: create HTTP session: empty session ID")
		}
		s.chatID.Store(created.ID)
	}

	if err := s.startHTTPEventStream(); err != nil {
		s.cancel()
		return nil, err
	}
	return s, nil
}

func opencodeEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		name, value, ok := strings.Cut(env[i], "=")
		if ok && name == key {
			return value
		}
	}
	return os.Getenv(key)
}

func (s *opencodeSession) sendHTTP(prompt string, imagePaths []string) error {
	s.clearAgentError()
	parts := make([]map[string]any, 0, len(imagePaths)+1)
	for _, path := range imagePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("opencode: read staged image: %w", err)
		}
		mimeType := mime.TypeByExtension(filepath.Ext(path))
		if mimeType == "" {
			mimeType = "image/png"
		}
		parts = append(parts, map[string]any{
			"type":     "file",
			"url":      "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
			"filename": filepath.Base(path),
			"mime":     mimeType,
		})
	}
	parts = append(parts, map[string]any{"type": "text", "text": prompt})

	body := map[string]any{"parts": parts}
	if s.agentName != "" {
		body["agent"] = s.agentName
	}
	if providerID, modelID, ok := strings.Cut(s.model, "/"); ok && providerID != "" && modelID != "" {
		body["model"] = map[string]any{"providerID": providerID, "modelID": modelID}
	}

	// /prompt_async is fire-and-forget (204); the whole turn flows via /event SSE.
	// Decouples POST lifecycle from the agent turn so long turns (incl. background
	// subagent dispatch) can't break the POST and vice versa.
	path := "/session/" + url.PathEscape(s.CurrentSessionID()) + "/prompt_async"
	if err := s.doHTTPJSONWithRetryClient(s.httpClient, http.MethodPost, path, body, nil, 1); err != nil {
		// Surface any async session.error (quota, payment-required) emitted on /event
		// before returning the POST-level failure.
		if agentErr := s.waitHTTPAgentError(750 * time.Millisecond); agentErr != nil {
			return agentErr
		}
		return err
	}
	return nil
}

func (s *opencodeSession) waitHTTPAgentError(timeout time.Duration) error {
	if err := s.currentAgentError(); err != nil {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case <-timer.C:
			return s.currentAgentError()
		case <-ticker.C:
			if err := s.currentAgentError(); err != nil {
				return err
			}
		}
	}
}

// initialSSEBackoff and maxSSEBackoff bound the reconnect retry cadence after
// the /event SSE stream ends. The SSE is the only durable link to opencode's
// turn output, so on EOF we reconnect instead of tearing the session down.
const (
	initialSSEBackoff = 500 * time.Millisecond
	maxSSEBackoff     = 30 * time.Second
)

func (s *opencodeSession) startHTTPEventStream() error {
	resp, err := s.connectSSE()
	if err != nil {
		return fmt.Errorf("opencode: connect event stream: %w", err)
	}
	s.wg.Add(1)
	go s.readHTTPEventsLoop(resp)
	return nil
}

func (s *opencodeSession) connectSSE() (*http.Response, error) {
	resp, err := s.doStreamRequest(http.MethodGet, "/event", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// readHTTPEventsLoop consumes the /event SSE forever, reconnecting with
// exponential backoff (capped at maxSSEBackoff) whenever the stream ends.
// It only returns when s.ctx is cancelled (session close). Transient stream
// EOF — server restart, network blip, keepalive expiry — no longer kills the
// session; the loop just reconnects.
func (s *opencodeSession) readHTTPEventsLoop(initial *http.Response) {
	defer s.wg.Done()

	resp := initial
	backoff := initialSSEBackoff
	for {
		s.readSSEBody(resp.Body)
		_ = resp.Body.Close()
		if s.ctx.Err() != nil {
			return
		}
		slog.Info("opencode: /event stream ended, reconnecting", "backoff", backoff)

		for attempts := 0; ; attempts++ {
			if s.ctx.Err() != nil {
				return
			}
			if attempts > 0 {
				wait := backoff
				backoff *= 2
				if backoff > maxSSEBackoff {
					backoff = maxSSEBackoff
				}
				s.sleepContext(wait)
				if s.ctx.Err() != nil {
					return
				}
			}
			newResp, err := s.connectSSE()
			if err != nil {
				slog.Warn("opencode: /event reconnect failed", "error", err, "attempt", attempts+1)
				continue
			}
			resp = newResp
			backoff = initialSSEBackoff
			break
		}
	}
}

func (s *opencodeSession) readSSEBody(body io.ReadCloser) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				s.handleHTTPEvent([]byte(data.String()))
				data.Reset()
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if err := scanner.Err(); err != nil && s.ctx.Err() == nil {
		slog.Debug("opencode: /event scanner ended", "error", err)
	}
}

func (s *opencodeSession) sleepContext(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.ctx.Done():
	case <-t.C:
	}
}

func (s *opencodeSession) handleHTTPEvent(data []byte) {
	var event struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		slog.Debug("opencode: invalid SSE event", "error", err)
		return
	}
	props := event.Properties
	sessionID, _ := props["sessionID"].(string)
	if sessionID != "" && sessionID != s.CurrentSessionID() {
		return
	}

	switch event.Type {
	case "session.next.step.started":
		if assistantMessageID, _ := props["assistantMessageID"].(string); assistantMessageID != "" {
			s.markHTTPAssistantMessageByID(assistantMessageID)
		}
	case "message.part.updated":
		s.handleHTTPPart(props)
	case "session.status":
		status, _ := props["status"].(map[string]any)
		switch statusType, _ := status["type"].(string); statusType {
		case "busy":
			s.resultSent.Store(false)
		case "idle":
			s.sendEventResult()
		case "retry":
			s.handleHTTPRetryStatus(status)
		}
	case "session.idle":
		s.sendEventResult()
	case "session.error":
		s.handleError(map[string]any{"error": props["error"]})
	case "permission.asked":
		s.handleHTTPPermission(props)
	case "question.asked", "question.v2.asked":
		s.handleHTTPQuestion(props)
	}
}

func (s *opencodeSession) handleHTTPRetryStatus(status map[string]any) {
	message := strings.TrimSpace(stringValue(status["message"]))
	if message == "" || !isHTTPRetryFatal(message) {
		return
	}
	err := fmt.Errorf("%s", message)
	s.rememberAgentError(err)
	s.emitHTTPEvent(core.Event{Type: core.EventError, Error: err})
}

func isHTTPRetryFatal(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"usage limit",
		"rate limit",
		"quota",
		"insufficient balance",
		"payment required",
		"429",
		"使用上限",
		"限额",
		"余额不足",
		"额度",
	} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func (s *opencodeSession) handleHTTPPart(props map[string]any) {
	part, _ := props["part"].(map[string]any)
	if part == nil {
		return
	}
	if sessionID, _ := part["sessionID"].(string); sessionID != s.CurrentSessionID() {
		return
	}
	raw := map[string]any{"part": part}
	switch partType, _ := part["type"].(string); partType {
	case "text":
		s.handleHTTPText(part)
	case "reasoning":
		s.handleHTTPReasoning(part)
	case "tool":
		state, _ := part["state"].(map[string]any)
		status, _ := state["status"].(string)
		if status == "completed" || status == "error" {
			s.handleToolUse(raw)
		}
	case "step-start":
		s.markHTTPAssistantMessage(part)
		s.handleStepStart(raw)
	case "step-finish":
		s.handleStepFinish(raw)
	}
}

func (s *opencodeSession) markHTTPAssistantMessage(part map[string]any) {
	messageID := stringValue(part["messageID"])
	s.markHTTPAssistantMessageByID(messageID)
}

func (s *opencodeSession) markHTTPAssistantMessageByID(messageID string) {
	if messageID == "" {
		return
	}
	s.httpPartMu.Lock()
	s.httpAssistantMsg[messageID] = struct{}{}
	s.httpPartMu.Unlock()
}

func (s *opencodeSession) isHTTPAssistantPart(part map[string]any) bool {
	messageID := stringValue(part["messageID"])
	if messageID == "" {
		return false
	}
	s.httpPartMu.Lock()
	_, ok := s.httpAssistantMsg[messageID]
	s.httpPartMu.Unlock()
	return ok
}

func (s *opencodeSession) handleHTTPText(part map[string]any) {
	if !s.isHTTPAssistantPart(part) {
		return
	}
	text := s.httpPartDelta(part)
	if text == "" {
		return
	}

	metadata, _ := part["metadata"].(map[string]any)
	synthetic, _ := part["synthetic"].(bool)
	if synthetic && metadata != nil {
		if cc, ok := metadata["compaction_continue"].(bool); ok && cc {
			slog.Info("opencodeSession: compaction_continue detected, marking expectingContinue", "session_id", s.CurrentSessionID())
			s.expectingContinue.Store(true)
			return
		}
	}

	s.emitHTTPEvent(core.Event{Type: core.EventText, Content: text, Metadata: metadata, Synthetic: synthetic})
}

func (s *opencodeSession) handleHTTPReasoning(part map[string]any) {
	if !s.isHTTPAssistantPart(part) {
		return
	}
	text := s.httpPartDelta(part)
	if text == "" {
		return
	}
	s.emitHTTPEvent(core.Event{Type: core.EventThinking, Content: text})
}

func (s *opencodeSession) httpPartDelta(part map[string]any) string {
	text := stringValue(part["text"])
	if text == "" {
		return ""
	}
	key := httpPartKey(part)
	if key == "" {
		if opencodePartEnded(part) {
			return text
		}
		return ""
	}

	s.httpPartMu.Lock()
	defer s.httpPartMu.Unlock()
	previous := s.httpPartText[key]
	s.httpPartText[key] = text
	if previous == "" {
		return text
	}
	if strings.HasPrefix(text, previous) {
		return text[len(previous):]
	}
	return text
}

func httpPartKey(part map[string]any) string {
	if id := stringValue(part["id"]); id != "" {
		return id
	}
	partType := stringValue(part["type"])
	messageID := stringValue(part["messageID"])
	if partType != "" && messageID != "" {
		return messageID + ":" + partType
	}
	return ""
}

func opencodePartEnded(part map[string]any) bool {
	timing, _ := part["time"].(map[string]any)
	end, ok := timing["end"]
	return ok && end != nil
}

func (s *opencodeSession) handleHTTPPermission(props map[string]any) {
	requestID, _ := props["id"].(string)
	permission, _ := props["permission"].(string)
	patterns := opencodeStrings(props["patterns"])
	if s.mode == "yolo" {
		if err := s.RespondPermission(requestID, core.PermissionResult{Behavior: "allow"}); err != nil {
			s.emitHTTPEvent(core.Event{Type: core.EventError, Error: err})
		}
		return
	}
	input := map[string]any{"patterns": patterns}
	if metadata, ok := props["metadata"].(map[string]any); ok {
		input["metadata"] = metadata
	}
	s.emitHTTPEvent(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     permission,
		ToolInput:    strings.Join(patterns, ", "),
		ToolInputRaw: input,
	})
}

func (s *opencodeSession) handleHTTPQuestion(props map[string]any) {
	requestID, _ := props["id"].(string)
	if requestID == "" {
		return
	}
	rawQuestions, _ := props["questions"].([]any)
	questions := make([]core.UserQuestion, 0, len(rawQuestions))
	for _, raw := range rawQuestions {
		question, _ := raw.(map[string]any)
		if question == nil {
			continue
		}
		options := make([]core.UserQuestionOption, 0)
		rawOptions, _ := question["options"].([]any)
		for _, rawOption := range rawOptions {
			option, _ := rawOption.(map[string]any)
			if option == nil {
				continue
			}
			options = append(options, core.UserQuestionOption{
				Label:       stringValue(option["label"]),
				Description: stringValue(option["description"]),
			})
		}
		questions = append(questions, core.UserQuestion{
			Question:    stringValue(question["question"]),
			Header:      stringValue(question["header"]),
			Options:     options,
			MultiSelect: boolValue(question["multiple"]),
		})
	}
	if len(questions) == 0 {
		return
	}
	s.pendingMu.Lock()
	s.pendingQuestions[requestID] = questions
	s.pendingMu.Unlock()
	s.emitHTTPEvent(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "AskUserQuestion",
		ToolInputRaw: map[string]any{"questions": rawQuestions},
		Questions:    questions,
	})
}

func (s *opencodeSession) respondHTTPPermission(requestID string, result core.PermissionResult) error {
	s.pendingMu.Lock()
	questions, isQuestion := s.pendingQuestions[requestID]
	s.pendingMu.Unlock()
	if isQuestion {
		path := "/question/" + url.PathEscape(requestID)
		if !strings.EqualFold(result.Behavior, "allow") {
			return s.doHTTPJSON(http.MethodPost, path+"/reject", nil, nil)
		}
		answersByQuestion, _ := result.UpdatedInput["answers"].(map[string]any)
		answers := make([][]string, 0, len(questions))
		for _, question := range questions {
			answer := strings.TrimSpace(stringValue(answersByQuestion[question.Question]))
			if question.MultiSelect {
				answers = append(answers, strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == '，' }))
			} else {
				answers = append(answers, []string{answer})
			}
		}
		if err := s.doHTTPJSON(http.MethodPost, path+"/reply", map[string]any{"answers": answers}, nil); err != nil {
			return err
		}
		s.pendingMu.Lock()
		delete(s.pendingQuestions, requestID)
		s.pendingMu.Unlock()
		return nil
	}

	reply := "reject"
	if strings.EqualFold(result.Behavior, "allow") {
		reply = "once"
	}
	return s.doHTTPJSON(http.MethodPost, "/permission/"+url.PathEscape(requestID)+"/reply", map[string]any{
		"reply":   reply,
		"message": result.Message,
	}, nil)
}

func (s *opencodeSession) doHTTPJSON(method, path string, body, output any) error {
	return s.doHTTPJSONWithRetry(method, path, body, output, 1)
}

func (s *opencodeSession) doHTTPJSONWithRetry(method, path string, body, output any, maxAttempts int) error {
	return s.doHTTPJSONWithRetryClient(s.httpClient, method, path, body, output, maxAttempts)
}

func (s *opencodeSession) doHTTPJSONWithRetryClient(client *http.Client, method, path string, body, output any, maxAttempts int) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			slog.Info("opencode: retrying HTTP request", "path", path, "attempt", attempt+1, "max", maxAttempts)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		resp, err := s.doRequestWithClient(client, method, path, body)
		if err != nil {
			lastErr = err
			if !isHTTPRetryableError(err) {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
			if !isHTTPRetryableStatus(resp.StatusCode) {
				return lastErr
			}
			continue
		}
		defer func() { _ = resp.Body.Close() }()
		if output == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			return err
		}
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return nil
	}
	return lastErr
}

func isHTTPRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
		return true
	}
	if netErr, ok := err.(interface{ Temporary() bool }); ok && netErr.Temporary() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"econnreset", "econnrefused", "econnaborted", "enoent", "broken pipe",
		"connection reset", "connection refused", "connection aborted",
		"no such host", "i/o timeout", "tls handshake timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func isHTTPRetryableStatus(code int) bool {
	return code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout ||
		code == http.StatusTooManyRequests || code == http.StatusBadGateway
}

func (s *opencodeSession) doStreamRequest(method, path string, body any) (*http.Response, error) {
	return s.doRequestWithClient(s.streamClient, method, path, body)
}

func (s *opencodeSession) doRequestWithClient(client *http.Client, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	u, err := url.Parse(s.connectionURL + path)
	if err != nil {
		return nil, err
	}
	if s.workDir != "" {
		query := u.Query()
		query.Set("directory", s.workDir)
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(s.ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.password != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	return client.Do(req)
}

func (s *opencodeSession) emitHTTPEvent(event core.Event) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

func opencodeStrings(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
