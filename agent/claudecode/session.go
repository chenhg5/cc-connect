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

// claudeSession manages a long-running Claude Code process using
// --input-format stream-json and --permission-prompt-tool stdio.
//
// In "auto" mode, permission requests are auto-approved internally
// (avoiding --dangerously-skip-permissions which fails under root).
type claudeSession struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdinMu     sync.Mutex
	events      chan core.Event
	sessionID   atomic.Value // stores string
	autoApprove bool         // auto mode: approve all permission requests
	workDir     string
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	alive       atomic.Bool
}

func newClaudeSession(ctx context.Context, workDir, model, sessionID, mode string, allowedTools []string, extraEnv []string) (*claudeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--input-format", "stream-json",
		"--permission-prompt-tool", "stdio",
	}

	if mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowedTools, ","))
	}

	slog.Debug("claudeSession: starting", "args", args, "dir", workDir, "mode", mode)

	cmd := exec.CommandContext(sessionCtx, "claude", args...)
	cmd.Dir = workDir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

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
		cmd:         cmd,
		stdin:       stdin,
		events:      make(chan core.Event, 64),
		autoApprove: mode == "bypassPermissions",
		workDir:     workDir,
		ctx:         sessionCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
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
				cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			}
		}
		close(cs.events)
		close(cs.done)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

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
		cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
	}
}

func (cs *claudeSession) handleSystem(raw map[string]any) {
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
		cs.events <- core.Event{Type: core.EventText, SessionID: sid}
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
			inputSummary := summarizeInput(toolName, item["input"])
			cs.events <- core.Event{
				Type:      core.EventToolUse,
				ToolName:  toolName,
				ToolInput: inputSummary,
			}
		case "thinking":
			if thinking, ok := item["thinking"].(string); ok && thinking != "" {
				cs.events <- core.Event{
					Type:    core.EventThinking,
					Content: thinking,
				}
			}
		case "text":
			if text, ok := item["text"].(string); ok && text != "" {
				cs.events <- core.Event{
					Type:    core.EventText,
					Content: text,
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
			isError, _ := item["is_error"].(bool)
			if isError {
				result, _ := item["content"].(string)
				slog.Debug("claudeSession: tool error", "content", result)
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
	cs.events <- core.Event{
		Type:      core.EventResult,
		Content:   content,
		SessionID: cs.CurrentSessionID(),
		Done:      true,
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

	// Auto mode: approve immediately without asking the user
	if cs.autoApprove {
		slog.Debug("claudeSession: auto-approving", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: input,
		})
		return
	}

	slog.Info("claudeSession: permission request", "request_id", requestID, "tool", toolName)
	cs.events <- core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     toolName,
		ToolInput:    summarizeInput(toolName, input),
		ToolInputRaw: input,
	}
}

// Send writes a user message (with optional images) to the Claude process stdin.
// Images are saved to local temp files first, then sent as base64 in the
// multimodal content array. File paths are also mentioned in the text prompt
// as a fallback so Claude Code can read them with its built-in tools.
func (cs *claudeSession) Send(prompt string, images []core.ImageAttachment) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	if len(images) == 0 {
		return cs.writeJSON(map[string]any{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": prompt},
		})
	}

	// Save images to local files and build multimodal content
	imgDir := filepath.Join(cs.workDir, ".cc-connect", "images")
	os.MkdirAll(imgDir, 0o755)

	var parts []map[string]any
	var savedPaths []string
	for i, img := range images {
		ext := extFromMime(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
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

	// Build text part: user prompt + file path references as fallback
	textPart := prompt
	if textPart == "" {
		textPart = "Please analyze the attached image(s)."
	}
	if len(savedPaths) > 0 {
		textPart += "\n\n(Images also saved locally: " + strings.Join(savedPaths, ", ") + ")"
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
	<-cs.done
	return nil
}
