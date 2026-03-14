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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	timeout  time.Duration
	extraEnv []string
	events   chan core.Event
	chatID   atomic.Value // stores string — Gemini session ID
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool

	pendingMsgs []string // buffered assistant messages awaiting classification
}

func newGeminiSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string, timeout time.Duration) (*geminiSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	gs := &geminiSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		timeout:  timeout,
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

func (gs *geminiSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) (err error) {
	if !gs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	// Gemini CLI supports @file references for images and files; save to temp files
	var imageRefs []string
	tmpDir := os.TempDir()
	if len(images) > 0 {
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
		fpath := filepath.Join(tmpDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
				slog.Warn("geminiSession: failed to save image", "error", err)
				continue
			}
			imageRefs = append(imageRefs, fpath)
		}
	}
	// Save files to temp and include as references
	var fileRefs []string
	for i, f := range files {
		fname := filepath.Base(f.FileName)
		if fname == "" || fname == "." || fname == ".." {
			fname = fmt.Sprintf("cc-connect-file-%d", i)
		}
		fpath := filepath.Join(tmpDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o644); err != nil {
			slog.Warn("geminiSession: failed to save file", "error", err)
			continue
		}
		fileRefs = append(fileRefs, fpath)
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

	// Build the prompt with image and file references
	fullPrompt := prompt
	if len(imageRefs) > 0 {
		fullPrompt = strings.Join(imageRefs, " ") + " " + fullPrompt
	}
	if len(fileRefs) > 0 {
		fullPrompt = strings.Join(fileRefs, " ") + " " + fullPrompt
	}

	args = append(args, "-p", fullPrompt)

	// Add timeout for each turn to prevent hanging processes
	var cancel context.CancelFunc
	var ctx context.Context
	if gs.timeout > 0 {
		ctx, cancel = context.WithTimeout(gs.ctx, gs.timeout)
	} else {
		ctx, cancel = context.WithCancel(gs.ctx)
	}

	// ensure cancel is called on early return errors
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	slog.Debug("geminiSession: launching", "resume", isResume, "args", core.RedactArgs(args))
	cmd := exec.CommandContext(ctx, gs.cmd, args...)
	// Set a short WaitDelay to ensure I/O goroutines don't block for long after the context is done
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = gs.workDir
	env := os.Environ()
	if len(gs.extraEnv) > 0 {
		env = core.MergeEnv(env, gs.extraEnv)
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

	started = true
	gs.wg.Add(1)
	go func() {
		defer cancel()
		gs.readLoop(ctx, cmd, stdout, &stderrBuf, append(imageRefs, fileRefs...))
	}()

	return nil
}

func (gs *geminiSession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, tempImages []string) {
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
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case gs.events <- evt:
				case <-gs.ctx.Done():
					return
				}
			}
		}
	}()

	// Unblock scanner if context is canceled
	go func() {
		<-ctx.Done()
		stdout.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		slog.Debug("geminiSession: raw", "line", truncate(line, 500))

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("geminiSession: non-JSON line", "line", line)
			continue
		}

		gs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("geminiSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	}
}

// Gemini CLI stream-json event types:
//
//	init       — session_id, model
//	message    — role (user/assistant), content, delta
//	tool_use   — tool_name, tool_id, parameters
//	tool_result — tool_id, status, output, error
//	error      — severity, message
//	result     — status, stats (final event)
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

		evt := core.Event{Type: core.EventText, SessionID: sid, Content: "", ToolName: model}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	}
}

func (gs *geminiSession) handleMessage(raw map[string]any) {
	role, _ := raw["role"].(string)
	content, _ := raw["content"].(string)

	if role == "user" || content == "" {
		return
	}

	// Delta messages are incremental streaming fragments — emit immediately
	// as EventText so engine's stream preview can update in real time.
	// Non-delta messages (complete text) are buffered for later classification
	// (thinking vs final text) based on what event follows.
	delta, _ := raw["delta"].(bool)
	if delta {
		evt := core.Event{Type: core.EventText, Content: content}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
		}
		return
	}

	gs.pendingMsgs = append(gs.pendingMsgs, content)
}

func (gs *geminiSession) handleToolUse(raw map[string]any) {
	gs.flushPendingAsThinking()

	toolName, _ := raw["tool_name"].(string)
	toolID, _ := raw["tool_id"].(string)
	params, _ := raw["parameters"].(map[string]any)

	input := formatToolParams(toolName, params)

	slog.Debug("geminiSession: tool_use", "tool", toolName, "id", toolID)
	evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
	select {
	case gs.events <- evt:
	case <-gs.ctx.Done():
		return
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
		evt := core.Event{Type: core.EventToolResult, ToolName: toolID, Content: truncate(output, 500)}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	}
}

func (gs *geminiSession) handleError(raw map[string]any) {
	severity, _ := raw["severity"].(string)
	message, _ := raw["message"].(string)

	if message != "" {
		slog.Warn("geminiSession: error event", "severity", severity, "message", message)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("[%s] %s", severity, message)}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	}
}

func (gs *geminiSession) handleResult(raw map[string]any) {
	gs.flushPendingAsText()

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
		evt := core.Event{Type: core.EventResult, Content: errMsg, SessionID: sid, Done: true, Error: fmt.Errorf("%s", errMsg)}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	} else {
		evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
			return
		}
	}
}

func (gs *geminiSession) flushPendingAsThinking() {
	if len(gs.pendingMsgs) == 0 {
		return
	}
	text := strings.Join(gs.pendingMsgs, "")
	gs.pendingMsgs = gs.pendingMsgs[:0]
	if text != "" {
		evt := core.Event{Type: core.EventThinking, Content: text}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
		}
	}
}

func (gs *geminiSession) flushPendingAsText() {
	if len(gs.pendingMsgs) == 0 {
		return
	}
	text := strings.Join(gs.pendingMsgs, "")
	gs.pendingMsgs = gs.pendingMsgs[:0]
	if text != "" {
		evt := core.Event{Type: core.EventText, Content: text}
		select {
		case gs.events <- evt:
		case <-gs.ctx.Done():
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
	done := make(chan struct{})
	go func() {
		gs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("geminiSession: close timed out, abandoning wg.Wait")
	}
	close(gs.events)
	return nil
}

// formatToolParams extracts a human-readable summary from tool parameters.
func formatToolParams(toolName string, params map[string]any) string {
	if params == nil {
		return ""
	}

	switch toolName {
	case "shell", "run_shell_command", "Bash":
		if cmd, ok := params["command"].(string); ok {
			return cmd
		}
	case "write_file", "WriteFile":
		fp, _ := params["file_path"].(string)
		if fp == "" {
			fp, _ = params["path"].(string)
		}
		if fp != "" {
			if content, ok := params["content"].(string); ok && content != "" {
				return "`" + fp + "`\n```\n" + content + "\n```"
			}
			return fp
		}
	case "replace", "ReplaceInFile":
		fp, _ := params["file_path"].(string)
		if fp == "" {
			fp, _ = params["path"].(string)
		}
		if fp != "" {
			old, _ := params["old_string"].(string)
			new_, _ := params["new_string"].(string)
			if old == "" {
				old, _ = params["old_str"].(string)
				new_, _ = params["new_str"].(string)
			}
			if old != "" || new_ != "" {
				diff := computeLineDiff(old, new_)
				return "`" + fp + "`\n```\n" + diff + "\n```"
			}
			return fp
		}
	case "read_file", "ReadFile":
		if p, ok := params["file_path"].(string); ok {
			return p
		}
		if p, ok := params["path"].(string); ok {
			return p
		}
	case "list_directory", "ListDirectory":
		if p, ok := params["path"].(string); ok {
			return p
		}
		if p, ok := params["directory"].(string); ok {
			return p
		}
	case "web_fetch", "WebFetch":
		if u, ok := params["url"].(string); ok {
			return u
		}
	case "google_web_search", "GoogleWebSearch":
		if q, ok := params["query"].(string); ok {
			return q
		}
	case "activate_skill":
		if n, ok := params["name"].(string); ok {
			return n
		}
	case "search_code", "SearchCode", "Glob", "Grep":
		if q, ok := params["query"].(string); ok {
			return q
		}
		if p, ok := params["pattern"].(string); ok {
			return p
		}
	}

	// Fallback: format as key: value pairs for readability
	var parts []string
	for k, v := range params {
		switch val := v.(type) {
		case string:
			parts = append(parts, k+": "+val)
		default:
			j, _ := json.Marshal(val)
			parts = append(parts, k+": "+string(j))
		}
	}
	return strings.Join(parts, ", ")
}

// computeLineDiff computes a minimal unified-style diff between old and new text.
// It finds common prefix/suffix lines and shows only the changed lines with
// up to 1 line of surrounding context. Unchanged context lines are prefixed
// with "  ", removed lines with "- ", and added lines with "+ ".
func computeLineDiff(old, new_ string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new_, "\n")

	// Find common prefix lines
	prefixLen := 0
	minLen := len(oldLines)
	if len(newLines) < minLen {
		minLen = len(newLines)
	}
	for prefixLen < minLen && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}

	// Find common suffix lines (not overlapping with prefix)
	suffixLen := 0
	for suffixLen < len(oldLines)-prefixLen && suffixLen < len(newLines)-prefixLen {
		oi := len(oldLines) - 1 - suffixLen
		ni := len(newLines) - 1 - suffixLen
		if oldLines[oi] != newLines[ni] {
			break
		}
		suffixLen++
	}

	// No actual changes
	if prefixLen+suffixLen >= len(oldLines) && prefixLen+suffixLen >= len(newLines) {
		return ""
	}

	// If everything differs (no common lines), show full old/new
	if prefixLen == 0 && suffixLen == 0 {
		var sb strings.Builder
		for _, l := range oldLines {
			sb.WriteString("- " + l + "\n")
		}
		for _, l := range newLines {
			sb.WriteString("+ " + l + "\n")
		}
		return strings.TrimRight(sb.String(), "\n")
	}

	var sb strings.Builder
	const contextN = 1

	// Context: tail of common prefix
	ctxStart := prefixLen - contextN
	if ctxStart < 0 {
		ctxStart = 0
	}
	if ctxStart > 0 {
		sb.WriteString("  ...\n")
	}
	for i := ctxStart; i < prefixLen; i++ {
		sb.WriteString("  " + oldLines[i] + "\n")
	}

	// Removed lines
	for i := prefixLen; i < len(oldLines)-suffixLen; i++ {
		sb.WriteString("- " + oldLines[i] + "\n")
	}

	// Added lines
	for i := prefixLen; i < len(newLines)-suffixLen; i++ {
		sb.WriteString("+ " + newLines[i] + "\n")
	}

	// Context: head of common suffix
	suffStart := len(oldLines) - suffixLen
	suffEnd := suffStart + contextN
	if suffEnd > len(oldLines) {
		suffEnd = len(oldLines)
	}
	for i := suffStart; i < suffEnd; i++ {
		sb.WriteString("  " + oldLines[i] + "\n")
	}
	if suffEnd < len(oldLines) {
		sb.WriteString("  ...")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
