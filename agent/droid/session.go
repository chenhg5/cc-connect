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
	workDir               string
	model                 string
	reasoningEffort       string
	auto                  string
	skipPermissionsUnsafe bool
	extraEnv              []string

	events chan core.Event

	sessionID atomic.Value

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	alive  atomic.Bool
}

func newDroidSession(
	ctx context.Context,
	cmd string,
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

func (s *droidSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
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

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (s *droidSession) buildExecArgs(prompt string) []string {
	args := []string{"exec", "--output-format", "stream-json"}

	if sid := s.CurrentSessionID(); sid != "" {
		args = append(args, "--session-id", sid)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.reasoningEffort != "" {
		args = append(args, "--reasoning-effort", s.reasoningEffort)
	}
	if s.auto != "" {
		args = append(args, "--auto", s.auto)
	}
	if s.skipPermissionsUnsafe {
		args = append(args, "--skip-permissions-unsafe")
	}
	if s.workDir != "" {
		args = append(args, "--cwd", s.workDir)
	}

	args = append(args, prompt)
	return args
}

func (s *droidSession) saveImages(images []core.ImageAttachment) ([]string, error) {
	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return nil, fmt.Errorf("droidSession: create image dir: %w", err)
	}

	paths := make([]string, 0, len(images))
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
		name := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		path := filepath.Join(imgDir, name)
		if err := os.WriteFile(path, img.Data, 0o644); err != nil {
			return nil, fmt.Errorf("droidSession: save image: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (s *droidSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()

	hasResult := false
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg == "" {
				stderrMsg = err.Error()
			}
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
				return
			}
		}

		if !hasResult {
			evt := core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("droidSession: non-JSON line", "line", truncate(line, 300))
			continue
		}

		if s.handleEvent(raw) {
			hasResult = true
		}
	}

	if err := scanner.Err(); err != nil {
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
	}
}

func (s *droidSession) handleEvent(raw map[string]any) bool {
	t, _ := raw["type"].(string)

	switch t {
	case "system":
		subtype, _ := raw["subtype"].(string)
		if subtype == "init" {
			if sid, ok := raw["session_id"].(string); ok && sid != "" {
				s.sessionID.Store(sid)
				evt := core.Event{Type: core.EventText, SessionID: sid}
				select {
				case s.events <- evt:
				case <-s.ctx.Done():
				}
			}
		}

	case "message":
		role, _ := raw["role"].(string)
		if role == "assistant" {
			if text, ok := raw["text"].(string); ok && text != "" {
				evt := core.Event{Type: core.EventText, Content: text}
				select {
				case s.events <- evt:
				case <-s.ctx.Done():
				}
			}
		}

	case "tool_use":
		toolName := getString(raw, "tool_name", "name")
		input := getString(raw, "tool_input", "input")
		if input == "" {
			if m, ok := raw["input"].(map[string]any); ok {
				if b, err := json.Marshal(m); err == nil {
					input = string(b)
				}
			}
		}
		evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}

	case "tool_result":
		toolName := getString(raw, "tool_name", "name")
		result := getString(raw, "result", "output")
		evt := core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncate(result, 500)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}

	case "result":
		content := getString(raw, "content", "text")
		evt := core.Event{Type: core.EventResult, Content: content, SessionID: s.CurrentSessionID(), Done: true}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return true

	case "error":
		message := getString(raw, "message")
		if message == "" {
			if b, err := json.Marshal(raw); err == nil {
				message = string(b)
			}
		}
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", message)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
	}

	return false
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
	return s.alive.Load()
}

func (s *droidSession) Close() error {
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
		slog.Warn("droidSession: close timed out, abandoning wg.Wait")
	}
	close(s.events)
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
