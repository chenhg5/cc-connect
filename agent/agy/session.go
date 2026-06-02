package agy

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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// agySession manages multi-turn conversations with the Antigravity CLI.
// Each Send starts one agy subprocess; session continuity uses --conversation.
type agySession struct {
	cmd            string
	workDir        string
	extraEnv       []string
	systemPrompt   string
	platformPrompt string
	timeout        time.Duration

	events         chan core.Event
	conversationID atomic.Value // stores string
	systemInjected atomic.Bool
	outputMu       sync.Mutex
	lastOutput     string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	alive  atomic.Bool

	cmdMu sync.Mutex
	cmds  map[*exec.Cmd]struct{}
}

func newAgySession(ctx context.Context, cmd, workDir, resumeID string, timeout time.Duration) (*agySession, error) {
	return newAgySessionWithOptions(ctx, cmd, workDir, resumeID, nil, "", "", timeout)
}

func newAgySessionWithOptions(ctx context.Context, cmd, workDir, resumeID string, extraEnv []string, systemPrompt, platformPrompt string, timeout time.Duration) (*agySession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &agySession{
		cmd:            cmd,
		workDir:        workDir,
		extraEnv:       append([]string(nil), extraEnv...),
		systemPrompt:   systemPrompt,
		platformPrompt: platformPrompt,
		timeout:        timeout,
		events:         make(chan core.Event, 64),
		ctx:            sessionCtx,
		cancel:         cancel,
		cmds:           make(map[*exec.Cmd]struct{}),
	}
	s.alive.Store(true)
	if resumeID != "" && resumeID != core.ContinueSession {
		s.conversationID.Store(resumeID)
	}
	return s, nil
}

func (s *agySession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("agy: session closed")
	}

	promptWithAttachments, stagedFiles := s.stageAttachments(prompt, images, files)
	fullPrompt := s.applySystemPrompt(promptWithAttachments)
	beforeConversations := snapshotAgyConversations()

	ctx, cancel := s.turnContext()
	started := false
	defer func() {
		if !started {
			cancel()
			removeFiles(stagedFiles)
		}
	}()

	args := s.buildArgs()
	slog.Debug("agySession: launching", "conversation_id", s.CurrentSessionID(), "args", core.RedactArgs(args))

	cmd := exec.CommandContext(ctx, s.cmd, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = s.workDir
	cmd.Env = os.Environ()
	if len(s.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(cmd.Env, s.extraEnv)
	}
	cmd.Stdin = strings.NewReader(fullPrompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("agySession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agySession: start: %w", err)
	}

	s.addCmd(cmd)
	started = true
	s.wg.Add(1)
	go func() {
		defer cancel()
		defer removeFiles(stagedFiles)
		s.readLoop(ctx, cmd, stdout, &stderrBuf, beforeConversations)
	}()

	return nil
}

func (s *agySession) buildArgs() []string {
	args := []string{"--dangerously-skip-permissions"}
	if s.workDir != "" {
		args = append(args, "--add-dir", s.workDir)
	}
	if conversationID := s.CurrentSessionID(); conversationID != "" {
		args = append(args, "--conversation", conversationID)
	}
	return append(args, "-p", "-")
}

func (s *agySession) turnContext() (context.Context, context.CancelFunc) {
	if s.timeout > 0 {
		return context.WithTimeout(s.ctx, s.timeout)
	}
	return context.WithCancel(s.ctx)
}

func (s *agySession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, before map[string]time.Time) {
	defer s.wg.Done()
	defer s.removeCmd(cmd)

	go func() {
		<-ctx.Done()
		_ = stdout.Close()
	}()

	previousOutput := s.previousOutput()
	text, authFromStdout, scanErr := s.readStream(stdout, previousOutput)
	waitErr := cmd.Wait()

	if s.ctx.Err() != nil {
		return
	}

	if scanErr != nil {
		slog.Error("agySession: scanner error", "error", scanErr)
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("agy: read stdout: %w", scanErr)})
		s.emitResult()
		return
	}

	stderrText := strings.TrimSpace(stderrBuf.String())
	combined := strings.TrimSpace(text + "\n" + stderrText)
	if authFromStdout || isAgyAuthError(combined) {
		slog.Debug("agySession: authentication required")
		s.emit(core.Event{
			Type:  core.EventError,
			Error: fmt.Errorf("agy: authentication required; run agy interactively to log in first"),
		})
		s.emitResult()
		return
	}

	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("agy: command timed out")})
			s.emitResult()
			return
		}
		msg := stderrText
		if msg == "" {
			msg = waitErr.Error()
		}
		slog.Error("agySession: process failed", "error", waitErr, "stderr", truncateAgyLog(msg, 500))
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("agy: %s", msg)})
		s.emitResult()
		return
	}

	if s.CurrentSessionID() == "" {
		if id := latestChangedAgyConversationID(before); id != "" {
			s.conversationID.Store(id)
			slog.Debug("agySession: conversation id inferred", "conversation_id", id)
		}
	}

	if strings.TrimSpace(text) != "" {
		s.setLastOutput(text)
	}
	s.emitResult()
}

func (s *agySession) applySystemPrompt(prompt string) string {
	systemPrompt := strings.TrimSpace(s.systemPrompt)
	if systemPrompt == "" || !s.systemInjected.CompareAndSwap(false, true) {
		return prompt
	}
	if platformPrompt := strings.TrimSpace(s.platformPrompt); platformPrompt != "" {
		systemPrompt += "\n\n## Formatting\n" + platformPrompt
	}
	if strings.TrimSpace(prompt) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n## User message\n" + prompt
}

func (s *agySession) readStream(stdout io.Reader, duplicatePrefix string) (string, bool, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var b strings.Builder
	var authDetected bool
	stripper := newPrefixStripper(duplicatePrefix)
	for scanner.Scan() {
		line := scanner.Text()
		chunk := line + "\n"
		b.WriteString(chunk)
		if authDetected || isAgyAuthError(line) {
			authDetected = true
			continue
		}
		out := stripper.Push(chunk)
		if out == "" {
			continue
		}
		s.emit(core.Event{Type: core.EventText, Content: out})
	}
	return b.String(), authDetected, scanner.Err()
}

func (s *agySession) previousOutput() string {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	return s.lastOutput
}

func (s *agySession) setLastOutput(text string) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	s.lastOutput = strings.TrimRight(text, "\r\n")
}

type prefixStripper struct {
	prefix string
	buf    strings.Builder
	done   bool
}

func newPrefixStripper(prefix string) *prefixStripper {
	return &prefixStripper{prefix: strings.TrimRight(prefix, "\r\n")}
}

func (p *prefixStripper) Push(chunk string) string {
	if p.done || p.prefix == "" {
		return chunk
	}

	p.buf.WriteString(chunk)
	buffered := p.buf.String()
	if len(buffered) < len(p.prefix) {
		if strings.HasPrefix(p.prefix, buffered) {
			return ""
		}
		p.done = true
		return buffered
	}

	if strings.HasPrefix(buffered, p.prefix) {
		p.done = true
		return strings.TrimLeft(buffered[len(p.prefix):], "\r\n")
	}

	p.done = true
	return buffered
}

func (s *agySession) stageAttachments(prompt string, images []core.ImageAttachment, files []core.FileAttachment) (string, []string) {
	if len(images) == 0 && len(files) == 0 {
		return prompt, nil
	}

	attachDir := filepath.Join(s.workDir, ".cc-connect", "attachments", fmt.Sprintf("agy_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("agySession: failed to create attachment dir", "error", err)
		attachDir = os.TempDir()
	}

	var refs []string
	var paths []string
	for i, img := range images {
		ext := imageExt(img.MimeType)
		path := filepath.Join(attachDir, fmt.Sprintf("image_%d%s", i, ext))
		if err := os.WriteFile(path, img.Data, 0o644); err != nil {
			slog.Warn("agySession: failed to save image", "error", err)
			continue
		}
		paths = append(paths, path)
		refs = append(refs, path)
	}
	for i, file := range files {
		name := safeAttachmentName(file.FileName)
		if name == "" {
			name = fmt.Sprintf("file_%d", i)
		}
		path := filepath.Join(attachDir, fmt.Sprintf("%d_%s", i, name))
		if err := os.WriteFile(path, file.Data, 0o644); err != nil {
			slog.Warn("agySession: failed to save file", "error", err)
			continue
		}
		paths = append(paths, path)
		refs = append(refs, path)
	}

	if len(refs) == 0 {
		return prompt, nil
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Please analyze the attached file(s)."
	}
	return prompt + "\n\n[Attached files saved at: " + strings.Join(refs, ", ") + "]", paths
}

func imageExt(mimeType string) string {
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

func safeAttachmentName(name string) string {
	name = filepath.ToSlash(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

func removeFiles(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func isAgyAuthError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "authentication interrupted")
}

func snapshotAgyConversations() map[string]time.Time {
	root, err := agyConversationsDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	out := make(map[string]time.Time, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".pb" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".pb")
		if isLikelyUUID(id) {
			out[id] = info.ModTime()
		}
	}
	return out
}

func latestChangedAgyConversationID(before map[string]time.Time) string {
	after := snapshotAgyConversations()
	var latestID string
	var latestMod time.Time
	for id, mod := range after {
		oldMod, existed := before[id]
		if existed && !mod.After(oldMod) {
			continue
		}
		if latestID == "" || mod.After(latestMod) {
			latestID = id
			latestMod = mod
		}
	}
	return latestID
}

func agyConversationsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "conversations"), nil
}

func isLikelyUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

func truncateAgyLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (s *agySession) emitResult() {
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
}

func (s *agySession) emit(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func (s *agySession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *agySession) Events() <-chan core.Event {
	return s.events
}

func (s *agySession) CurrentSessionID() string {
	id, _ := s.conversationID.Load().(string)
	return id
}

func (s *agySession) Alive() bool {
	return s.alive.Load()
}

func (s *agySession) Close() error {
	if !s.alive.CompareAndSwap(true, false) {
		return nil
	}
	s.cancel()
	s.killCommands()
	s.wg.Wait()
	close(s.events)
	return nil
}

func (s *agySession) addCmd(cmd *exec.Cmd) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	s.cmds[cmd] = struct{}{}
}

func (s *agySession) removeCmd(cmd *exec.Cmd) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	delete(s.cmds, cmd)
}

func (s *agySession) killCommands() {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	for cmd := range s.cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}
