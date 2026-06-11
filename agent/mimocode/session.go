package mimocode

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

// mimocodeSession manages multi-turn conversations with the MiMoCode CLI.
// Each Send() launches a new `mimo run --format json` process
// with --session for conversation continuity.
type mimocodeSession struct {
	cmd               string
	workDir           string
	model             string
	mode              string
	agentName         string
	extraEnv          []string
	events            chan core.Event
	chatID            atomic.Value // stores string — MiMoCode session ID
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	alive             atomic.Bool
	resultSent        atomic.Bool // true when EventResult has been sent for this turn
}

func newMimocodeSession(ctx context.Context, cmd, workDir, model, mode, agentName, resumeID string, extraEnv []string) (*mimocodeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &mimocodeSession{
		cmd:       cmd,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		agentName: agentName,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	s.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		s.chatID.Store(resumeID)
	}

	return s, nil
}

func (s *mimocodeSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	s.resultSent.Store(false)

	chatID := s.CurrentSessionID()
	isResume := chatID != ""

	args := s.buildRunArgs(prompt, imagePaths, chatID)

	slog.Debug("mimocodeSession: launching", "resume", isResume, "args", core.RedactArgs(args))

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mimocodeSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	cmd.Stdin = strings.NewReader(prompt)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mimocodeSession: start: %w", err)
	}

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (s *mimocodeSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mimocodeSession: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := mimocodeImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("mimocodeSession: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if prompt == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func mimocodeImageExt(mimeType string) string {
	switch mimeType {
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

func (s *mimocodeSession) buildRunArgs(prompt string, imagePaths []string, chatID string) []string {
	args := []string{"run", "--format", "json"}

	if chatID != "" {
		args = append(args, "--session", chatID)
	}
	if s.agentName != "" {
		args = append(args, "--agent", s.agentName)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.workDir != "" {
		args = append(args, "--dir", s.workDir)
	}

	// In yolo/auto mode, skip permission prompts entirely
	if s.mode == "yolo" {
		args = append(args, "--dangerously-skip-permissions")
	}

	for _, imagePath := range imagePaths {
		if imagePath == "" {
			continue
		}
		args = append(args, "--file", imagePath)
	}

	return args
}

func (s *mimocodeSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	defer func() { _ = cmd.Wait() }()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("mimocodeSession: non-JSON line", "line", line)
			continue
		}

		s.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("mimocodeSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
		return
	}

	stderrMsg := stderrBuf.String()
	if stderrMsg != "" {
		slog.Error("mimocodeSession: process error", "stderr", truncate(stderrMsg, 500))
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return
	}

	slog.Debug("mimocodeSession: readLoop complete, sending fallback EventResult", "session_id", s.CurrentSessionID())
	s.sendEventResult()
}

// MiMoCode NDJSON event structure (similar to OpenCode/Claude Code):
//
//	{ "type": "text|tool_use|reasoning|step_start|step_finish",
//	  "part": { "type": "text|tool|reasoning|step-start|step-finish", ... } }
func (s *mimocodeSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "text":
		s.handleText(raw)
	case "tool_use":
		s.handleToolUse(raw)
	case "reasoning":
		s.handleReasoning(raw)
	case "step_start":
		s.handleStepStart(raw)
	case "step_finish":
		s.handleStepFinish(raw)
	case "error":
		s.handleError(raw)
	default:
		b, _ := json.Marshal(raw)
		slog.Debug("mimocodeSession: unhandled event", "type", eventType, "raw", string(b))
	}
}

func (s *mimocodeSession) handleText(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	text, _ := part["text"].(string)
	metadata, _ := part["metadata"].(map[string]any)

	if text != "" {
		evt := core.Event{Type: core.EventText, Content: text, Metadata: metadata}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *mimocodeSession) handleToolUse(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}

	toolName, _ := part["tool"].(string)

	state, _ := part["state"].(map[string]any)
	status := ""
	if state != nil {
		status, _ = state["status"].(string)
	}

	input := extractToolInput(state)

	if status == "completed" {
		useEvt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- useEvt:
		case <-s.ctx.Done():
			return
		}

		output, _ := state["output"].(string)
		resultEvt := core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncate(output, 500)}
		select {
		case s.events <- resultEvt:
		case <-s.ctx.Done():
			return
		}
	} else {
		evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}

		if status == "error" && state != nil {
			errMsg, _ := state["error"].(string)
			if errMsg != "" {
				slog.Info("mimocodeSession: tool rejected, surfacing error as text", "tool", toolName, "error", errMsg)
				errEvt := core.Event{Type: core.EventText, Content: errMsg}
				select {
				case s.events <- errEvt:
				case <-s.ctx.Done():
					return
				}
			}
		}
	}
}

func extractToolInput(state map[string]any) string {
	if state == nil {
		return ""
	}
	if title, ok := state["title"].(string); ok && title != "" {
		return title
	}
	switch input := state["input"].(type) {
	case string:
		return input
	case map[string]any:
		if desc, ok := input["description"].(string); ok && desc != "" {
			return desc
		}
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			return cmd
		}
		b, _ := json.Marshal(input)
		return truncate(string(b), 200)
	}
	return ""
}

func (s *mimocodeSession) handleReasoning(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	text, _ := part["text"].(string)
	if text != "" {
		evt := core.Event{Type: core.EventThinking, Content: text}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *mimocodeSession) handleError(raw map[string]any) {
	errMsg := extractErrorMessage(raw)
	slog.Error("mimocodeSession: agent error", "error", errMsg)
	evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		return
	}
}

func extractErrorMessage(raw map[string]any) string {
	if errObj, ok := raw["error"].(map[string]any); ok {
		if data, ok := errObj["data"].(map[string]any); ok {
			if msg, ok := data["message"].(string); ok && msg != "" {
				name, _ := errObj["name"].(string)
				if name != "" {
					return name + ": " + msg
				}
				return msg
			}
		}
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
		if name, ok := errObj["name"].(string); ok && name != "" {
			return name
		}
	}
	if errStr, ok := raw["error"].(string); ok && errStr != "" {
		return errStr
	}
	if part, ok := raw["part"].(map[string]any); ok {
		if msg, ok := part["error"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := part["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := raw["message"].(string); ok && msg != "" {
		return msg
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

func (s *mimocodeSession) handleStepStart(raw map[string]any) {
	sessionID, _ := raw["sessionID"].(string)
	if sessionID == "" {
		part, _ := raw["part"].(map[string]any)
		if part != nil {
			sessionID, _ = part["sessionID"].(string)
		}
	}
	if sessionID != "" {
		s.chatID.Store(sessionID)
		slog.Debug("mimocodeSession: session started", "session_id", sessionID)
	}
}

func (s *mimocodeSession) handleStepFinish(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	reason := ""
	if part != nil {
		reason, _ = part["reason"].(string)
	}
	slog.Debug("mimocodeSession: step finished", "reason", reason, "session_id", s.CurrentSessionID())

	if reason == "stop" {
		s.sendEventResult()
	}
}

func (s *mimocodeSession) sendEventResult() {
	if s.resultSent.Load() {
		slog.Debug("mimocodeSession: EventResult already sent, skipping", "session_id", s.CurrentSessionID())
		return
	}
	s.resultSent.Store(true)
	sid := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// RespondPermission is a no-op — MiMoCode handles permissions internally.
func (s *mimocodeSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *mimocodeSession) Events() <-chan core.Event {
	return s.events
}

func (s *mimocodeSession) CurrentSessionID() string {
	v, _ := s.chatID.Load().(string)
	return v
}

func (s *mimocodeSession) Alive() bool {
	return s.alive.Load()
}

func (s *mimocodeSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(s.events)
	case <-time.After(8 * time.Second):
		slog.Warn("mimocodeSession: close timed out, abandoning wg.Wait")
	}
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
