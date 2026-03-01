package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// geminiSession manages multi-turn conversations with the Gemini CLI.
// Each Send() launches a new `gemini -p ... --output-format stream-json` process
// with --resume for conversation continuity.
type geminiSession struct {
	cmd      string
	workDir  string
	model    string
	mode     string
	extraEnv []string
	events   chan core.Event
	chatID   atomic.Value // stores string — Gemini session ID
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool
}

func newGeminiSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*geminiSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	gs := &geminiSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	gs.alive.Store(true)

	if resumeID != "" {
		gs.chatID.Store(resumeID)
	}

	return gs, nil
}

func (gs *geminiSession) Send(prompt string, images []core.ImageAttachment) error {
	if !gs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	// Gemini CLI supports @file references for images; save to temp files
	var imageRefs []string
	if len(images) > 0 {
		tmpDir := os.TempDir()
		for i, img := range images {
			ext := ".png"
			switch img.MimeType {
			case "image/jpeg":
				ext = ".jpg"
			case "image/gif":
				ext = ".gif"
			case "image/webp":
				ext = ".webp"
			}
			fname := fmt.Sprintf("cc-connect-img-%d%s", i, ext)
			fpath := fmt.Sprintf("%s/%s", tmpDir, fname)
			if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
				slog.Warn("geminiSession: failed to save image", "error", err)
				continue
			}
			imageRefs = append(imageRefs, fpath)
		}
	}

	chatID := gs.CurrentSessionID()
	isResume := chatID != ""

	args := []string{
		"--output-format", "stream-json",
	}

	switch gs.mode {
	case "yolo":
		args = append(args, "-y")
	case "auto_edit":
		args = append(args, "--approval-mode", "auto_edit")
	case "plan":
		args = append(args, "--approval-mode", "plan")
	}

	if isResume {
		args = append(args, "--resume", chatID)
	}
	if gs.model != "" {
		args = append(args, "-m", gs.model)
	}

	// Build the prompt with image file references
	fullPrompt := prompt
	if len(imageRefs) > 0 {
		fullPrompt = strings.Join(imageRefs, " ") + " " + prompt
	}

	args = append(args, "-p", fullPrompt)

	slog.Debug("geminiSession: launching", "resume", isResume, "args", args)

	cmd := exec.CommandContext(gs.ctx, gs.cmd, args...)
	cmd.Dir = gs.workDir
	env := os.Environ()
	if len(gs.extraEnv) > 0 {
		env = append(env, gs.extraEnv...)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("geminiSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("geminiSession: start: %w", err)
	}

	gs.wg.Add(1)
	go gs.readLoop(cmd, stdout, &stderrBuf, imageRefs)

	return nil
}

func (gs *geminiSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, tempImages []string) {
	defer gs.wg.Done()
	defer func() {
		// Clean up temp image files
		for _, f := range tempImages {
			os.Remove(f)
		}
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("geminiSession: process failed", "error", err, "stderr", stderrMsg)
				gs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			}
		}
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
			slog.Debug("geminiSession: non-JSON line", "line", line)
			continue
		}

		gs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("geminiSession: scanner error", "error", err)
		gs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
	}
}

// Gemini CLI stream-json event types:
//   init       — session_id, model
//   message    — role (user/assistant), content, delta
//   tool_use   — tool_name, tool_id, parameters
//   tool_result — tool_id, status, output, error
//   error      — severity, message
//   result     — status, stats (final event)
func (gs *geminiSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "init":
		gs.handleInit(raw)
	case "message":
		gs.handleMessage(raw)
	case "tool_use":
		gs.handleToolUse(raw)
	case "tool_result":
		gs.handleToolResult(raw)
	case "error":
		gs.handleError(raw)
	case "result":
		gs.handleResult(raw)
	default:
		slog.Debug("geminiSession: unhandled event", "type", eventType)
	}
}

func (gs *geminiSession) handleInit(raw map[string]any) {
	sid, _ := raw["session_id"].(string)
	model, _ := raw["model"].(string)

	if sid != "" {
		gs.chatID.Store(sid)
		slog.Debug("geminiSession: session init", "session_id", sid, "model", model)

		gs.events <- core.Event{
			Type:      core.EventText,
			SessionID: sid,
			Content:   "",
			ToolName:  model,
		}
	}
}

func (gs *geminiSession) handleMessage(raw map[string]any) {
	role, _ := raw["role"].(string)
	content, _ := raw["content"].(string)
	delta, _ := raw["delta"].(bool)

	if role == "user" {
		return
	}

	// assistant message (may be delta or full)
	if content != "" {
		_ = delta // both delta and full messages are streamed as text events
		gs.events <- core.Event{
			Type:    core.EventText,
			Content: content,
		}
	}
}

func (gs *geminiSession) handleToolUse(raw map[string]any) {
	toolName, _ := raw["tool_name"].(string)
	toolID, _ := raw["tool_id"].(string)
	params, _ := raw["parameters"].(map[string]any)

	input := formatToolParams(toolName, params)

	slog.Debug("geminiSession: tool_use", "tool", toolName, "id", toolID)
	gs.events <- core.Event{
		Type:      core.EventToolUse,
		ToolName:  toolName,
		ToolInput: input,
	}
}

func (gs *geminiSession) handleToolResult(raw map[string]any) {
	toolID, _ := raw["tool_id"].(string)
	status, _ := raw["status"].(string)
	output, _ := raw["output"].(string)

	slog.Debug("geminiSession: tool_result", "tool_id", toolID, "status", status)

	if status == "error" {
		errObj, _ := raw["error"].(map[string]any)
		if errObj != nil {
			errMsg, _ := errObj["message"].(string)
			if errMsg != "" {
				output = "Error: " + errMsg
			}
		}
	}

	if output != "" {
		gs.events <- core.Event{
			Type:     core.EventToolResult,
			ToolName: toolID,
			Content:  truncate(output, 500),
		}
	}
}

func (gs *geminiSession) handleError(raw map[string]any) {
	severity, _ := raw["severity"].(string)
	message, _ := raw["message"].(string)

	if message != "" {
		slog.Warn("geminiSession: error event", "severity", severity, "message", message)
		gs.events <- core.Event{
			Type:  core.EventError,
			Error: fmt.Errorf("[%s] %s", severity, message),
		}
	}
}

func (gs *geminiSession) handleResult(raw map[string]any) {
	status, _ := raw["status"].(string)

	var errMsg string
	if status == "error" {
		errObj, _ := raw["error"].(map[string]any)
		if errObj != nil {
			errMsg, _ = errObj["message"].(string)
		}
	}

	sid := gs.CurrentSessionID()

	if errMsg != "" {
		gs.events <- core.Event{
			Type:      core.EventResult,
			Content:   errMsg,
			SessionID: sid,
			Done:      true,
			Error:     fmt.Errorf("%s", errMsg),
		}
	} else {
		gs.events <- core.Event{
			Type:      core.EventResult,
			SessionID: sid,
			Done:      true,
		}
	}
}

// RespondPermission is a no-op — Gemini CLI permissions are handled via -y / --approval-mode flags.
func (gs *geminiSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (gs *geminiSession) Events() <-chan core.Event {
	return gs.events
}

func (gs *geminiSession) CurrentSessionID() string {
	v, _ := gs.chatID.Load().(string)
	return v
}

func (gs *geminiSession) Alive() bool {
	return gs.alive.Load()
}

func (gs *geminiSession) Close() error {
	gs.alive.Store(false)
	gs.cancel()
	gs.wg.Wait()
	close(gs.events)
	return nil
}

// formatToolParams extracts a human-readable summary from tool parameters.
func formatToolParams(toolName string, params map[string]any) string {
	if params == nil {
		return ""
	}

	switch toolName {
	case "shell", "run_shell_command":
		if cmd, ok := params["command"].(string); ok {
			return truncate(cmd, 200)
		}
	case "write_file", "read_file", "replace":
		if p, ok := params["file_path"].(string); ok {
			return p
		}
		if p, ok := params["path"].(string); ok {
			return p
		}
	case "web_fetch":
		if u, ok := params["url"].(string); ok {
			return truncate(u, 200)
		}
	case "google_web_search":
		if q, ok := params["query"].(string); ok {
			return truncate(q, 200)
		}
	}

	b, _ := json.Marshal(params)
	return truncate(string(b), 200)
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
