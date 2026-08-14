package dsh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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

// dshSession runs a multi-turn dsh conversation. Each Send() spawns
// `dsh --profile headless` as a one-shot process; the persisted dsh session
// (identified by the cc-connect-owned session id) is resumed on every run,
// so context carries across turns.
type dshSession struct {
	cmd       string
	extraArgs []string // extra args from cmd, prepended before dsh args
	workDir   string
	model     string // "" = use dsh settings default
	mode      string // "read-only" | "workspace-write" | "danger-full-access" | "confirm"
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value
	ctx       context.Context
	cancel    context.CancelFunc
	sendWg    sync.WaitGroup // tracks in-flight Send() calls
	alive     atomic.Bool
}

// newDSHSession creates a session. sessionID is the cc-connect-persisted dsh
// session id (empty on the first message); when empty, one is generated so
// every run after the first resumes the same conversation.
func newDSHSession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, sessionID string, extraEnv []string) (*dshSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &dshSession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       ctx,
		cancel:    cancel,
	}
	s.alive.Store(true)

	if sessionID != "" && sessionID != core.ContinueSession {
		s.sessionID.Store(sessionID)
	} else {
		s.sessionID.Store(fmt.Sprintf("session-cc-connect-%d-%d", time.Now().UnixNano(), os.Getpid()))
	}

	return s, nil
}

// ── Send ─────────────────────────────────────────────────────

// Send runs one headless dsh turn: spawns `dsh --profile headless
// --session-id <id> [--model <m>] [--mode <mode>] <prompt>`, waits for the
// process to finish, then emits the final assistant text (stdout) and the
// turn result. stderr lines are surfaced as EventError when the run fails.
func (s *dshSession) Send(msg string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.sendWg.Add(1)
	defer s.sendWg.Done()

	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	prompt := s.buildPrompt(msg, messageID, images, files)
	args := s.buildArgs(prompt)

	slog.Debug("dshSession: launching headless run", "cmd", s.cmd, "sessionID", s.CurrentSessionID(), "mode", s.mode, "model", s.model)

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("dsh: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	prepareCmdForKill(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("dsh: start: %w", err)
	}

	// Read the final assistant text (headless prints it on stdout and exits).
	output, readErr := readAllString(stdout)

	err = cmd.Wait()
	stderrMsg := strings.TrimSpace(stderrBuf.String())
	if err != nil {
		// emit() drops events once s.ctx is done (Close/cancel), so a
		// cancelled run surfaces no spurious error turn to the engine.
		slog.Error("dshSession: process error", "cmd", s.cmd, "error", err, "stderr", truncStr(stderrMsg, 1000))
	}

	text := strings.TrimSpace(output)
	if text != "" {
		s.emit(core.Event{Type: core.EventText, Content: text})
	}
	if readErr != nil {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: read stdout: %w", readErr)})
	}
	if stderrMsg != "" {
		// Surface actionable stderr (e.g. "dsh: MISSING_CREDENTIAL: ...").
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: %s", truncStr(stderrMsg, 2000))})
	}
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: %s", err)})
	}

	// Signal turn completion.
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
	return nil
}

// buildPrompt saves attachments to disk and appends file references so the
// dsh agent can read them with its own tools.
func (s *dshSession) buildPrompt(msg, messageID string, images []core.ImageAttachment, files []core.FileAttachment) string {
	var paths []string
	if len(files) > 0 {
		paths = append(paths, core.SaveFilesToDisk(s.workDir, messageID, files)...)
	}
	if len(images) > 0 {
		paths = append(paths, saveImages(s.workDir, messageID, images)...)
	}
	return core.AppendFileRefs(msg, paths)
}

// buildArgs assembles the dsh headless invocation. The prompt is the task
// positional; options come first so commander parses them reliably.
func (s *dshSession) buildArgs(prompt string) []string {
	args := append([]string{}, s.extraArgs...)
	args = append(args, "--profile", "headless", "--session-id", s.CurrentSessionID())
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.mode != "" {
		args = append(args, "--mode", s.mode)
	}
	return append(args, prompt)
}

func (s *dshSession) emit(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// ── AgentSession interface ──────────────────────────────────

func (s *dshSession) Events() <-chan core.Event {
	return s.events
}

func (s *dshSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *dshSession) Alive() bool {
	return s.alive.Load()
}

func (s *dshSession) RespondPermission(_ string, _ core.PermissionResult) error {
	// Headless runs are one-shot: there is no interactive permission channel,
	// so permission decisions are not forwarded. dsh's own approval seam
	// fails closed under headless (no answerer) — mode controls that behavior.
	return nil
}

func (s *dshSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	s.sendWg.Wait()
	close(s.events)
	return nil
}

// ── Helpers ──────────────────────────────────────────────────

func readAllString(r interface{ Read([]byte) (int, error) }) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var sb strings.Builder
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), err
	}
	return sb.String(), nil
}

// saveImages writes image attachments under workDir/.cc-connect/images and
// returns their absolute paths (dsh cannot ingest image bytes through the
// headless task text, so the agent is pointed at the files instead).
func saveImages(workDir, messageID string, images []core.ImageAttachment) []string {
	if len(images) == 0 {
		return nil
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	imgDir := filepath.Join(absDir, ".cc-connect", "images")
	if messageID != "" {
		imgDir = filepath.Join(imgDir, sanitizeName(messageID))
	}
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		slog.Error("dsh: create images dir", "error", err)
		return nil
	}
	var paths []string
	for i, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		ext := ".png"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		name := sanitizeName(img.FileName)
		if name == "" {
			name = fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		} else if !strings.HasSuffix(strings.ToLower(name), ext) {
			name = name + ext
		}
		p := filepath.Join(imgDir, name)
		if err := os.WriteFile(p, img.Data, 0o644); err != nil {
			slog.Error("dsh: save image failed", "error", err)
			continue
		}
		paths = append(paths, p)
	}
	return paths
}

func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}
