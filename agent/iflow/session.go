package iflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

var sessionIDRe = regexp.MustCompile(`"session-id"\s*:\s*"([^"]+)"`)

// iflowSession manages multi-turn conversations with iFlow CLI.
// Each Send() runs a new `iflow -p` process and resumes via -r <session-id>.
type iflowSession struct {
	cmd       string
	workDir   string
	model     string
	mode      string
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value // stores string
	sentOnce  atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool
}

func newIFlowSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*iflowSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &iflowSession{
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
		s.sentOnce.Store(true)
	}

	return s, nil
}

func (s *iflowSession) Send(prompt string, images []core.ImageAttachment) error {
	if len(images) > 0 {
		slog.Warn("iflowSession: images are not supported in non-interactive mode, ignoring")
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	args := make([]string, 0, 16)
	if s.model != "" {
		args = append(args, "-m", s.model)
	}

	switch s.mode {
	case "yolo":
		args = append(args, "--yolo")
	case "plan":
		args = append(args, "--plan")
	case "auto-edit":
		args = append(args, "--autoEdit")
	default:
		args = append(args, "--default")
	}

	sid := s.CurrentSessionID()
	if sid != "" {
		args = append(args, "-r", sid)
	} else if s.sentOnce.Load() {
		args = append(args, "-c")
	}

	execInfoFile, err := os.CreateTemp("", "iflow-exec-info-*.json")
	if err != nil {
		return fmt.Errorf("iflowSession: create execution info file: %w", err)
	}
	execInfoPath := execInfoFile.Name()
	execInfoFile.Close()

	args = append(args, "-p", prompt, "-o", execInfoPath)
	slog.Debug("iflowSession: launching", "resume", sid != "", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.Remove(execInfoPath)
		return fmt.Errorf("iflowSession: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.Remove(execInfoPath)
		return fmt.Errorf("iflowSession: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		os.Remove(execInfoPath)
		return fmt.Errorf("iflowSession: start: %w", err)
	}

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, stderr, execInfoPath)
	return nil
}

func (s *iflowSession) readLoop(cmd *exec.Cmd, stdout, stderr io.ReadCloser, execInfoPath string) {
	defer s.wg.Done()
	defer os.Remove(execInfoPath)

	var stdoutBuf, stderrBuf bytes.Buffer
	var ioWG sync.WaitGroup
	ioWG.Add(2)
	go func() {
		defer ioWG.Done()
		_, _ = io.Copy(&stdoutBuf, stdout)
	}()
	go func() {
		defer ioWG.Done()
		_, _ = io.Copy(&stderrBuf, stderr)
	}()

	waitErr := cmd.Wait()
	ioWG.Wait()

	stdoutText := strings.TrimSpace(stdoutBuf.String())
	stderrText := strings.TrimSpace(stderrBuf.String())

	sid, _ := readExecutionInfoSessionID(execInfoPath)
	if sid == "" {
		sid = extractSessionIDFromExecutionInfo(stderrText)
	}
	if sid != "" {
		s.sessionID.Store(sid)
	}
	s.sentOnce.Store(true)

	if waitErr != nil {
		s.emitEvent(core.Event{Type: core.EventError, Error: summarizeIFlowError(stderrText, waitErr)})
		return
	}

	if stdoutText == "" {
		if isIFlowAPIFailure(stderrText) {
			s.emitEvent(core.Event{Type: core.EventError, Error: summarizeIFlowError(stderrText, nil)})
			return
		}
		s.emitEvent(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
		return
	}

	s.emitEvent(core.Event{
		Type:      core.EventResult,
		Content:   stdoutText,
		SessionID: s.CurrentSessionID(),
		Done:      true,
	})
}

func (s *iflowSession) emitEvent(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func readExecutionInfoSessionID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var payload struct {
		SessionID string `json:"session-id"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.SessionID), nil
}

func extractSessionIDFromExecutionInfo(stderrText string) string {
	m := sessionIDRe.FindStringSubmatch(stderrText)
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func isIFlowAPIFailure(stderrText string) bool {
	if stderrText == "" {
		return false
	}
	lower := strings.ToLower(stderrText)
	if strings.Contains(lower, "error when talking to iflow api") {
		return true
	}
	if strings.Contains(lower, "generate data error") {
		return true
	}
	if strings.Contains(lower, "retrying with backoff") && strings.Contains(lower, "fetch failed") {
		return true
	}
	return false
}

func summarizeIFlowError(stderrText string, waitErr error) error {
	if stderrText != "" {
		for _, line := range strings.Split(stderrText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "<Execution Info>") || strings.HasPrefix(line, "</Execution Info>") {
				continue
			}
			if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "}") || strings.HasPrefix(line, "\"") {
				continue
			}
			if utf8.RuneCountInString(line) > 300 {
				line = string([]rune(line)[:300]) + "..."
			}
			return fmt.Errorf("%s", line)
		}
	}
	if waitErr != nil {
		return fmt.Errorf("iflow process failed: %w", waitErr)
	}
	return fmt.Errorf("iflow API request failed")
}

func (s *iflowSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *iflowSession) Events() <-chan core.Event {
	return s.events
}

func (s *iflowSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *iflowSession) Alive() bool {
	return s.alive.Load()
}

func (s *iflowSession) Close() error {
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
		slog.Warn("iflowSession: close timed out, abandoning wg.Wait")
	}
	close(s.events)
	return nil
}
