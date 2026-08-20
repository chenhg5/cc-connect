package dsh

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

// dshSession runs a multi-turn dsh conversation. Each Send() spawns
// `dsh --profile headless --provider ... --model ... --reasoning-effort ...
// --jsonl` as a one-shot process; the persisted dsh session (identified by the
// cc-connect-owned session id) is resumed on every run, so context carries
// across turns.
//
// With --jsonl the dsh headless runner streams events on stdout (text /
// thinking deltas, tool calls, approval requests, result/done envelopes) and
// reads approval responses from stdin, so cc-connect can show tool progress
// in the chat and relay permission decisions from the human (Feishu card).
type dshSession struct {
	cmd             string
	extraArgs       []string // extra args from cmd, prepended before dsh args
	workDir         string
	provider        string // provider route override ("" = use dsh settings default)
	model           string // "" = use dsh settings default
	reasoningEffort string // "" = use dsh settings default
	mode            string // "read-only" | "workspace-write" | "danger-full-access" | "confirm"
	preset          string // dsh agent preset requested for this session's next run
	extraEnv        []string
	events          chan core.Event
	sessionID       atomic.Value
	ctx             context.Context
	cancel          context.CancelFunc
	sendWg          sync.WaitGroup // tracks in-flight Send() calls
	alive           atomic.Bool

	// per-run process wiring (nil between runs)
	runMu   sync.Mutex // guards stdin + pendingApprovals
	stdin   io.WriteCloser
	pending map[string]struct{} // approval ids awaiting a human decision

}

// newDSHSession creates a session. sessionID is the cc-connect-persisted dsh
// session id (empty on the first message); when empty, one is generated so
// every run after the first resumes the same conversation.
func newDSHSession(ctx context.Context, cmd string, extraArgs []string, workDir, provider, model, reasoningEffort, mode, preset, sessionID string, extraEnv []string) (*dshSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &dshSession{
		cmd:             cmd,
		extraArgs:       extraArgs,
		workDir:         workDir,
		provider:        strings.TrimSpace(provider),
		model:           model,
		reasoningEffort: normalizeReasoningEffort(reasoningEffort),
		mode:            mode,
		preset:          strings.TrimSpace(preset),
		extraEnv:        extraEnv,
		events:          make(chan core.Event, 64),
		ctx:             ctx,
		cancel:          cancel,
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

// Send runs one headless dsh turn in --jsonl mode: spawns
// `dsh --profile headless --session-id <id> [--model <m>] [--mode <mode>] --jsonl <prompt>`,
// streams events into the events channel (text/thinking/tool calls), relays
// approval requests as permission cards, and finishes with an EventResult.
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

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("dsh: stdin pipe: %w", err)
	}
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

	s.runMu.Lock()
	s.stdin = stdinPipe
	s.pending = make(map[string]struct{})
	s.runMu.Unlock()

	// Reader goroutine: stream JSONL events until the process exits.
	finalText := new(string)
	resultSent := new(atomic.Bool)
	readerDone := make(chan struct{})
	go s.readJSONL(stdout, finalText, resultSent, readerDone)

	// Drain stdout to EOF BEFORE cmd.Wait(): Wait() closes the stdout pipe,
	// and a concurrent close would discard the lines still buffered in the
	// pipe (tool calls, the result/done envelopes). The reader reaches EOF
	// when the process exits and closes its write end.
	<-readerDone

	// The turn is over — close stdin BEFORE cmd.Wait() so the dsh runner's
	// approval readline sees EOF and the one-shot process can exit promptly
	// (it would otherwise wait for the driver to close stdin).
	s.runMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	s.pending = nil
	s.runMu.Unlock()

	err = cmd.Wait()

	stderrMsg := strings.TrimSpace(stderrBuf.String())
	if err != nil {
		// emit() drops events once s.ctx is done (Close/cancel), so a
		// cancelled run surfaces no spurious error turn to the engine.
		slog.Error("dshSession: process error", "cmd", s.cmd, "error", err, "stderr", truncStr(stderrMsg, 1000))
	}

	if stderrMsg != "" {
		// Surface actionable stderr (e.g. "dsh: MISSING_CREDENTIAL: ...").
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: %s", truncStr(stderrMsg, 2000))})
	}
	if err != nil && *finalText == "" {
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: %s", err)})
	}

	// Signal turn completion (EventResult with the full final text; the
	// engine prefers event.Content over the accumulated stream deltas).
	if !resultSent.Load() {
		s.emit(core.Event{Type: core.EventResult, Content: *finalText, SessionID: s.CurrentSessionID(), Done: true})
	}
	return nil
}

// readJSONL reads the dsh --jsonl event stream and maps it to core.Events.
// It emits a terminal EventResult (Done: true) when the `done` envelope
// arrives (marking resultSent), then closes readerDone.
func (s *dshSession) readJSONL(stdout io.ReadCloser, finalText *string, resultSent *atomic.Bool, done chan struct{}) {
	defer close(done)
	defer func() { _ = stdout.Close() }()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var thinkingBuf strings.Builder
	flushThinking := func() {
		if thinkingBuf.Len() == 0 {
			return
		}
		s.emit(core.Event{Type: core.EventThinking, Content: thinkingBuf.String()})
		thinkingBuf.Reset()
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			CallID    string          `json:"callId"`
			Content   string          `json:"content"`
			IsError   bool            `json:"isError"`
			ID        string          `json:"id"`
			ToolName  string          `json:"toolName"`
			Reason    string          `json:"reason"`
			Success   bool            `json:"success"`
			SessionID string          `json:"sessionId"`
			Extra     json.RawMessage `json:"-"`
		}
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			slog.Debug("dshSession: non-JSON line", "line", truncStr(line, 100))
			continue
		}

		switch evt.Type {
		case "text":
			if evt.Text == "" {
				continue
			}
			flushThinking()
			s.emit(core.Event{Type: core.EventText, Content: evt.Text})

		case "thinking":
			if evt.Text == "" {
				continue
			}
			thinkingBuf.WriteString(evt.Text)

		case "tool/call":
			flushThinking()
			s.emit(core.Event{Type: core.EventToolUse, ToolName: evt.Name, ToolInput: truncStr(evt.Arguments, 500)})

		case "tool/result":
			flushThinking()
			s.emit(core.Event{Type: core.EventToolResult, ToolName: evt.Name, Content: truncStr(evt.Content, 500)})

		case "approval/request":
			flushThinking()
			requestID := "dsh_" + evt.ID
			s.runMu.Lock()
			if s.pending != nil {
				s.pending[requestID] = struct{}{}
			}
			s.runMu.Unlock()
			reason := evt.Reason
			if reason == "" {
				reason = fmt.Sprintf("tool %q requires your approval", evt.ToolName)
			}
			s.emit(core.Event{
				Type:      core.EventPermissionRequest,
				RequestID: requestID,
				ToolName:  evt.ToolName,
				ToolInput: reason,
				ToolInputRaw: map[string]any{
					"toolName": evt.ToolName,
					"reason":   evt.Reason,
					"callId":   evt.CallID,
				},
			})

		case "result":
			*finalText = evt.Text

		case "done":
			flushThinking()
			resultSent.Store(true)
			s.emit(core.Event{Type: core.EventResult, Content: *finalText, SessionID: s.CurrentSessionID(), Done: true})
			return
		}
	}

	// Process exited without a done envelope (e.g. hard failure).
	flushThinking()
}

// ── RespondPermission ────────────────────────────────────────

// RespondPermission writes the human's decision back to the running dsh
// process's stdin (the headless runner's approval answerer waits for it).
func (s *dshSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.stdin == nil {
		return nil // no active run; nothing to answer
	}
	id := strings.TrimPrefix(requestID, "dsh_")
	if _, ok := s.pending[requestID]; !ok {
		slog.Warn("dshSession: RespondPermission for unknown request", "requestID", requestID)
		return nil
	}
	outcome := "rejected"
	if result.Behavior == "allow" {
		outcome = "allowed-once"
	}
	line, err := json.Marshal(map[string]string{
		"type":    "approval/response",
		"id":      id,
		"outcome": outcome,
	})
	if err != nil {
		return fmt.Errorf("dsh: marshal approval response: %w", err)
	}
	_, err = s.stdin.Write(append(line, '\n'))
	if err != nil {
		return fmt.Errorf("dsh: write approval response: %w", err)
	}
	return nil
}

// ── buildArgs / buildPrompt ─────────────────────────────────

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
	if s.provider != "" {
		args = append(args, "--provider", s.provider)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.reasoningEffort != "" {
		args = append(args, "--reasoning-effort", s.reasoningEffort)
	}
	if s.mode != "" {
		args = append(args, "--mode", s.mode)
	}
	s.runMu.Lock()
	preset := s.preset
	s.runMu.Unlock()
	if preset != "" {
		args = append(args, "--preset", preset)
	}
	args = append(args, "--jsonl")
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

// SetPreset stores the preset to apply before the next headless turn. The
// dsh runner performs the blank-session check and writes its durable selection
// event; this object only carries the command choice across process launches.
func (s *dshSession) SetPreset(preset string) error {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return fmt.Errorf("dsh: preset name must not be empty")
	}
	s.runMu.Lock()
	s.preset = preset
	s.runMu.Unlock()
	return nil
}

func (s *dshSession) GetPreset() string {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.preset
}

func (s *dshSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	s.runMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	s.pending = nil
	s.runMu.Unlock()
	s.sendWg.Wait()
	close(s.events)
	return nil
}

// ── Helpers ──────────────────────────────────────────────────

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
