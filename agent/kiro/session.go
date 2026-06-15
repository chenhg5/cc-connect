package kiro

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

var sessionIDPattern = regexp.MustCompile(`(?i)\bSession(?:\s+ID)?:\s*([A-Za-z0-9._:-]+)`)

type kiroSession struct {
	cmd          string
	workDir      string
	model        string
	mode         string
	agentProfile string
	agentEngine  string
	kasMode      string
	trustTools   string
	extraEnv     []string
	events       chan core.Event
	sessionID    atomic.Value // stores string
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	alive        atomic.Bool
}

func newKiroSession(ctx context.Context, cmd, workDir, model, mode, agentProfile, agentEngine, kasMode, trustTools, resumeID string, extraEnv []string) (*kiroSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	ks := &kiroSession{
		cmd:          cmd,
		workDir:      workDir,
		model:        model,
		mode:         mode,
		agentProfile: agentProfile,
		agentEngine:  agentEngine,
		kasMode:      kasMode,
		trustTools:   trustTools,
		extraEnv:     extraEnv,
		events:       make(chan core.Event, 64),
		ctx:          sessionCtx,
		cancel:       cancel,
	}
	ks.alive.Store(true)
	if resumeID != "" && resumeID != core.ContinueSession {
		ks.sessionID.Store(resumeID)
	}
	return ks, nil
}

func (ks *kiroSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !ks.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(images) > 0 || len(files) > 0 {
		filePaths := core.SaveFilesToDisk(ks.workDir, files)
		imagePaths := saveKiroImages(ks.workDir, images)
		prompt = core.AppendFileRefs(prompt, append(imagePaths, filePaths...))
	}

	args := ks.buildArgs(prompt)
	slog.Debug("kiroSession: launching", "resume", ks.CurrentSessionID() != "", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(ks.ctx, ks.cmd, args...)
	cmd.Dir = ks.workDir
	if len(ks.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), ks.extraEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("kiroSession: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kiroSession: start: %w", err)
	}

	ks.wg.Add(1)
	go ks.readLoop(cmd, stdout, &stderrBuf)
	return nil
}

func (ks *kiroSession) buildArgs(prompt string) []string {
	args := []string{"chat", "--no-interactive"}
	if sid := ks.CurrentSessionID(); sid != "" {
		args = append(args, "--resume-id", sid)
	}
	if ks.mode == "yolo" {
		args = append(args, "--trust-all-tools")
	} else if ks.trustTools != "" {
		args = append(args, "--trust-tools", ks.trustTools)
	}
	if ks.model != "" && ks.model != "auto" {
		args = append(args, "--model", ks.model)
	}
	if ks.agentProfile != "" {
		args = append(args, "--agent", ks.agentProfile)
	}
	if ks.agentEngine != "" {
		args = append(args, "--agent-engine", ks.agentEngine)
	}
	if ks.kasMode != "" {
		args = append(args, "--mode", ks.kasMode)
	}
	return append(args, prompt)
}

func (ks *kiroSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer ks.wg.Done()

	var lines []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := stripANSI(scanner.Text())
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if sid := extractSessionID(line); sid != "" {
			ks.sessionID.Store(sid)
		}
	}
	scanErr := scanner.Err()
	exitErr := cmd.Wait()

	content := cleanKiroOutput(strings.Join(lines, "\n"))
	if sid := extractSessionID(content); sid != "" {
		ks.sessionID.Store(sid)
	}

	if scanErr != nil {
		ks.send(core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", scanErr)})
		return
	}
	if exitErr != nil {
		stderrMsg := strings.TrimSpace(stripANSI(stderrBuf.String()))
		if stderrMsg == "" {
			stderrMsg = content
		}
		if stderrMsg == "" {
			stderrMsg = exitErr.Error()
		}
		slog.Error("kiroSession: process failed", "error", exitErr, "stderr", truncStr(stderrMsg, 200))
		ks.send(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)})
		return
	}
	if content != "" {
		ks.send(core.Event{Type: core.EventResult, Content: content, SessionID: ks.CurrentSessionID(), Done: true})
		return
	}
	ks.send(core.Event{Type: core.EventResult, SessionID: ks.CurrentSessionID(), Done: true})
}

func (ks *kiroSession) send(ev core.Event) {
	select {
	case ks.events <- ev:
	case <-ks.ctx.Done():
	}
}

func saveKiroImages(workDir string, images []core.ImageAttachment) []string {
	if len(images) == 0 {
		return nil
	}
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		attachDir = os.TempDir()
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
		path := filepath.Join(attachDir, fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext))
		if err := os.WriteFile(path, img.Data, 0o644); err != nil {
			slog.Warn("kiroSession: failed to save image", "error", err)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func extractSessionID(text string) string {
	m := sessionIDPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func cleanKiroOutput(text string) string {
	text = stripANSI(text)
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "▰") || strings.Contains(trimmed, "Opening browser") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

func (ks *kiroSession) RespondPermission(_ string, _ core.PermissionResult) error { return nil }
func (ks *kiroSession) Events() <-chan core.Event                                 { return ks.events }

func (ks *kiroSession) CurrentSessionID() string {
	v, _ := ks.sessionID.Load().(string)
	return v
}

func (ks *kiroSession) Alive() bool { return ks.alive.Load() }

func (ks *kiroSession) Close() error {
	ks.alive.Store(false)
	ks.cancel()
	done := make(chan struct{})
	go func() {
		ks.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(ks.events)
	case <-time.After(8 * time.Second):
		slog.Warn("kiroSession: close timed out, abandoning wg.Wait")
	}
	return nil
}
