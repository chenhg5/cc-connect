package antigravity

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
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// antigravitySession manages multi-turn conversations with the Antigravity CLI (agy).
type antigravitySession struct {
	cmd       string
	workDir   string
	model     string
	mode      string
	timeout   time.Duration
	extraEnv  []string
	events    chan core.Event
	stdin     io.WriteCloser
	stdinMu   sync.Mutex
	closeOnce sync.Once
	permReqID atomic.Value // stores string
	chatID    atomic.Value // stores string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool
}

var permissionPromptPattern = regexp.MustCompile(`(?is)(allow|approve|permission).{0,400}(\(y/n\)|\(y\/n\)|\(y\/N\)|\(Y\/n\)|\[y\/n\]|\[y\/N\]|\[Y\/n\]|yes\/no)`)

func newAntigravitySession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string, timeout time.Duration) (*antigravitySession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	as := &antigravitySession{
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
	as.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		as.chatID.Store(resumeID)
	}

	return as, nil
}

func (as *antigravitySession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	// Capture existing chat logs so we can identify a new session on first turn
	preEntries := make(map[string]bool)
	homeDir, err := os.UserHomeDir()
	if err == nil {
		slug := antigravityProjectSlug(as.workDir)
		chatsDir := filepath.Join(homeDir, ".gemini", "tmp", slug, "chats")
		if entries, err := os.ReadDir(chatsDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
					preEntries[entry.Name()] = true
				}
			}
		}
	}

	// Save images and files into the workspace
	attachDir := filepath.Join(as.workDir, ".cc-connect", "attachments")
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
		if err := os.WriteFile(fpath, img.Data, 0o600); err == nil {
			imageRefs = append(imageRefs, fpath)
		}
	}

	var fileRefs []string
	for i, f := range files {
		fname := filepath.Base(f.FileName)
		if fname == "" || fname == "." || fname == ".." {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixMilli(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o600); err == nil {
			fileRefs = append(fileRefs, fpath)
		}
	}

	chatID := as.CurrentSessionID()
	isResume := chatID != ""

	// Attach image and file references to prompt
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
	args := buildAntigravityArgs(chatID, isResume, as.mode, fullPrompt)
	if strings.TrimSpace(as.model) != "" {
		slog.Warn("antigravitySession: model is configured but ignored because agy does not support --model yet", "model", as.model)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if as.timeout > 0 {
		ctx, cancel = context.WithTimeout(as.ctx, as.timeout)
	} else {
		ctx, cancel = context.WithCancel(as.ctx)
	}

	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	slog.Debug("antigravitySession: launching", "resume", isResume, "args", core.RedactArgs(args))
	cmd := exec.CommandContext(ctx, as.cmd, args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = as.workDir
	env := os.Environ()
	if len(as.extraEnv) > 0 {
		env = core.MergeEnv(env, as.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("antigravitySession: stdout pipe: %w", err)
	}
	var stdin io.WriteCloser
	if usesInteractivePermission(as.mode) {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("antigravitySession: stdin pipe: %w", err)
		}
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("antigravitySession: start: %w", err)
	}
	as.stdinMu.Lock()
	as.stdin = stdin
	as.stdinMu.Unlock()

	started = true
	as.wg.Add(1)
	go func() {
		defer cancel()
		as.readLoop(ctx, cmd, stdout, &stderrBuf, append(imageRefs, fileRefs...), preEntries, time.Now())
	}()

	return nil
}

func buildAntigravityArgs(chatID string, isResume bool, mode, fullPrompt string) []string {
	// Keep "-p <prompt>" at the very end because agy consumes the immediate next arg.
	args := make([]string, 0, 10)
	if isResume {
		args = append(args, "--conversation", chatID)
	}
	switch mode {
	case "yolo":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--sandbox")
	}
	args = append(args, "-p", fullPrompt)
	return args
}

func usesInteractivePermission(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "default")
}

func (as *antigravitySession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, tempFiles []string, preEntries map[string]bool, sendStartedAt time.Time) {
	defer as.wg.Done()
	defer func() {
		for _, f := range tempFiles {
			_ = os.Remove(f)
		}

		// Detect conversation ID if this was the first turn of a fresh session
		if as.CurrentSessionID() == "" {
			var sid string
			for attempt := 0; attempt < 15; attempt++ {
				sid = as.detectNewSessionID(preEntries, sendStartedAt)
				if sid != "" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if sid != "" {
				as.chatID.Store(sid)
				slog.Debug("antigravitySession: detected session ID", "session_id", sid)
				// Emit an EventText carrying the session ID back to core
				select {
				case as.events <- core.Event{Type: core.EventText, SessionID: sid}:
				case <-as.ctx.Done():
				}
			}
		}

		err := cmd.Wait()
		sid := as.CurrentSessionID()
		if err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("antigravitySession: process failed", "error", err, "stderr", stderrMsg)
				select {
				case as.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}:
				case <-as.ctx.Done():
				}
			}
		}

		// Finalize turn
		select {
		case as.events <- core.Event{Type: core.EventResult, SessionID: sid, Done: true}:
		case <-as.ctx.Done():
		}
	}()

	go func() {
		<-ctx.Done()
		_ = stdout.Close()
	}()

	reader := bufio.NewReader(stdout)
	buf := make([]byte, 1024)
	permWindow := ""

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			permWindow += text
			if len(permWindow) > 4096 {
				permWindow = permWindow[len(permWindow)-4096:]
			}
			if pending, _ := as.permReqID.Load().(string); pending == "" {
				if prompt, ok := extractPermissionPrompt(permWindow); ok {
					requestID := fmt.Sprintf("agy-perm-%d", time.Now().UnixNano())
					as.permReqID.Store(requestID)
					select {
					case as.events <- core.Event{
						Type:         core.EventPermissionRequest,
						RequestID:    requestID,
						ToolName:     "terminal_permission",
						ToolInput:    prompt,
						ToolInputRaw: map[string]any{"prompt": prompt},
					}:
					case <-as.ctx.Done():
						return
					}
				}
			}
			select {
			case as.events <- core.Event{Type: core.EventText, Content: text}:
			case <-as.ctx.Done():
				return
			}
		}
		if err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "file already closed") {
				slog.Error("antigravitySession: read error", "error", err)
				select {
				case as.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}:
				case <-as.ctx.Done():
				}
			}
			return
		}
	}
}

func (as *antigravitySession) detectNewSessionID(preEntries map[string]bool, sendStartedAt time.Time) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	slug := antigravityProjectSlug(as.workDir)
	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", slug, "chats")

	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		return ""
	}

	type candidate struct {
		sessionID string
		modTime   time.Time
		diff      time.Duration
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if preEntries[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		if !sendStartedAt.IsZero() && mod.Before(sendStartedAt.Add(-2*time.Second)) {
			continue
		}

		fpath := filepath.Join(chatsDir, entry.Name())
		file, err := os.Open(fpath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		if scanner.Scan() {
			var sf struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal([]byte(scanner.Text()), &sf) == nil && sf.SessionID != "" {
				diff := time.Duration(0)
				if !sendStartedAt.IsZero() {
					if mod.After(sendStartedAt) {
						diff = mod.Sub(sendStartedAt)
					} else {
						diff = sendStartedAt.Sub(mod)
					}
				}
				candidates = append(candidates, candidate{
					sessionID: sf.SessionID,
					modTime:   mod,
					diff:      diff,
				})
			}
		}
		_ = file.Close()
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].diff == candidates[j].diff {
			return candidates[i].modTime.After(candidates[j].modTime)
		}
		return candidates[i].diff < candidates[j].diff
	})
	return candidates[0].sessionID
}

func extractPermissionPrompt(text string) (string, bool) {
	loc := permissionPromptPattern.FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	prompt := strings.TrimSpace(text[loc[0]:loc[1]])
	if prompt == "" {
		return "", false
	}
	return prompt, true
}

func (as *antigravitySession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if pending, _ := as.permReqID.Load().(string); pending != "" && requestID != "" && requestID != pending {
		return fmt.Errorf("permission request mismatch: got %q, pending %q", requestID, pending)
	}
	as.stdinMu.Lock()
	defer as.stdinMu.Unlock()
	if as.stdin == nil {
		return fmt.Errorf("stdin is not available")
	}
	// agy permission prompts accept terminal-style responses.
	// Keep this conservative until agy exposes a structured permission protocol.
	reply := "y\n"
	if strings.EqualFold(result.Behavior, "deny") {
		reply = "n\n"
	}
	_, err := io.WriteString(as.stdin, reply)
	if err != nil {
		return fmt.Errorf("write permission response: %w", err)
	}
	as.permReqID.Store("")
	return nil
}

func (as *antigravitySession) Events() <-chan core.Event {
	return as.events
}

func (as *antigravitySession) CurrentSessionID() string {
	v, _ := as.chatID.Load().(string)
	return v
}

func (as *antigravitySession) Alive() bool {
	return as.alive.Load()
}

func (as *antigravitySession) Close() error {
	as.alive.Store(false)
	as.cancel()
	as.stdinMu.Lock()
	if as.stdin != nil {
		_ = as.stdin.Close()
		as.stdin = nil
	}
	as.stdinMu.Unlock()
	done := make(chan struct{})
	go func() {
		as.wg.Wait()
		as.closeOnce.Do(func() {
			close(as.events)
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("antigravitySession: close timed out")
	}
	return nil
}
