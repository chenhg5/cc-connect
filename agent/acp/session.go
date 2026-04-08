package acp

import (
	"bytes"
	"context"
	"encoding/json"
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

type acpSession struct {
	workDir string
	events  chan core.Event
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	alive   atomic.Bool

	cmd *exec.Cmd
	tr  *transport

	acpSessMu sync.RWMutex
	acpSessID string

	sendMu sync.Mutex

	permMu   sync.Mutex
	permByID map[string]permState

	toolInputMu   sync.Mutex
	toolInputByID map[string]string // toolCallId -> summarized tool input
}

type permState struct {
	RPCID   json.RawMessage
	Options []permissionOption
}

func newACPSession(
	ctx context.Context,
	command string,
	args []string,
	extraEnv []string,
	workDir string,
	resumeSessionID string,
	authMethod string,
) (*acpSession, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	s := &acpSession{
		workDir:   absWorkDir,
		events:    make(chan core.Event, 128),
		ctx:       sessionCtx,
		cancel:        cancel,
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
		acpSessID: resumeSessionID,
	}
	s.alive.Store(true)

	cmd := exec.CommandContext(sessionCtx, command, args...)
	cmd.Dir = absWorkDir
	cmd.Env = core.MergeEnv(os.Environ(), extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)

	s.cmd = cmd
	s.tr = newTransport(stdout, stdin, s.onNotification, s.onServerRequest)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("acp: start %s: %w", command, err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.tr.readLoop(sessionCtx)
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := stderrBuf.String()
			if msg != "" {
				slog.Error("acp: process exited", "error", waitErr, "stderr", msg)
				s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", strings.TrimSpace(msg))})
			} else {
				slog.Debug("acp: process exited", "error", waitErr)
			}
		}
		s.alive.Store(false)
	}()

	if err := s.handshake(resumeSessionID, authMethod); err != nil {
		_ = s.Close()
		return nil, err
	}

	return s, nil
}

func (s *acpSession) handshake(resumeSessionID string, authMethod string) error {
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "cc-connect",
			"version": "1.0.0",
		},
	}
	res, err := s.tr.call(s.ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("acp: initialize: %w", err)
	}

	var initOut struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(res, &initOut); err != nil {
		return fmt.Errorf("acp: parse initialize result: %w", err)
	}
	slog.Debug("acp: initialized", "protocol", initOut.ProtocolVersion, "load_session", initOut.AgentCapabilities.LoadSession)

	if strings.TrimSpace(authMethod) != "" {
		if _, err := s.tr.call(s.ctx, "authenticate", map[string]any{
			"methodId": authMethod,
		}); err != nil {
			return fmt.Errorf("acp: authenticate (%s): %w", authMethod, err)
		}
		slog.Debug("acp: authenticated", "method_id", authMethod)
	}

	wantResume := resumeSessionID != "" && resumeSessionID != core.ContinueSession
	if wantResume && initOut.AgentCapabilities.LoadSession {
		loadParams := map[string]any{
			"sessionId":  resumeSessionID,
			"cwd":        s.workDir,
			"mcpServers": []any{},
		}
		loadRes, err := s.tr.call(s.ctx, "session/load", loadParams)
		if err != nil {
			slog.Warn("acp: session/load failed, starting new session", "error", err)
		} else {
			var lr struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(loadRes, &lr) == nil && lr.SessionID != "" {
				s.setACPSessionID(lr.SessionID)
				return nil
			}
		}
	}

	newParams := map[string]any{
		"cwd":        s.workDir,
		"mcpServers": []any{},
	}
	newRes, err := s.tr.call(s.ctx, "session/new", newParams)
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	var sn struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newRes, &sn); err != nil {
		return fmt.Errorf("acp: parse session/new: %w", err)
	}
	if sn.SessionID == "" {
		return fmt.Errorf("acp: session/new: empty sessionId")
	}
	s.setACPSessionID(sn.SessionID)
	return nil
}

func (s *acpSession) setACPSessionID(id string) {
	s.acpSessMu.Lock()
	s.acpSessID = id
	s.acpSessMu.Unlock()
}

func (s *acpSession) currentACPSessionID() string {
	s.acpSessMu.RLock()
	defer s.acpSessMu.RUnlock()
	return s.acpSessID
}

func (s *acpSession) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		slog.Debug("acp: notification", "method", method)
		return
	}
	s.cacheToolCallInput(params)
	sid := s.currentACPSessionID()
	for _, ev := range mapSessionUpdate(sid, params) {
		s.emit(ev)
	}
}

// cacheToolCallInput extracts and caches rawInput from tool_call and tool_call_update
// session updates so that handlePermissionRequest can look it up by toolCallId.
// OpenCode ACP bug (#7370): rawInput is empty in tool_call and request_permission,
// but populated in tool_call_update. We cache from both sources.
func (s *acpSession) cacheToolCallInput(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	switch head.SessionUpdate {
	case "tool_call":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		s.toolInputMu.Lock()
		input := summarizeACPToolInput(tc.Kind, tc.RawInput)
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call input", "toolCallId", tc.ToolCallID, "kind", tc.Kind, "input", input)
	case "tool_call_update":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		input := summarizeACPToolInput("", tc.RawInput)
		if input == "" {
			return
		}
		s.toolInputMu.Lock()
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call_update input", "toolCallId", tc.ToolCallID, "input", input)
	}
}

func (s *acpSession) onServerRequest(method string, id json.RawMessage, params json.RawMessage) {
	switch method {
	case "session/request_permission":
		s.handlePermissionRequest(id, params)
	case "cursor/ask_question", "cursor/create_plan", "cursor/update_todos", "cursor/task", "cursor/generate_image":
		// Cursor CLI extensions — acknowledge so tool flows do not block; IM UX is limited for these.
		slog.Debug("acp: cursor extension request (no-op ack)", "method", method)
		_ = s.tr.respondSuccess(id, map[string]any{})
	default:
		if strings.HasPrefix(method, "cursor/") {
			slog.Debug("acp: unknown cursor extension, ack empty", "method", method)
			_ = s.tr.respondSuccess(id, map[string]any{})
			return
		}
		slog.Info("acp: unhandled server request", "method", method)
		_ = s.tr.respondError(id, -32601, "method not implemented")
	}
}

func (s *acpSession) handlePermissionRequest(id json.RawMessage, params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		_ = s.tr.respondError(id, -32602, "invalid params")
		return
	}
	slog.Info("acp: permission request raw params", "params", string(params))
	reqKey := jsonIDKey(id)
	toolName := p.ToolCall.Title
	if toolName == "" {
		toolName = p.ToolCall.Kind
	}
	if toolName == "" {
		toolName = "permission"
	}

	s.permMu.Lock()
	s.permByID[reqKey] = permState{RPCID: id, Options: p.Options}
	s.permMu.Unlock()

	rawTool := map[string]any{}
	_ = json.Unmarshal(params, &rawTool)

	// OpenCode ACP bug (#7370): rawInput in request_permission is always {},
	// but tool_call_update (which arrives right after) has the real input.
	// Emit in a goroutine so we don't block the read loop, and wait briefly
	// for tool_call_update to populate the cache.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for i := 0; i < 10; i++ {
			s.toolInputMu.Lock()
			toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
			s.toolInputMu.Unlock()
			if toolInput != "" {
				break
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		s.toolInputMu.Lock()
		toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
		s.toolInputMu.Unlock()
		if toolInput == "" {
			toolInput = summarizeACPToolInput(p.ToolCall.Kind, p.ToolCall.RawInput)
		}
		if toolInput == "" {
			toolInput = p.ToolCall.Title
		}
		if toolInput == "" {
			toolInput = p.ToolCall.ToolCallID
		}

		slog.Info("acp: permission request", "request_id", reqKey, "tool", toolName, "input", toolInput)
		s.emit(core.Event{
			Type:         core.EventPermissionRequest,
			RequestID:    reqKey,
			ToolName:     toolName,
			ToolInput:    toolInput,
			ToolInputRaw: rawTool,
			SessionID:    s.currentACPSessionID(),
		})
	}()
}

func (s *acpSession) emit(ev core.Event) {
	if ev.SessionID == "" {
		ev.SessionID = s.currentACPSessionID()
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *acpSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	filePaths := core.SaveFilesToDisk(s.workDir, files)
	prompt = core.AppendFileRefs(prompt, filePaths)
	if len(images) > 0 {
		prompt = s.appendImageRefs(prompt, images)
	}

	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no agent session id")
	}

	promptBlocks := []any{
		map[string]any{"type": "text", "text": prompt},
	}
	params := map[string]any{
		"sessionId": sid,
		"prompt":    promptBlocks,
	}

	_, err := s.tr.call(s.ctx, "session/prompt", params)
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: err})
		return fmt.Errorf("acp: session/prompt: %w", err)
	}

	// Text was streamed via session/update; engine aggregates EventText.
	s.emit(core.Event{
		Type:      core.EventResult,
		SessionID: sid,
		Done:      true,
	})
	return nil
}

func (s *acpSession) appendImageRefs(prompt string, images []core.ImageAttachment) string {
	attachDir := filepath.Join(s.workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("acp: mkdir attachments failed", "error", err)
		return prompt
	}
	var paths []string
	for i, img := range images {
		ext := ".bin"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		case "image/png", "":
			ext = ".png"
		}
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("acp: save image failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	if len(paths) == 0 {
		return prompt
	}
	if prompt == "" {
		prompt = "User sent image(s)."
	}
	return prompt + "\n\n(Image files saved locally: " + strings.Join(paths, ", ") + ")"
}

func (s *acpSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.permMu.Lock()
	st, ok := s.permByID[requestID]
	if ok {
		delete(s.permByID, requestID)
	}
	s.permMu.Unlock()
	if !ok {
		return fmt.Errorf("acp: unknown permission request %q", requestID)
	}

	allow := strings.EqualFold(result.Behavior, "allow")
	optID := pickPermissionOptionID(allow, st.Options)
	if allow && optID == "" {
		slog.Warn("acp: allow requested but agent sent no options", "request_id", requestID)
		return s.tr.respondError(st.RPCID, -32603, "no permission options from agent")
	}
	res := buildPermissionResult(allow, optID)

	slog.Debug("acp: permission response", "request_id", requestID, "allow", allow, "option_id", optID)
	return s.tr.respondSuccess(st.RPCID, res)
}

func (s *acpSession) Events() <-chan core.Event {
	return s.events
}

func (s *acpSession) CurrentSessionID() string {
	return s.currentACPSessionID()
}

func (s *acpSession) Alive() bool {
	return s.alive.Load()
}

func (s *acpSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("acp: close timed out waiting for I/O loop")
	}
	close(s.events)
	return nil
}

// summarizeACPToolInput extracts a human-readable summary from ACP tool rawInput.
func summarizeACPToolInput(kind string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	if len(m) == 0 {
		return ""
	}
	switch strings.ToLower(kind) {
	case "bash", "shell", "terminal", "execute":
		if cmd, ok := m["command"].(string); ok {
			if desc, ok := m["description"].(string); ok && desc != "" {
				return "# " + desc + "\n" + cmd
			}
			return cmd
		}
	case "read", "write", "edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
		if fp, ok := m["path"].(string); ok {
			return fp
		}
	}
	// Fallback: try extracting command with description before formatting JSON.
	if cmd, ok := m["command"].(string); ok {
		if desc, ok := m["description"].(string); ok && desc != "" {
			return "# " + desc + "\n" + cmd
		}
		return cmd
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}
