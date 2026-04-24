package claudecodesdk

import (
	"bufio"
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
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type sdkSession struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdinMu         sync.Mutex
	events          chan core.Event
	sessionID       atomic.Value // stores string
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	alive           atomic.Bool
	gracefulTimeout time.Duration
}

func newSDKSession(
	ctx context.Context,
	nodePath, sidecarPath string,
	workDir, model, mode, resume, claudePath string,
	extraEnv []string,
) (*sdkSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	// Resolve sidecar to absolute path (relative to cwd, not workDir)
	if !filepath.IsAbs(sidecarPath) {
		if abs, err := filepath.Abs(sidecarPath); err == nil {
			sidecarPath = abs
		}
	}

	args := []string{sidecarPath, workDir}
	args = append(args, model, mode, resume)
	if claudePath != "" {
		args = append(args, claudePath)
	}

	cmd := exec.CommandContext(sessionCtx, nodePath, args...)
	cmd.Dir = workDir

	env := os.Environ()
	if len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecodesdk: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudecodesdk: stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("claudecodesdk: start sidecar: %w", err)
	}

	s := &sdkSession{
		cmd:             cmd,
		stdin:           stdin,
		events:          make(chan core.Event, 64),
		ctx:             sessionCtx,
		cancel:          cancel,
		done:            make(chan struct{}),
		gracefulTimeout: 120 * time.Second,
	}
	s.sessionID.Store(resume)
	s.alive.Store(true)

	go s.readLoop(stdout)

	return s, nil
}

func (s *sdkSession) readLoop(stdout io.ReadCloser) {
	defer func() {
		s.alive.Store(false)
		close(s.events)
		close(s.done)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var evt sidecarEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			slog.Debug("claudecodesdk: non-JSON line", "line", line)
			continue
		}

		coreEvt := s.mapEvent(&evt)
		if coreEvt == nil {
			continue
		}

		select {
		case s.events <- *coreEvt:
		case <-s.ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("claudecodesdk: scanner error", "error", err)
	}
}

func (s *sdkSession) mapEvent(evt *sidecarEvent) *core.Event {
	switch evt.Event {
	case "system":
		if evt.Subtype == "init" {
			s.sessionID.Store(evt.SessionID)
		}
		return nil

	case "text":
		return &core.Event{Type: core.EventText, Content: evt.Content}

	case "thinking":
		return &core.Event{Type: core.EventThinking, Content: evt.Content}

	case "tool_use":
		inputJSON, _ := json.Marshal(evt.Input)
		inputStr := string(inputJSON)
		if len(inputStr) > 200 {
			inputStr = inputStr[:200] + "..."
		}
		return &core.Event{
			Type:      core.EventToolUse,
			ToolName:  evt.ToolName,
			ToolInput: inputStr,
		}

	case "permission_request":
		input, _ := evt.Input.(map[string]any)
		coreEvt := &core.Event{
			Type:         core.EventPermissionRequest,
			RequestID:    evt.RequestID,
			ToolName:     evt.ToolName,
			ToolInputRaw: input,
		}
		if input != nil {
			coreEvt.ToolInput = summarizeInput(evt.ToolName, input)
		}
		if evt.ToolName == "AskUserQuestion" {
			coreEvt.Questions = parseUserQuestions(input)
		}
		return coreEvt

	case "result":
		sid, _ := s.sessionID.Load().(string)
		var inTok, outTok int
		if evt.Usage != nil {
			inTok = evt.Usage.InputTokens
			outTok = evt.Usage.OutputTokens
		}
		return &core.Event{
			Type:         core.EventResult,
			Content:      evt.Content,
			SessionID:    sid,
			Done:         true,
			InputTokens:  inTok,
			OutputTokens: outTok,
		}

	case "error":
		return &core.Event{
			Type:  core.EventError,
			Error: fmt.Errorf("%s", evt.Message),
		}

	case "aborted":
		return &core.Event{
			Type:  core.EventError,
			Error: fmt.Errorf("query aborted"),
		}

	case "ready":
		return nil

	default:
		slog.Debug("claudecodesdk: unknown event", "event", evt.Event)
		return nil
	}
}

func summarizeInput(tool string, input map[string]any) string {
	switch tool {
	case "Read", "Edit", "Write":
		if fp, ok := input["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return cmd
		}
	}
	b, _ := json.Marshal(input)
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// Send writes a user message command to the sidecar stdin.
func (s *sdkSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("claudecodesdk: session is not running")
	}
	prompt = core.AppendFileRefs(prompt, core.SaveFilesToDisk("", files))
	return s.writeJSON(sidecarCommand{Type: "send", Content: prompt})
}

// IsRetryableError implements core.RetryableErrorChecker.
// It detects transient Claude API errors that are worth retrying.
func (s *sdkSession) IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Match API Error 400 with specific error codes from the Claude API proxy.
	// Example: API Error: 400 {"type":"error","error":{"message":"网络错误...","code":"1234"},...}
	if strings.Contains(msg, "API Error: 400") {
		return true
	}
	// Match specific error codes that indicate transient failures.
	if strings.Contains(msg, `"code":"1234"`) {
		return true
	}
	// Match network error messages from various proxies.
	if strings.Contains(msg, "网络错误") {
		return true
	}
	return false
}

// RespondPermission writes a permission response to the sidecar stdin.
func (s *sdkSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !s.alive.Load() {
		return fmt.Errorf("claudecodesdk: session is not running")
	}
	return s.writeJSON(sidecarPermission{
		Type:      "permission",
		RequestID: requestID,
		Result: sidecarPermResult{
			Behavior:     result.Behavior,
			UpdatedInput: result.UpdatedInput,
			Message:      result.Message,
		},
	})
}

// SetLiveMode sends a hot mode switch to the sidecar without restarting.
// This implements core.LiveModeSwitcher.
func (s *sdkSession) SetLiveMode(mode string) bool {
	if !s.alive.Load() {
		return false
	}
	err := s.writeJSON(sidecarSetMode{Type: "set_mode", Mode: mode})
	if err != nil {
		slog.Error("claudecodesdk: set_live_mode failed", "error", err)
		return false
	}
	return true
}

// Abort sends an abort command to cancel the running query.
func (s *sdkSession) Abort() error {
	if !s.alive.Load() {
		return fmt.Errorf("claudecodesdk: session is not running")
	}
	return s.writeJSON(sidecarAbort{Type: "abort"})
}

func (s *sdkSession) writeJSON(v any) error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("claudecodesdk: marshal: %w", err)
	}
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("claudecodesdk: write stdin: %w", err)
	}
	return nil
}

func (s *sdkSession) Events() <-chan core.Event { return s.events }
func (s *sdkSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}
func (s *sdkSession) Alive() bool { return s.alive.Load() }

// Close terminates the sidecar process gracefully.
func (s *sdkSession) Close() error {
	s.writeJSON(sidecarCommand{Type: "close"})

	graceful := s.gracefulTimeout
	if graceful <= 0 {
		graceful = 8 * time.Second
	}

	select {
	case <-s.done:
		slog.Info("claudecodesdk: sidecar exited cleanly")
		return nil
	case <-time.After(3 * time.Second):
		s.stdinMu.Lock()
		_ = s.stdin.Close()
		s.stdinMu.Unlock()
	}

	select {
	case <-s.done:
		slog.Info("claudecodesdk: exited after stdin close")
		return nil
	case <-time.After(graceful):
		slog.Warn("claudecodesdk: graceful stop timed out, sending SIGTERM")
	}

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-s.done:
		slog.Info("claudecodesdk: exited after SIGTERM")
		return nil
	case <-time.After(5 * time.Second):
		slog.Warn("claudecodesdk: SIGTERM timed out, sending SIGKILL")
	}

	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	<-s.done
	return nil
}

// parseUserQuestions extracts structured questions from AskUserQuestion input.
func parseUserQuestions(input map[string]any) []core.UserQuestion {
	if input == nil {
		return nil
	}
	questionsRaw, ok := input["questions"].([]any)
	if !ok || len(questionsRaw) == 0 {
		return nil
	}
	var questions []core.UserQuestion
	for _, qRaw := range questionsRaw {
		qMap, ok := qRaw.(map[string]any)
		if !ok {
			continue
		}
		q := core.UserQuestion{
			Question:    mapStrVal(qMap, "question"),
			Header:      mapStrVal(qMap, "header"),
			MultiSelect: mapBoolVal(qMap, "multiSelect"),
		}
		if optsRaw, ok := qMap["options"].([]any); ok {
			for _, oRaw := range optsRaw {
				oMap, ok := oRaw.(map[string]any)
				if !ok {
					continue
				}
				q.Options = append(q.Options, core.UserQuestionOption{
					Label:       mapStrVal(oMap, "label"),
					Description: mapStrVal(oMap, "description"),
				})
			}
		}
		if q.Question != "" {
			questions = append(questions, q)
		}
	}
	return questions
}

func mapStrVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func mapBoolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}
