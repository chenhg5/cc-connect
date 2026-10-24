package droid

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

type droidSession struct {
	cmd                   string
	cliExtraArgs          []string
	workDir               string
	model                 string
	reasoningEffort       string
	auto                  string
	skipPermissionsUnsafe bool
	extraEnv              []string

	events chan core.Event

	sessionID atomic.Value

	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	wg        sync.WaitGroup
	inFlight  bool
	closeOnce sync.Once
	alive     atomic.Bool
}

func newDroidSession(
	ctx context.Context,
	cmd string,
	cliExtraArgs []string,
	workDir string,
	model string,
	reasoningEffort string,
	auto string,
	skipPermissionsUnsafe bool,
	resumeID string,
	extraEnv []string,
) (*droidSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &droidSession{
		cmd:                   cmd,
		cliExtraArgs:          append([]string(nil), cliExtraArgs...),
		workDir:               workDir,
		model:                 model,
		reasoningEffort:       reasoningEffort,
		auto:                  auto,
		skipPermissionsUnsafe: skipPermissionsUnsafe,
		extraEnv:              extraEnv,
		events:                make(chan core.Event, 64),
		ctx:                   sessionCtx,
		cancel:                cancel,
	}
	s.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		s.sessionID.Store(resumeID)
	}

	return s, nil
}

func (s *droidSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if err := s.beginTurn(); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			s.endTurn()
			s.wg.Done()
		}
	}()

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	if len(images) > 0 {
		imagePaths, err := s.saveImages(images)
		if err != nil {
			return err
		}
		if strings.TrimSpace(prompt) == "" {
			prompt = "Please analyze the attached image(s)."
		}
		prompt += "\n\n(Images saved locally, please read them: " + strings.Join(imagePaths, ", ") + ")"
	}

	args := s.buildExecArgs(prompt)

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	cmd.WaitDelay = 2 * time.Second
	if len(s.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), s.extraEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("droidSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("droidSession: start: %w", err)
	}

	go s.readLoop(cmd, stdout, &stderrBuf)
	started = true

	return nil
}

func (s *droidSession) beginTurn() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.inFlight {
		return fmt.Errorf("droid session turn already in progress")
	}
	s.wg.Add(1)
	s.inFlight = true
	return nil
}

func (s *droidSession) endTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight = false
}

func (s *droidSession) buildExecArgs(prompt string) []string {
	args := append([]string(nil), s.cliExtraArgs...)
	args = append(args, "exec", "--output-format", "stream-json")

	if sid := s.CurrentSessionID(); sid != "" {
		args = append(args, "--session-id", sid)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.reasoningEffort != "" {
		args = append(args, "--reasoning-effort", s.reasoningEffort)
	}
	if s.auto != "" && !s.skipPermissionsUnsafe {
		args = append(args, "--auto", s.auto)
	}
	if s.skipPermissionsUnsafe {
		args = append(args, "--skip-permissions-unsafe")
	}
	if s.workDir != "" {
		args = append(args, "--cwd", s.workDir)
	}

	args = append(args, "--", prompt)
	return args
}

func (s *droidSession) saveImages(images []core.ImageAttachment) ([]string, error) {
	workDir, err := filepath.Abs(s.workDir)
	if err != nil {
		return nil, fmt.Errorf("droidSession: resolve image directory: %w", err)
	}
	imgDir := filepath.Join(workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return nil, fmt.Errorf("droidSession: create image dir: %w", err)
	}

	paths := make([]string, 0, len(images))
	for _, img := range images {
		ext := ".png"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		file, err := os.CreateTemp(imgDir, "img_*"+ext)
		if err != nil {
			return nil, fmt.Errorf("droidSession: create image: %w", err)
		}
		path := file.Name()
		_, writeErr := file.Write(img.Data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return nil, fmt.Errorf("droidSession: save image: %w", writeErr)
			}
			return nil, fmt.Errorf("droidSession: close image: %w", closeErr)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (s *droidSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	stopClosing := context.AfterFunc(s.ctx, func() { _ = stdout.Close() })
	defer stopClosing()

	var terminal *core.Event
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			slog.Debug("droidSession: non-JSON line", "line", truncate(string(line), 300))
			continue
		}
		if event := s.handleEvent(raw); event != nil && (terminal == nil || terminal.Type != core.EventError) {
			terminal = event
		}
	}
	if err := scanner.Err(); err != nil {
		terminal = &core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		_ = cmd.Process.Kill()
	}
	if err := cmd.Wait(); err != nil && (terminal == nil || terminal.Type != core.EventError) {
		message := strings.TrimSpace(stderrBuf.String())
		if message == "" {
			message = err.Error()
		}
		terminal = &core.Event{Type: core.EventError, Error: fmt.Errorf("droid exec: %s", message)}
	}
	if terminal == nil {
		terminal = &core.Event{Type: core.EventResult, Done: true}
	}
	terminal.SessionID = s.CurrentSessionID()

	// Release the persisted-session writer only after exit, before publishing completion.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight = false
	s.emit(*terminal)
}

func (s *droidSession) emit(event core.Event) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

func (s *droidSession) handleEvent(raw map[string]any) *core.Event {
	if sid := getString(raw, "session_id"); sid != "" {
		s.sessionID.Store(sid)
	}
	event := core.Event{SessionID: s.CurrentSessionID()}
	switch getString(raw, "type") {
	case "system":
		if getString(raw, "subtype") != "init" {
			return nil
		}
		event.Type = core.EventText
	case "message":
		if getString(raw, "role") != "assistant" {
			return nil
		}
		event.Type = core.EventText
		event.Content = getString(raw, "text")
	case "reasoning":
		event.Type = core.EventThinking
		event.Content = getString(raw, "text")
	case "tool_call", "tool_use":
		event.Type = core.EventToolUse
		event.ToolName = getString(raw, "toolName", "toolId", "tool_name", "name")
		event.ToolInput = eventValue(raw, "parameters", "tool_input", "input")
	case "tool_result":
		event.Type = core.EventToolResult
		event.ToolName = getString(raw, "toolName", "toolId", "tool_name", "name")
		event.ToolResult = eventValue(raw, "value", "result", "output")
		failed, _ := raw["isError"].(bool)
		if failed {
			if detail, ok := raw["error"].(map[string]any); ok {
				event.ToolResult = getString(detail, "message")
			}
			event.ToolStatus = "failed"
		} else {
			event.ToolStatus = "completed"
		}
		success := !failed
		event.ToolSuccess = &success
	case "completion", "result":
		event.Type = core.EventResult
		event.Content = getString(raw, "finalText", "result", "content", "text")
		event.Done = true
		if usage, ok := raw["usage"].(map[string]any); ok {
			event.InputTokens = tokenCount(usage, "input_tokens")
			event.OutputTokens = tokenCount(usage, "output_tokens")
			event.CacheReadInputTokens = tokenCount(usage, "cache_read_input_tokens")
			event.CacheCreationInputTokens = tokenCount(usage, "cache_creation_input_tokens")
		}
		failed, _ := raw["is_error"].(bool)
		if failed || getString(raw, "subtype") == "failure" {
			event.Type = core.EventError
			event.Error = fmt.Errorf("droid exec: %s", event.Content)
		}
		return &event
	case "error":
		event.Type = core.EventError
		message := getString(raw, "message")
		if message == "" {
			message = eventValue(raw, "error")
		}
		if message == "" {
			message = "droid exec failed"
		}
		event.Error = fmt.Errorf("%s", message)
		return &event
	default:
		return nil
	}
	s.emit(event)
	return nil
}

func eventValue(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok && value != nil {
			if text, ok := value.(string); ok {
				return text
			}
			if data, err := json.Marshal(value); err == nil {
				return string(data)
			}
		}
	}
	return ""
}

func tokenCount(usage map[string]any, key string) int {
	value, _ := usage[key].(float64)
	return int(value)
}

func getString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := raw[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func (s *droidSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *droidSession) Events() <-chan core.Event {
	return s.events
}

func (s *droidSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *droidSession) Alive() bool {
	return s.alive.Load() && s.ctx.Err() == nil
}

func (s *droidSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		// Pair with beginTurn so no WaitGroup reservation can follow Wait.
		s.mu.Lock()
		s.alive.Store(false)
		s.mu.Unlock()
		s.wg.Wait()
		close(s.events)
	})
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
