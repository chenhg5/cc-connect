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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// antigravitySession manages multi-turn conversations with the Antigravity CLI (agy).
type antigravitySession struct {
	cmd              string
	extraArgs        []string // extra args from cmd, prepended before agy args
	workDir          string
	model            string
	mode             string
	timeout          time.Duration
	extraEnv         []string
	events           chan core.Event
	closeOnce        sync.Once
	chatID           atomic.Value // stores string
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	alive            atomic.Bool
	permissionBridge *agyPermissionBridge
	cachedUsage      func() *core.UsageReport // account quota view from the Agent (maomao fork)
}

func newAntigravitySession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, resumeID string, extraEnv []string, timeout time.Duration) (*antigravitySession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	as := &antigravitySession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		timeout:   timeout,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	as.alive.Store(true)

	if mode == "default" {
		bridge, err := newAgyPermissionBridge(sessionCtx, as.events)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("antigravity: initialize permission bridge: %w", err)
		}
		as.permissionBridge = bridge
	}

	if resumeID != "" && resumeID != core.ContinueSession {
		as.chatID.Store(resumeID)
	}

	return as, nil
}

func (as *antigravitySession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
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
	core.ArchiveAttachments(append(append([]string(nil), imageRefs...), fileRefs...)) // maomao fork: NAS media library

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
	if strings.TrimSpace(as.model) != "" {
		slog.Warn("antigravitySession: model is configured but ignored because agy does not support --model yet", "model", as.model)
	}
	return as.launch(agyTurn{fullPrompt: fullPrompt, tempFiles: append(imageRefs, fileRefs...)})
}

// agyTurn is one user turn, kept so the turn can be launched a second time.
type agyTurn struct {
	fullPrompt string   // prompt with attachment references, as first sent
	tempFiles  []string // attachments; removed when the turn is over, not per process
	retried    bool     // this launch is the one auth retry
	// continueConv: the failed launch got agy's "init", so the conversation
	// holds this turn's message and the retry asks agy to go on with it.
	continueConv bool
}

// agyAuthRetryPrompt continues a turn that agy dropped because its login
// token expired mid-turn (agy 1.4.0 does not refresh it in a running process,
// google-antigravity/antigravity-cli#1091; a new process does).
const agyAuthRetryPrompt = "上一步因为登录令牌过期被中断了（不是你的问题），请接着把刚才没做完的事做完。"

// isAgyAuthExpired reports whether agy's stderr names an expired login token:
// a line `AGY_ERROR: {..., "status":"UNAUTHENTICATED", "error_code":401, ...}`
// (cc-connect.log 2026-10-18 16:00).
func isAgyAuthExpired(stderr string) bool {
	for _, line := range strings.Split(stderr, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "AGY_ERROR:")
		if !ok {
			continue
		}
		var e struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(rest)), &e) == nil && e.Status == "UNAUTHENTICATED" {
			return true
		}
	}
	return false
}

// agyStatusError is readLoop's message for a result with a non-SUCCESS status.
func agyStatusError(status string) string {
	return "agy turn ended with status " + status
}

// launch starts one agy process for the turn and hands it to readLoop, which
// finishes the turn (or launches the retry). On error nothing was started.
func (as *antigravitySession) launch(t agyTurn) error {
	chatID := as.CurrentSessionID()
	isResume := chatID != ""
	prompt := t.fullPrompt
	if t.retried && t.continueConv && isResume {
		// The conversation already holds the user's message and whatever
		// agy did before the 401, so ask it to go on instead of sending the
		// message a second time. If the failed process never reported
		// "init", agy did not take the turn in, so the original prompt is
		// resent (its attachments are still on disk), into the same
		// conversation when there is one.
		prompt = agyAuthRetryPrompt
	}

	agyConfigDir := ""
	if as.permissionBridge != nil {
		agyConfigDir = as.permissionBridge.AgyConfigDir()
	}
	args := as.buildAntigravityArgs(chatID, isResume, as.mode, agyConfigDir, prompt)

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

	slog.Debug("antigravitySession: launching", "resume", isResume, "retry", t.retried, "args", core.RedactArgs(args))
	cmd := exec.CommandContext(ctx, as.cmd, args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = as.workDir
	env := os.Environ()
	if len(as.extraEnv) > 0 {
		env = core.MergeEnv(env, as.extraEnv)
	}
	if as.permissionBridge != nil {
		env = core.MergeEnv(env, as.permissionBridge.Env())
	}
	cmd.Env = env

	// Keep stdin disconnected: agy --print consumes piped stdin to EOF before
	// processing the prompt, so an open pipe would deadlock the turn.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("antigravitySession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("antigravitySession: start: %w", err)
	}

	started = true
	as.wg.Add(1)
	go func() {
		defer cancel()
		as.readLoop(ctx, cmd, stdout, &stderrBuf, t)
	}()

	return nil
}

func (as *antigravitySession) buildAntigravityArgs(chatID string, isResume bool, mode, agyConfigDir, fullPrompt string) []string {
	// Prepend extra args from cmd so wrappers like "timeout 3600 agy" work.
	// Keep "-p <prompt>" at the very end because agy consumes the immediate next arg.
	args := append([]string{}, as.extraArgs...)
	if agyConfigDir != "" {
		// Antigravity currently names this compatibility flag --gemini_dir.
		args = append(args, "--gemini_dir="+agyConfigDir, "--print-timeout=24h")
	}
	if isResume {
		args = append(args, "--conversation", chatID)
	}
	// stream-json carries the conversation id agy assigned to this very
	// process, so a new session learns its id without scanning shared state.
	args = append(args, "--output-format", "stream-json")
	switch mode {
	case "yolo":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--sandbox")
	}
	args = append(args, "-p", fullPrompt)
	return args
}

func (as *antigravitySession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, t agyTurn) {
	// Runs last, so a retry launched below is counted before this one is done.
	defer as.wg.Done()
	resultErr := ""
	sawInit := false    // this process reported "init"
	replied := false    // the turn already streamed reply text
	readFailed := false // a read error was already reported to core
	defer func() {
		err := cmd.Wait()

		if err != nil || resultErr != "" {
			// A failed result names the status; stderr says why.
			stderr := strings.TrimSpace(stderrBuf.String())
			msg := stderr
			if resultErr != "" && msg != "" {
				msg = resultErr + ": " + msg
			} else if resultErr != "" {
				msg = resultErr
			}
			switch {
			case msg == "":
			case resultErr == agyStatusError("ERROR") && replied && err == nil && stderr == "":
				// agy keeps the last failed step of a conversation and
				// reports status ERROR (exit 0, nothing on stderr) on every
				// later turn of it, though the turn itself succeeded:
				// cc-connect.log 2026-10-17 17:30-19:09, result lines carry
				// the earlier 401 with a cumulative duration_seconds, and
				// the agy cli log shows only "step 211 ... CANCELED" again.
				slog.Warn("antigravitySession: ignoring stale failed status after a reply", "status", resultErr)
			case !replied && !t.retried && !readFailed && ctx.Err() == nil && as.ctx.Err() == nil && isAgyAuthExpired(stderr):
				// The login token expired mid-turn; a new agy process
				// refreshes it. Retry once in place of the error and leave
				// the attachments and the turn's EventResult to the retry.
				slog.Warn("antigravitySession: login token expired mid-turn, retrying once", "error", err, "stderr", msg, "session_id", as.CurrentSessionID())
				next := t
				next.retried = true
				next.continueConv = sawInit
				lerr := as.launch(next)
				if lerr == nil {
					return
				}
				msg += "\n(retry failed to start: " + lerr.Error() + ")"
				fallthrough
			default:
				slog.Error("antigravitySession: process failed", "error", err, "stderr", msg, "retried", t.retried)
				// core drops a turn's text on EventError, so once agy has
				// written the reply, deliver it and put the error under it.
				ev := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg)}
				if replied {
					ev = core.Event{Type: core.EventText, Content: "\n\n⚠️ " + msg}
				}
				select {
				case as.events <- ev:
				case <-as.ctx.Done():
				}
			}
		}

		for _, f := range t.tempFiles {
			_ = os.Remove(f)
		}

		// Finalize turn.
		sid := as.CurrentSessionID()
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
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !sawInit {
				// parseStreamLine passes "init" on only for a new id, so
				// look at the event name here; "init" is the first line.
				var head struct {
					Event string `json:"event"`
				}
				sawInit = json.Unmarshal(bytes.TrimSpace(line), &head) == nil && head.Event == "init"
			}
			ev, ok := as.parseStreamLine(line)
			if ok && ev.Type == core.EventError {
				resultErr = ev.Error.Error()
				slog.Warn("antigravitySession: failed result", "line", string(bytes.TrimSpace(line)))
			} else if ok {
				if ev.Content != "" {
					replied = true
				}
				select {
				case as.events <- ev:
				case <-as.ctx.Done():
					return
				}
			}
		}
		if err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "file already closed") {
				slog.Error("antigravitySession: read error", "error", err)
				readFailed = true
				select {
				case as.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}:
				case <-as.ctx.Done():
				}
			}
			return
		}
	}
}

// agyStreamLine is one line of agy --output-format stream-json (agy 1.3.5,
// checked again on 1.4.0): "init" first, then "step_update" per step, then
// "result". A tool step comes twice with the same step_index: "ACTIVE" with
// its parameters, then "DONE" with its output as well.
type agyStreamLine struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	StepUpdate     *struct {
		StepType  string `json:"step_type"`
		State     string `json:"state"`
		TextDelta string `json:"text_delta"`
		ToolName  string `json:"tool_name"`
		ToolInfo  *struct {
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
	} `json:"result"`
}

// parseStreamLine turns one stdout line into the event core sees. Reply text
// is what text mode prints: every agent_response delta. Each tool step is
// passed on once, when it starts, as EventToolUse; besides showing progress
// this keeps core's idle timer from killing a turn that runs tools for longer
// than idle_timeout_mins without writing text. A line not starting
// with "{" is passed on as text; a "{" line that does not parse (the last,
// cut-off line of a killed process) is dropped. A failed result comes back
// as EventError for readLoop to report after the process exits.
func (as *antigravitySession) parseStreamLine(line []byte) (core.Event, bool) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return core.Event{Type: core.EventText, Content: string(line)}, true
	}
	var l agyStreamLine
	if err := json.Unmarshal(trimmed, &l); err != nil {
		slog.Warn("antigravitySession: dropping unparsable stream-json line", "error", err, "bytes", len(trimmed))
		return core.Event{}, false
	}
	switch l.Event {
	case "init":
		// agy starts a new conversation when --conversation names one it
		// does not have, so always adopt the id it reports.
		if id := l.ConversationID; id != "" && id != as.CurrentSessionID() {
			as.chatID.Store(id)
			slog.Debug("antigravitySession: conversation id", "session_id", id)
			return core.Event{Type: core.EventText, SessionID: id}, true
		}
	case "step_update":
		u := l.StepUpdate
		if u == nil {
			break
		}
		if u.StepType == "agent_response" && u.TextDelta != "" {
			return core.Event{Type: core.EventText, Content: u.TextDelta}, true
		}
		// The DONE line repeats the step; reporting it too would show the
		// tool twice.
		if u.StepType == "tool" && u.State == "ACTIVE" {
			input := ""
			if u.ToolInfo != nil && len(u.ToolInfo.Parameters) > 0 {
				input = string(u.ToolInfo.Parameters)
			}
			return core.Event{Type: core.EventToolUse, ToolName: u.ToolName, ToolInput: input}, true
		}
	case "result":
		if r := l.Result; r != nil && r.Status != "" && r.Status != "SUCCESS" {
			return core.Event{Type: core.EventError, Error: fmt.Errorf("%s", agyStatusError(r.Status))}, true
		}
	}
	return core.Event{}, false
}

func (as *antigravitySession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if as.permissionBridge == nil {
		return fmt.Errorf("antigravity: permission responses are only available in default mode")
	}
	return as.permissionBridge.RespondPermission(requestID, result)
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
	if as.permissionBridge != nil {
		as.permissionBridge.Close()
	}
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

// CachedUsage implements core.CachedUsageReporter (maomao fork). It never
// waits on the network: it returns the Agent's last quota snapshot and lets
// the Agent refresh it in the background.
func (s *antigravitySession) CachedUsage() *core.UsageReport {
	if s.cachedUsage == nil {
		return nil
	}
	return s.cachedUsage()
}
