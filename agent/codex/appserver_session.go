package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type rpcResponseEnvelope struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcNotificationEnvelope struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type threadStartResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type threadResumeResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type turnStartResponse struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type turnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

type itemNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	Item     map[string]any `json:"item"`
}

type errorNotification struct {
	Message string `json:"message"`
}

type appServerSession struct {
	url       string
	workDir   string
	model     string
	effort    string
	mode      string
	extraEnv  []string
	codexHome string

	events chan core.Event

	ctx    context.Context
	cancel context.CancelFunc

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	procMu  sync.Mutex
	writeMu sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponseEnvelope

	threadID atomic.Value
	alive    atomic.Bool

	closeOnce sync.Once
	wg        sync.WaitGroup

	stateMu     sync.Mutex
	pendingMsgs []string
	currentTurn string
}

func newAppServerSession(ctx context.Context, url, workDir, model, effort, mode, resumeID string, extraEnv []string, codexHome string) (*appServerSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &appServerSession{
		url:       url,
		workDir:   workDir,
		model:     model,
		effort:    effort,
		mode:      mode,
		extraEnv:  append([]string(nil), extraEnv...),
		codexHome: strings.TrimSpace(codexHome),
		events:    make(chan core.Event, 128),
		ctx:       sessionCtx,
		cancel:    cancel,
		pending:   make(map[int64]chan rpcResponseEnvelope),
	}
	s.alive.Store(true)

	if err := s.connect(); err != nil {
		cancel()
		return nil, err
	}

	if err := s.initialize(); err != nil {
		_ = s.Close()
		return nil, err
	}

	if err := s.ensureThread(resumeID); err != nil {
		_ = s.Close()
		return nil, err
	}

	return s, nil
}

func (s *appServerSession) connect() error {
	args := []string{"app-server"}
	if strings.TrimSpace(s.url) != "" {
		args = append(args, "--listen", strings.TrimSpace(s.url))
	}
	cmd := exec.CommandContext(s.ctx, "codex", args...)
	cmd.Dir = s.workDir
	env := append([]string(nil), s.extraEnv...)
	if s.codexHome != "" {
		env = append(env, "CODEX_HOME="+s.codexHome)
	}
	if len(env) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("codex app-server start: %w", err)
	}

	s.procMu.Lock()
	s.cmd = cmd
	s.stdin = stdin
	s.procMu.Unlock()

	slog.Info("codex app-server session started", "transport", "stdio", "pid", cmd.Process.Pid, "work_dir", s.workDir)

	s.wg.Add(3)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()
	return nil
}

func (s *appServerSession) initialize() error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "cc-connect-codex-agent",
			"title":   "CC Connect Codex Agent",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
			"optOutNotificationMethods": []string{
				"command/exec/outputDelta",
				"item/agentMessage/delta",
				"item/plan/delta",
				"item/fileChange/outputDelta",
				"item/reasoning/summaryTextDelta",
				"item/reasoning/textDelta",
			},
		},
	}

	var resp initResponse
	if err := s.request("initialize", params, &resp); err != nil {
		return fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := s.notify("initialized", nil); err != nil {
		return fmt.Errorf("codex app-server initialized notify: %w", err)
	}
	return nil
}

func (s *appServerSession) ensureThread(resumeID string) error {
	if resumeID != "" && resumeID != core.ContinueSession {
		params := s.threadRequestParams()
		params["threadId"] = resumeID
		params["persistExtendedHistory"] = true

		var resp threadResumeResponse
		if err := s.request("thread/resume", params, &resp); err != nil {
			return err
		}
		if resp.Thread.ID == "" {
			return fmt.Errorf("codex app-server resume returned empty thread id")
		}
		s.threadID.Store(resp.Thread.ID)
		slog.Info("codex app-server thread resumed", "thread_id", resp.Thread.ID)
		return nil
	}

	var resp threadStartResponse
	if err := s.request("thread/start", s.threadRequestParams(), &resp); err != nil {
		return err
	}
	if resp.Thread.ID == "" {
		return fmt.Errorf("codex app-server start returned empty thread id")
	}
	s.threadID.Store(resp.Thread.ID)
	slog.Info("codex app-server thread started", "thread_id", resp.Thread.ID)
	return nil
}

func (s *appServerSession) threadRequestParams() map[string]any {
	params := map[string]any{
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
	}
	if s.model != "" {
		params["model"] = s.model
	}
	if approval, sandbox := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
		if sandbox != "" {
			params["sandbox"] = sandbox
		}
	}
	return params
}

func appServerModeSettings(mode string) (approval string, sandbox string) {
	switch normalizeMode(mode) {
	case "auto-edit", "full-auto":
		return "never", "workspace-write"
	case "yolo":
		return "never", "danger-full-access"
	default:
		return "on-request", "read-only"
	}
}

func (s *appServerSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}

	threadID := s.CurrentSessionID()
	if threadID == "" {
		return fmt.Errorf("codex app-server thread id is empty")
	}

	input := make([]map[string]any, 0, 1+len(imagePaths))
	input = append(input, map[string]any{
		"type":          "text",
		"text":          prompt,
		"text_elements": []any{},
	})
	for _, path := range imagePaths {
		input = append(input, map[string]any{
			"type": "localImage",
			"path": path,
		})
	}

	params := map[string]any{
		"threadId": threadID,
		"input":    input,
	}
	if s.model != "" {
		params["model"] = s.model
	}
	if s.effort != "" {
		params["effort"] = s.effort
	}
	if approval, _ := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
	}

	var resp turnStartResponse
	if err := s.request("turn/start", params, &resp); err != nil {
		return fmt.Errorf("codex app-server turn/start: %w", err)
	}
	if resp.Turn.ID == "" {
		return fmt.Errorf("codex app-server turn/start returned empty turn id")
	}

	s.stateMu.Lock()
	s.currentTurn = resp.Turn.ID
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	return nil
}

func (s *appServerSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("codex app-server: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := codexImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("codex app-server: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if strings.TrimSpace(prompt) == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func (s *appServerSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *appServerSession) Events() <-chan core.Event {
	return s.events
}

func (s *appServerSession) CurrentSessionID() string {
	v, _ := s.threadID.Load().(string)
	return v
}

func (s *appServerSession) Alive() bool {
	return s.alive.Load()
}

func (s *appServerSession) Close() error {
	s.alive.Store(false)
	s.cancel()

	s.procMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.procMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	s.closeOnce.Do(func() {
		close(s.events)
	})
	return nil
}

func (s *appServerSession) readLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	scanBuf := make([]byte, 0, 64*1024)
	const maxLineSize = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(scanBuf, maxLineSize)

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		data := scanner.Bytes()

		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			slog.Debug("codex app-server: invalid JSON", "error", err)
			continue
		}

		if _, ok := probe["id"]; ok {
			var resp rpcResponseEnvelope
			if err := json.Unmarshal(data, &resp); err != nil {
				slog.Debug("codex app-server: bad response envelope", "error", err)
				continue
			}
			s.handleResponse(resp)
			continue
		}

		var notif rpcNotificationEnvelope
		if err := json.Unmarshal(data, &notif); err != nil {
			slog.Debug("codex app-server: bad notification envelope", "error", err)
			continue
		}
		s.handleNotification(notif.Method, notif.Params)
	}

	err := scanner.Err()
	if err != nil {
		if s.ctx.Err() == nil && !errors.Is(err, io.EOF) {
			slog.Warn("codex app-server read failed", "error", err)
			if errors.Is(err, bufio.ErrTooLong) {
				s.emitError(fmt.Errorf("codex app-server line exceeds max size (%d bytes): %w", maxLineSize, err))
			} else {
				s.emitError(fmt.Errorf("codex app-server connection closed: %w", err))
			}
		}
		s.alive.Store(false)
		s.rejectPending(err)
		return
	}

	s.alive.Store(false)
	s.rejectPending(io.EOF)
}

func (s *appServerSession) stderrLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		slog.Debug("codex app-server stderr", "line", line)
	}
	if err := scanner.Err(); err != nil && s.ctx.Err() == nil {
		slog.Debug("codex app-server stderr read failed", "error", err)
	}
}

func (s *appServerSession) waitLoop() {
	defer s.wg.Done()

	s.procMu.Lock()
	cmd := s.cmd
	s.procMu.Unlock()
	if cmd == nil {
		return
	}

	err := cmd.Wait()
	if s.ctx.Err() == nil && err != nil {
		slog.Warn("codex app-server exited unexpectedly", "error", err)
		s.emitError(fmt.Errorf("codex app-server exited: %w", err))
	}
	s.alive.Store(false)
	if err == nil {
		err = io.EOF
	}
	s.rejectPending(err)
}

func (s *appServerSession) handleResponse(resp rpcResponseEnvelope) {
	id, ok := rpcIDToInt64(resp.ID)
	if !ok {
		return
	}

	s.pendingMu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.pendingMu.Unlock()

	if ch == nil {
		return
	}

	select {
	case ch <- resp:
	default:
	}
}

func (s *appServerSession) handleNotification(method string, paramsRaw json.RawMessage) {
	switch method {
	case "turn/started":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.stateMu.Lock()
			s.currentTurn = notif.Turn.ID
			s.pendingMsgs = s.pendingMsgs[:0]
			s.stateMu.Unlock()
		}

	case "item/started":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.handleItemStarted(notif.Item)
		}

	case "item/completed":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.handleItemCompleted(notif.Item)
		}

	case "turn/completed":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.flushPendingAsText()
			s.emit(core.Event{
				Type:      core.EventResult,
				SessionID: s.CurrentSessionID(),
				Done:      true,
			})
		}

	case "error":
		var notif errorNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil && strings.TrimSpace(notif.Message) != "" {
			s.emitError(fmt.Errorf("%s", notif.Message))
		}
	}
}

func (s *appServerSession) handleItemStarted(item map[string]any) {
	itemType, _ := item["type"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "agentMessage", "reasoning", "userMessage", "plan", "hookPrompt", "contextCompaction":
		return
	}

	s.flushPendingAsThinking()

	switch itemType {
	case "commandExecution":
		command, _ := item["command"].(string)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: command})

	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		name := strings.Trim(strings.Join([]string{server, tool}, ":"), ":")
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "MCP", ToolInput: name + "\n" + appServerJSON(item["arguments"])})

	case "webSearch":
		query, _ := item["query"].(string)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "WebSearch", ToolInput: query})

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		s.emit(core.Event{Type: core.EventToolUse, ToolName: tool, ToolInput: appServerJSON(item["arguments"])})

	case "fileChange":
		s.emit(core.Event{Type: core.EventToolUse, ToolName: "Patch", ToolInput: appServerJSON(item["changes"])})
	}
}

func (s *appServerSession) handleItemCompleted(item map[string]any) {
	itemType, _ := item["type"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "reasoning":
		text := appServerReasoningText(item)
		if text != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text})
		}

	case "agentMessage":
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) != "" {
			s.stateMu.Lock()
			s.pendingMsgs = append(s.pendingMsgs, text)
			s.stateMu.Unlock()
		}

	case "commandExecution":
		command, _ := item["command"].(string)
		status, _ := item["status"].(string)
		output, _ := item["aggregatedOutput"].(string)
		exitCode, hasExitCode := toInt(item["exitCode"])
		var exitCodePtr *int
		if hasExitCode {
			exitCodePtr = &exitCode
		}
		success := appServerToolSuccess(status, exitCodePtr)
		s.emit(core.Event{
			Type:         core.EventToolResult,
			ToolName:     "Bash",
			ToolInput:    command,
			ToolResult:   truncate(strings.TrimSpace(output), 500),
			ToolStatus:   strings.TrimSpace(status),
			ToolExitCode: exitCodePtr,
			ToolSuccess:  &success,
		})

	case "mcpToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerJSON(item["result"])
		if errText := appServerJSON(item["error"]); strings.TrimSpace(errText) != "" && result == "" {
			result = errText
		}
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
		})

	case "webSearch":
		query, _ := item["query"].(string)
		s.emit(core.Event{
			Type:       core.EventToolResult,
			ToolName:   "WebSearch",
			ToolResult: truncate(strings.TrimSpace(query), 500),
		})

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerDynamicToolText(item["contentItems"])
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
		})
	}
}

func appServerReasoningText(item map[string]any) string {
	var parts []string
	if summary, ok := item["summary"].([]any); ok {
		for _, entry := range summary {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		if content, ok := item["content"].([]any); ok {
			for _, entry := range content {
				if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func appServerDynamicToolText(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return appServerJSON(raw)
	}
	var parts []string
	for _, entry := range items {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := m["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return appServerJSON(raw)
	}
	return strings.Join(parts, "\n")
}

func appServerToolSuccess(status string, exitCode *int) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if exitCode != nil {
		return *exitCode == 0
	}
	return s == "completed" || s == "success" || s == "succeeded" || s == "ok"
}

func appServerJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "{}" || s == "[]" || s == `""` {
		return ""
	}
	return s
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func rpcIDToInt64(v any) (int64, bool) {
	switch id := v.(type) {
	case float64:
		return int64(id), true
	case int64:
		return id, true
	case int:
		return int64(id), true
	case json.Number:
		i, err := id.Int64()
		return i, err == nil
	}
	return 0, false
}

func (s *appServerSession) flushPendingAsThinking() {
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text})
		}
	}
}

func (s *appServerSession) flushPendingAsText() {
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventText, Content: text})
		}
	}
}

func (s *appServerSession) emit(event core.Event) {
	select {
	case s.events <- event:
	default:
		slog.Warn("codex appserver: event channel full, dropping event", "type", event.Type)
	}
}

func (s *appServerSession) emitError(err error) {
	if err == nil {
		return
	}
	s.emit(core.Event{Type: core.EventError, Error: err})
}

func (s *appServerSession) rejectPending(err error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for id, ch := range s.pending {
		delete(s.pending, id)
		select {
		case ch <- rpcResponseEnvelope{ID: id, Error: &rpcError{Message: err.Error()}}:
		default:
		}
	}
}

func (s *appServerSession) request(method string, params any, out any) error {
	id := s.nextID.Add(1)
	ch := make(chan rpcResponseEnvelope, 1)

	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	if err := s.writeJSON(payload); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("%s", strings.TrimSpace(resp.Error.Message))
		}
		if out != nil {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-time.After(120 * time.Second):
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return fmt.Errorf("%s timed out", method)
	}
}

func (s *appServerSession) notify(method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	return s.writeJSON(payload)
}

func (s *appServerSession) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("codex app-server encode: %w", err)
	}

	s.procMu.Lock()
	stdin := s.stdin
	s.procMu.Unlock()
	if stdin == nil {
		return fmt.Errorf("codex app-server connection is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("codex app-server write: %w", err)
	}
	return nil
}
