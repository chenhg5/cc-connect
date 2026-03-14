package pi

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

// piSession manages a multi-turn pi coding agent conversation.
// Each Send() spawns `pi --mode json -p <prompt>`.
// Subsequent turns use `--session <sessionID>` to resume.
type piSession struct {
	cmd       string
	workDir   string
	model     string
	mode      string
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value // stores string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool

	thinkingBuf strings.Builder // accumulates thinking_delta chunks
}

func newPiSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*piSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &piSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	s.alive.Store(true)

	if resumeID != "" {
		s.sessionID.Store(resumeID)
	}

	return s, nil
}

func (s *piSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	// Clean up attachments from previous turns.
	cleanAttachments(s.workDir)

	// Save all attachments to disk — pi reads them via @file syntax.
	var atFiles []string
	if len(images) > 0 {
		atFiles = append(atFiles, saveImagesToDisk(s.workDir, images)...)
	}
	if len(files) > 0 {
		atFiles = append(atFiles, core.SaveFilesToDisk(s.workDir, files)...)
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	args := []string{"--mode", "json", "-p"}

	sid := s.CurrentSessionID()
	if sid != "" {
		args = append(args, "--session", sid)
	}

	if s.model != "" {
		args = append(args, "--model", s.model)
	}

	if s.mode == "yolo" {
		args = append(args, "--auto-approve")
	}

	// Pass attachments as @file arguments
	for _, f := range atFiles {
		args = append(args, "@"+f)
	}

	// Append prompt as positional arg
	args = append(args, prompt)

	slog.Debug("piSession: launching", "resume", sid != "", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("piSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("piSession: start: %w", err)
	}

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (s *piSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("piSession: process failed", "error", err, "stderr", truncStr(stderrMsg, 200))
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case s.events <- evt:
				case <-s.ctx.Done():
					return
				}
			}
		}
	}()

	// Pi's JSON events are small (typically <1KB each). A 10MB Scanner buffer
	// is more than sufficient — no need for the bufio.Reader approach used by
	// adapters that may receive very large single-line responses.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("piSession: non-JSON line", "line", truncStr(line, 100))
			continue
		}

		s.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("piSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}

	// Emit EventResult when the process finishes.
	sid := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// Pi NDJSON event types:
//
//	session           — session metadata with id
//	agent_start/end   — agent lifecycle
//	turn_start/end    — turn boundaries
//	message_start     — beginning of user/assistant/toolResult message
//	message_update    — streaming deltas (assistantMessageEvent sub-events)
//	message_end       — complete message
func (s *piSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "session":
		if id, ok := raw["id"].(string); ok && id != "" {
			s.sessionID.Store(id)
			slog.Debug("piSession: session started", "session_id", id)
		}

	case "message_update":
		s.handleMessageUpdate(raw)

	case "message_end":
		s.handleMessageEnd(raw)

	case "agent_start", "agent_end", "turn_start", "turn_end", "message_start":
		// Logged for debugging but no action needed.
		slog.Debug("piSession: lifecycle event", "type", eventType)

	default:
		slog.Debug("piSession: unhandled event", "type", eventType)
	}
}

// handleMessageUpdate processes streaming deltas from pi's assistantMessageEvent.
func (s *piSession) handleMessageUpdate(raw map[string]any) {
	ame, _ := raw["assistantMessageEvent"].(map[string]any)
	if ame == nil {
		return
	}

	subType, _ := ame["type"].(string)

	switch subType {
	case "text_delta":
		delta, _ := ame["delta"].(string)
		if delta != "" {
			evt := core.Event{Type: core.EventText, Content: delta}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
				return
			}
		}

	case "thinking_delta":
		delta, _ := ame["delta"].(string)
		if delta != "" {
			s.thinkingBuf.WriteString(delta)
		}

	case "thinking_end":
		if s.thinkingBuf.Len() > 0 {
			evt := core.Event{Type: core.EventThinking, Content: s.thinkingBuf.String()}
			s.thinkingBuf.Reset()
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
				return
			}
		}

	case "toolcall_end":
		// Extract tool name and input from the accumulated message content.
		s.emitToolFromMessage(ame)
	}
}

// emitToolFromMessage extracts tool call info from a toolcall_end event.
func (s *piSession) emitToolFromMessage(ame map[string]any) {
	msg, _ := ame["message"].(map[string]any)
	if msg == nil {
		msg, _ = ame["partial"].(map[string]any)
	}
	if msg == nil {
		return
	}

	content, _ := msg["content"].([]any)
	idx := int(0)
	if ci, ok := ame["contentIndex"].(float64); ok {
		idx = int(ci)
	}

	if idx >= 0 && idx < len(content) {
		item, _ := content[idx].(map[string]any)
		if item != nil {
			itemType, _ := item["type"].(string)
			if itemType == "toolCall" {
				name, _ := item["name"].(string)
				input := extractToolInput(item)
				evt := core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input}
				select {
				case s.events <- evt:
				case <-s.ctx.Done():
					return
				}
			}
		}
	}
}

// handleMessageEnd processes completed messages — particularly toolResult messages.
func (s *piSession) handleMessageEnd(raw map[string]any) {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return
	}

	role, _ := msg["role"].(string)

	switch role {
	case "toolResult":
		toolName, _ := msg["toolName"].(string)
		content, _ := msg["content"].([]any)
		var output string
		for _, c := range content {
			if item, ok := c.(map[string]any); ok {
				if text, ok := item["text"].(string); ok {
					output = text
					break
				}
			}
		}
		evt := core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncStr(output, 500)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}

	case "assistant":
		// Check for errors
		if errMsg, _ := msg["errorMessage"].(string); errMsg != "" {
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

// extractToolInput pulls a concise summary from a tool call content item.
func extractToolInput(item map[string]any) string {
	args, _ := item["arguments"].(map[string]any)
	if args == nil {
		return ""
	}
	// Prefer description or command fields.
	if desc, ok := args["description"].(string); ok && desc != "" {
		return desc
	}
	if cmd, ok := args["command"].(string); ok && cmd != "" {
		return cmd
	}
	if fp, ok := args["file_path"].(string); ok && fp != "" {
		return fp
	}
	if pattern, ok := args["pattern"].(string); ok && pattern != "" {
		return pattern
	}
	if query, ok := args["query"].(string); ok && query != "" {
		return query
	}
	b, _ := json.Marshal(args)
	return truncStr(string(b), 200)
}

func (s *piSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *piSession) Events() <-chan core.Event {
	return s.events
}

func (s *piSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *piSession) Alive() bool {
	return s.alive.Load()
}

func (s *piSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("piSession: close timed out, abandoning wg.Wait")
	}
	close(s.events)
	return nil
}

// cleanAttachments removes files from the attachments directory to avoid
// accumulating files across turns.
func cleanAttachments(workDir string) {
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")
	entries, err := os.ReadDir(attachDir)
	if err != nil {
		return // directory may not exist yet
	}
	for _, e := range entries {
		if !e.IsDir() {
			os.Remove(filepath.Join(attachDir, e.Name()))
		}
	}
}

// saveImagesToDisk saves image attachments to workDir/.cc-connect/attachments/
// and returns the list of absolute file paths.
func saveImagesToDisk(workDir string, images []core.ImageAttachment) []string {
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Error("piSession: failed to create attachments dir", "error", err)
		return nil
	}

	var paths []string
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
		fname := img.FileName
		if fname == "" {
			fname = fmt.Sprintf("image_%d_%d%s", time.Now().UnixMilli(), i, ext)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("piSession: save image failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	return paths
}

func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
