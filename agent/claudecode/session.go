package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

// claudeSettings represents the .claude/settings.json structure.
type claudeSettings struct {
	Hooks *claudeHooks `json:"hooks,omitempty"`
}

// claudeHooks represents the hooks section in settings.json.
type claudeHooks struct {
	UserPromptSubmit []claudeHookConfig `json:"UserPromptSubmit,omitempty"`
}

// claudeHookConfig represents a single hook configuration.
type claudeHookConfig struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // milliseconds; default 5000
}

// claudeSession manages a long-running Claude Code process using
// --input-format stream-json and --permission-prompt-tool stdio.
//
// In "auto" mode, permission requests are auto-approved internally
// (avoiding --dangerously-skip-permissions which fails under root).
type claudeSession struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdinMu         sync.Mutex
	events          chan core.Event
	sessionID       atomic.Value // stores string
	permissionMode  atomic.Value // stores string
	autoApprove     atomic.Bool
	acceptEditsOnly atomic.Bool
	dontAsk         atomic.Bool
	workDir         string
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	alive           atomic.Bool
	toolIDMap       map[string]string // tool_use_id -> tool_name mapping
	toolIDMapMu     sync.RWMutex
}

func newClaudeSession(ctx context.Context, workDir, model, sessionID, mode string, allowedTools, disallowedTools []string, extraEnv []string, platformPrompt string, disableVerbose bool) (*claudeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	args := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--permission-prompt-tool", "stdio",
	}
	if !disableVerbose {
		args = append(args, "--verbose")
	}

	if mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	switch sessionID {
	case "":
		// Truly fresh session — no resume, no continue.
	case core.ContinueSession:
		// --continue grabs the most recent session in the workspace, which
		// may belong to an active CLI terminal. Fork it so the platform
		// conversation gets its own independent context branch.
		args = append(args, "--continue", "--fork-session")
	default:
		// Resuming a known session ID — this is cc-connect's own session
		// from a previous connection, safe to resume directly.
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowedTools, ","))
	}
	if len(disallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(disallowedTools, ","))
	}

	if sysPrompt := core.AgentSystemPrompt(); sysPrompt != "" {
		if platformPrompt != "" {
			sysPrompt += "\n## Formatting\n" + platformPrompt + "\n"
		}
		args = append(args, "--append-system-prompt", sysPrompt)
	}

	slog.Debug("claudeSession: starting", "args", core.RedactArgs(args), "dir", workDir, "mode", mode)

	cmd := exec.CommandContext(sessionCtx, "claude", args...)
	cmd.Dir = workDir
	// Filter out CLAUDECODE env var to prevent "nested session" detection,
	// since cc-connect is a bridge, not a nested Claude Code session.
	env := filterEnv(os.Environ(), "CLAUDECODE")
	if len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: start: %w", err)
	}

	cs := &claudeSession{
		cmd:        cmd,
		stdin:      stdin,
		events:     make(chan core.Event, 64),
		workDir:    workDir,
		ctx:        sessionCtx,
		cancel:     cancel,
		done:       make(chan struct{}),
		toolIDMap:  make(map[string]string),
	}
	cs.setPermissionMode(mode)
	cs.sessionID.Store(sessionID)
	cs.alive.Store(true)

	go cs.readLoop(stdout, &stderrBuf)

	return cs, nil
}

func (cs *claudeSession) readLoop(stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer func() {
		cs.alive.Store(false)
		if err := cs.cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("claudeSession: process failed", "error", err, "stderr", stderrMsg)
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
		close(cs.events)
		close(cs.done)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("claudeSession: non-JSON line", "line", line)
			continue
		}

		eventType, _ := raw["type"].(string)
		slog.Debug("claudeSession: event", "type", eventType)

		switch eventType {
		case "system":
			cs.handleSystem(raw)
		case "assistant":
			cs.handleAssistant(raw)
		case "user":
			cs.handleUser(raw)
		case "result":
			cs.handleResult(raw)
		case "control_request":
			cs.handleControlRequest(raw)
		case "control_cancel_request":
			requestID, _ := raw["request_id"].(string)
			slog.Debug("claudeSession: permission cancelled", "request_id", requestID)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("claudeSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *claudeSession) handleSystem(raw map[string]any) {
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
		evt := core.Event{Type: core.EventText, SessionID: sid}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *claudeSession) handleAssistant(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	contentArr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		switch contentType {
		case "tool_use":
			toolName, _ := item["name"].(string)
			toolID, _ := item["id"].(string)
			if toolName == "AskUserQuestion" {
				continue
			}
			// Store tool_use_id -> tool_name mapping for tool_result lookup
			if toolID != "" && toolName != "" {
				cs.toolIDMapMu.Lock()
				cs.toolIDMap[toolID] = toolName
				cs.toolIDMapMu.Unlock()
			}
			inputSummary := summarizeInput(toolName, item["input"])
			evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: inputSummary}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				return
			}
		case "thinking":
			if thinking, ok := item["thinking"].(string); ok && thinking != "" {
				evt := core.Event{Type: core.EventThinking, Content: thinking}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		case "text":
			if text, ok := item["text"].(string); ok && text != "" {
				evt := core.Event{Type: core.EventText, Content: text}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}
}

func (cs *claudeSession) handleUser(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	contentArr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		if contentType == "tool_result" {
			toolUseID, _ := item["tool_use_id"].(string)
			isError, _ := item["is_error"].(bool)

			// Extract content (can be string or array)
			var result string
			switch c := item["content"].(type) {
			case string:
				result = c
			case []any:
				// Content can be an array of content blocks
				var parts []string
				for _, block := range c {
					if bm, ok := block.(map[string]any); ok {
						if text, ok := bm["text"].(string); ok {
							parts = append(parts, text)
						}
					}
				}
				result = strings.Join(parts, "\n")
			}

			// Look up tool name from stored mapping
			var toolName string
			if toolUseID != "" {
				cs.toolIDMapMu.RLock()
				toolName = cs.toolIDMap[toolUseID]
				cs.toolIDMapMu.RUnlock()
			}

			if isError {
				slog.Debug("claudeSession: tool error", "tool", toolName, "tool_use_id", toolUseID, "content", truncateStr(result, 200))
			}

			// Emit EventToolResult for UI progress display
			if toolName != "" {
				evt := core.Event{
					Type:       core.EventToolResult,
					ToolName:   toolName,
					ToolResult: truncateStr(result, 500),
				}
				if isError {
					evt.ToolStatus = "error"
				}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}
}

func (cs *claudeSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
	}

	var inputTokens, outputTokens int
	if usage, ok := raw["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(v)
		}
	}

	evt := core.Event{
		Type:         core.EventResult,
		Content:      content,
		SessionID:    cs.CurrentSessionID(),
		Done:         true,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

func (cs *claudeSession) handleControlRequest(raw map[string]any) {
	requestID, _ := raw["request_id"].(string)
	request, _ := raw["request"].(map[string]any)
	if request == nil {
		return
	}
	subtype, _ := request["subtype"].(string)
	if subtype != "can_use_tool" {
		slog.Debug("claudeSession: unknown control request subtype", "subtype", subtype)
		return
	}

	toolName, _ := request["tool_name"].(string)
	input, _ := request["input"].(map[string]any)

	if cs.autoApprove.Load() {
		slog.Debug("claudeSession: auto-approving", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: input,
		})
		return
	}
	if cs.dontAsk.Load() {
		slog.Debug("claudeSession: auto-denying", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior: "deny",
			Message:  "Permission mode is set to dontAsk.",
		})
		return
	}
	if cs.acceptEditsOnly.Load() && isClaudeEditTool(toolName) {
		slog.Debug("claudeSession: auto-approving edit tool", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: input,
		})
		return
	}

	slog.Info("claudeSession: permission request", "request_id", requestID, "tool", toolName)
	evt := core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     toolName,
		ToolInput:    summarizeInput(toolName, input),
		ToolInputRaw: input,
	}

	if toolName == "AskUserQuestion" {
		evt.Questions = parseUserQuestions(input)
	}

	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

// Send writes a user message (with optional images and files) to the Claude process stdin.
// Images are sent as base64 in the multimodal content array.
// Files are saved to local temp files and referenced in the text prompt
// so Claude Code can read them with its built-in tools.
func (cs *claudeSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	// Execute UserPromptSubmit hooks before sending (stream-json mode only)
	// Hooks receive {"message": prompt} via stdin, same as CLI mode.
	executeUserPromptSubmitHooks(cs.ctx, cs.workDir, prompt)

	if len(images) == 0 && len(files) == 0 {
		return cs.writeJSON(map[string]any{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": prompt},
		})
	}

	attachDir := filepath.Join(cs.workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("claudeSession: mkdir attachments failed", "error", err, "path", attachDir)
	}

	var parts []map[string]any
	var savedPaths []string

	// Save and encode images
	for i, img := range images {
		ext := extFromMime(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("claudeSession: save image failed", "error", err)
			continue
		}
		savedPaths = append(savedPaths, fpath)
		slog.Debug("claudeSession: image saved", "path", fpath, "size", len(img.Data))

		mimeType := img.MimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		parts = append(parts, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mimeType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}

	// Save files to disk so Claude Code can read them
	filePaths := core.SaveFilesToDisk(cs.workDir, files)

	// Build text part: user prompt + file path references
	textPart := prompt
	if textPart == "" && len(filePaths) > 0 {
		textPart = "Please analyze the attached file(s)."
	} else if textPart == "" {
		textPart = "Please analyze the attached image(s)."
	}
	if len(savedPaths) > 0 {
		textPart += "\n\n(Images also saved locally: " + strings.Join(savedPaths, ", ") + ")"
	}
	if len(filePaths) > 0 {
		textPart += "\n\n(Files saved locally, please read them: " + strings.Join(filePaths, ", ") + ")"
	}
	parts = append(parts, map[string]any{"type": "text", "text": textPart})

	return cs.writeJSON(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": parts},
	})
}

func extFromMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// RespondPermission writes a control_response to the Claude process stdin.
func (cs *claudeSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	var permResponse map[string]any
	if result.Behavior == "allow" {
		updatedInput := result.UpdatedInput
		if updatedInput == nil {
			updatedInput = make(map[string]any)
		}
		permResponse = map[string]any{
			"behavior":     "allow",
			"updatedInput": updatedInput,
		}
	} else {
		msg := result.Message
		if msg == "" {
			msg = "The user denied this tool use. Stop and wait for the user's instructions."
		}
		permResponse = map[string]any{
			"behavior": "deny",
			"message":  msg,
		}
	}

	controlResponse := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   permResponse,
		},
	}

	slog.Debug("claudeSession: permission response", "request_id", requestID, "behavior", result.Behavior)
	return cs.writeJSON(controlResponse)
}

func (cs *claudeSession) writeJSON(v any) error {
	cs.stdinMu.Lock()
	defer cs.stdinMu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := cs.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

func isClaudeEditTool(toolName string) bool {
	switch toolName {
	case "Edit", "Write", "NotebookEdit", "MultiEdit":
		return true
	default:
		return false
	}
}

func (cs *claudeSession) setPermissionMode(mode string) {
	cs.permissionMode.Store(mode)
	cs.autoApprove.Store(mode == "bypassPermissions")
	cs.acceptEditsOnly.Store(mode == "acceptEdits")
	cs.dontAsk.Store(mode == "dontAsk")
}

func (cs *claudeSession) SetLiveMode(mode string) bool {
	current, _ := cs.permissionMode.Load().(string)
	if mode == "auto" || mode == "plan" || current == "auto" || current == "plan" {
		return false
	}
	cs.setPermissionMode(mode)
	return true
}

func (cs *claudeSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *claudeSession) CurrentSessionID() string {
	v, _ := cs.sessionID.Load().(string)
	return v
}

func (cs *claudeSession) Alive() bool {
	return cs.alive.Load()
}

func (cs *claudeSession) Close() error {
	cs.cancel()

	select {
	case <-cs.done:
		return nil
	case <-time.After(8 * time.Second):
		slog.Warn("claudeSession: graceful close timed out, killing process")
		if cs.cmd != nil && cs.cmd.Process != nil {
			_ = cs.cmd.Process.Kill()
		}
		<-cs.done
		return nil
	}
}

// filterEnv returns a copy of env with entries matching the given key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// loadClaudeSettings loads .claude/settings.json from the workDir.
func loadClaudeSettings(workDir string) (*claudeSettings, error) {
	settingsPath := filepath.Join(workDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no settings file is not an error
		}
		return nil, fmt.Errorf("read settings.json: %w", err)
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	return &settings, nil
}

// executeUserPromptSubmitHooks runs all configured UserPromptSubmit hooks
// with the user's prompt passed via stdin as JSON.
func executeUserPromptSubmitHooks(ctx context.Context, workDir, prompt string) {
	settings, err := loadClaudeSettings(workDir)
	if err != nil {
		slog.Debug("claudeSession: failed to load settings for hooks", "error", err)
		return
	}
	if settings == nil || settings.Hooks == nil || len(settings.Hooks.UserPromptSubmit) == 0 {
		return // no hooks configured
	}

	// Prepare stdin payload
 stdinPayload, err := json.Marshal(map[string]string{"message": prompt})
	if err != nil {
		slog.Error("claudeSession: marshal hook stdin failed", "error", err)
		return
	}

	for _, hook := range settings.Hooks.UserPromptSubmit {
		if hook.Command == "" {
			continue
		}
		timeout := hook.Timeout
		if timeout <= 0 {
			timeout = 5000 // default 5 seconds
		}

		hookCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()

		cmd := exec.CommandContext(hookCtx, "sh", "-c", hook.Command)
		cmd.Dir = workDir
		cmd.Stdin = bytes.NewReader(stdinPayload)
		cmd.Stdout = nil // hooks output is not used
		cmd.Stderr = nil // hooks stderr is ignored

		if err := cmd.Run(); err != nil {
			if hookCtx.Err() == context.DeadlineExceeded {
				slog.Warn("claudeSession: hook timed out", "command", hook.Command, "timeout_ms", timeout)
			} else {
				slog.Warn("claudeSession: hook failed", "command", hook.Command, "error", err)
			}
			continue
		}
		slog.Debug("claudeSession: hook executed", "command", hook.Command)
	}
}

// truncateStr truncates a string to at most n runes.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Simple byte truncation is fine for logging/progress display
	return s[:n] + "..."
}

