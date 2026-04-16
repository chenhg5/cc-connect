package kimi

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

// kimSession manages multi-turn conversations with the Kimi CLI.
// Each Send() launches a new `kimi --print --output-format stream-json` process
// with --resume for conversation continuity.
type kimiSession struct {
	cmd       string
	workDir   string
	model     string
	mode      string
	timeout   time.Duration
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value // stores string — Kimi session ID
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool

	pendingMsgs []string // buffered assistant text messages
}

func newKimiSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string, timeout time.Duration) (*kimiSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	ks := &kimiSession{
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
	ks.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		ks.sessionID.Store(resumeID)
	}

	return ks, nil
}

func (ks *kimiSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !ks.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	// Save images and files into the workspace so Kimi CLI can access them.
	attachDir := filepath.Join(ks.workDir, ".cc-connect", "attachments")
	if (len(images) > 0 || len(files) > 0) && os.MkdirAll(attachDir, 0o755) != nil {
		attachDir = os.TempDir()
	}

	var imageRefs []string
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
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Warn("kimiSession: failed to save image", "error", err)
			continue
		}
		imageRefs = append(imageRefs, fpath)
	}

	var fileRefs []string
	for i, f := range files {
		fname := filepath.Base(f.FileName)
		if fname == "" || fname == "." || fname == ".." {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixMilli(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o644); err != nil {
			slog.Warn("kimiSession: failed to save file", "error", err)
			continue
		}
		fileRefs = append(fileRefs, fpath)
	}

	fullPrompt := prompt
	if len(imageRefs) > 0 {
		if fullPrompt == "" {
			fullPrompt = "Please analyze the attached image(s)."
		}
		fullPrompt += "\n\n[Attached images saved at: " + strings.Join(imageRefs, ", ") + "]"
	}
	if len(fileRefs) > 0 {
		if fullPrompt == "" {
			fullPrompt = "Please analyze the attached file(s)."
		}
		fullPrompt += "\n\n[Attached files saved at: " + strings.Join(fileRefs, ", ") + "]"
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
	}

	switch ks.mode {
	case "plan":
		args = append(args, "--plan")
	case "quiet":
		args = append(args, "--quiet")
	}

	sid := ks.CurrentSessionID()
	if sid != "" {
		args = append(args, "--resume", sid)
	}
	if ks.model != "" {
		args = append(args, "--model", ks.model)
	}
	if ks.workDir != "" {
		args = append(args, "--work-dir", ks.workDir)
	}

	args = append(args, "--prompt", fullPrompt)

	var cancel context.CancelFunc
	var ctx context.Context
	if ks.timeout > 0 {
		ctx, cancel = context.WithTimeout(ks.ctx, ks.timeout)
	} else {
		ctx, cancel = context.WithCancel(ks.ctx)
	}

	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	slog.Debug("kimiSession: launching", "resume", sid != "", "args", core.RedactArgs(args))
	cmd := exec.CommandContext(ctx, ks.cmd, args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = ks.workDir
	env := os.Environ()
	if len(ks.extraEnv) > 0 {
		env = core.MergeEnv(env, ks.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("kimiSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kimiSession: start: %w", err)
	}

	started = true
	ks.wg.Add(1)
	go func() {
		defer cancel()
		ks.readLoop(ctx, cmd, stdout, &stderrBuf, append(imageRefs, fileRefs...))
	}()

	return nil
}

func (ks *kimiSession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, tempFiles []string) {
	defer ks.wg.Done()
	defer func() {
		for _, f := range tempFiles {
			os.Remove(f)
		}
	}()

	go func() {
		<-ctx.Done()
		stdout.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var scanErr error
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		slog.Debug("kimiSession: raw", "line", truncate(line, 500))

		// Kimi prints a non-JSON line at the end: "To resume this session: kimi -r <id>"
		if strings.HasPrefix(line, "To resume this session:") {
			if id := extractResumeSessionID(line); id != "" {
				ks.sessionID.Store(id)
				slog.Debug("kimiSession: session id updated", "session_id", id)
			}
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("kimiSession: non-JSON line", "line", line)
			continue
		}

		ks.handleEvent(raw)
	}
	scanErr = scanner.Err()

	// Wait for process exit before sending any terminal event so the engine
	// never sees EventError after EventResult from the same turn.
	waitErr := cmd.Wait()

	if scanErr != nil {
		slog.Error("kimiSession: scanner error", "error", scanErr)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", scanErr)}
		select {
		case ks.events <- evt:
		case <-ks.ctx.Done():
			return
		}
	}

	if waitErr != nil {
		stderrMsg := strings.TrimSpace(stderrBuf.String())
		if stderrMsg != "" {
			slog.Error("kimiSession: process failed", "error", waitErr, "stderr", stderrMsg)
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			select {
			case ks.events <- evt:
			case <-ks.ctx.Done():
				return
			}
			return
		}
	}

	// Flush any remaining pending messages as text and send result event.
	ks.flushPendingAsText()
	evt := core.Event{Type: core.EventResult, SessionID: ks.CurrentSessionID(), Done: true}
	select {
	case ks.events <- evt:
	case <-ks.ctx.Done():
	}
}

func extractResumeSessionID(line string) string {
	// Format: "To resume this session: kimi -r <uuid>"
	parts := strings.Fields(line)
	for i, p := range parts {
		if p == "-r" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// Kimi CLI stream-json message roles:
//   - "assistant": content (think + text), tool_calls
//   - "tool":      content (tool execution result), tool_call_id
func (ks *kimiSession) handleEvent(raw map[string]any) {
	role, _ := raw["role"].(string)

	switch role {
	case "assistant":
		ks.handleAssistant(raw)
	case "tool":
		ks.handleTool(raw)
	default:
		slog.Debug("kimiSession: unhandled role", "role", role)
	}
}

func (ks *kimiSession) handleAssistant(raw map[string]any) {
	content, _ := raw["content"].([]any)
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "think", "thinking":
			if think, ok := block["think"].(string); ok && think != "" {
				evt := core.Event{Type: core.EventThinking, Content: think}
				select {
				case ks.events <- evt:
				case <-ks.ctx.Done():
					return
				}
			}
		case "text":
			if text, ok := block["text"].(string); ok && text != "" {
				ks.pendingMsgs = append(ks.pendingMsgs, text)
			}
		}
	}

	// Handle tool_calls
	toolCalls, _ := raw["tool_calls"].([]any)
	if len(toolCalls) > 0 {
		ks.flushPendingAsThinking()
		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			funcBlock, _ := tcMap["function"].(map[string]any)
			toolName, _ := funcBlock["name"].(string)
			args, _ := funcBlock["arguments"].(string)
			toolID, _ := tcMap["id"].(string)

			slog.Debug("kimiSession: tool_call", "tool", toolName, "id", toolID)
			evt := core.Event{
				Type:      core.EventToolUse,
				ToolName:  toolName,
				ToolInput: truncate(strings.TrimSpace(args), 500),
				RequestID: toolID,
			}
			select {
			case ks.events <- evt:
			case <-ks.ctx.Done():
				return
			}
		}
	}
}

func (ks *kimiSession) handleTool(raw map[string]any) {
	toolCallID, _ := raw["tool_call_id"].(string)
	content, _ := raw["content"].([]any)
	var outputParts []string
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		if blockType == "text" {
			if text, ok := block["text"].(string); ok {
				outputParts = append(outputParts, text)
			}
		}
	}
	output := strings.Join(outputParts, "")

	if output != "" {
		slog.Debug("kimiSession: tool result", "tool_call_id", toolCallID)
		evt := core.Event{
			Type:       core.EventToolResult,
			ToolName:   toolCallID,
			ToolResult: truncate(strings.TrimSpace(output), 500),
		}
		select {
		case ks.events <- evt:
		case <-ks.ctx.Done():
			return
		}
	}
}

func (ks *kimiSession) flushPendingAsThinking() {
	if len(ks.pendingMsgs) == 0 {
		return
	}
	text := strings.Join(ks.pendingMsgs, "")
	ks.pendingMsgs = ks.pendingMsgs[:0]
	if text != "" {
		evt := core.Event{Type: core.EventThinking, Content: text}
		select {
		case ks.events <- evt:
		case <-ks.ctx.Done():
		}
	}
}

func (ks *kimiSession) flushPendingAsText() {
	if len(ks.pendingMsgs) == 0 {
		return
	}
	text := strings.Join(ks.pendingMsgs, "")
	ks.pendingMsgs = ks.pendingMsgs[:0]
	if text != "" {
		evt := core.Event{Type: core.EventText, Content: text}
		select {
		case ks.events <- evt:
		case <-ks.ctx.Done():
		}
	}
}

// RespondPermission is a no-op — Kimi CLI permissions are handled via --print (implicit --yolo).
func (ks *kimiSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (ks *kimiSession) Events() <-chan core.Event {
	return ks.events
}

func (ks *kimiSession) CurrentSessionID() string {
	v, _ := ks.sessionID.Load().(string)
	return v
}

func (ks *kimiSession) Alive() bool {
	return ks.alive.Load()
}

func (ks *kimiSession) Close() error {
	ks.alive.Store(false)
	ks.cancel()
	done := make(chan struct{})
	go func() {
		ks.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("kimiSession: close timed out, abandoning wg.Wait")
	}
	close(ks.events)
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
