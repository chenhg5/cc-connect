package comate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.AgentSession = (*comateSession)(nil)

// comateSession manages a multi-turn Comate conversation.
// Each Send() spawns `comatecli run [-c <convID>] -q <prompt> --display event-stream`.
// With auto-approval disabled, a tool call suspends the turn: comatecli emits a
// pending-approvals event and exits without executing; RespondPermission then
// resumes the turn with `comatecli run -c <convID> --approve|--reject`, whose
// events flow into the same channel until the turn completes.
type comateSession struct {
	cmd                    string
	cliExtraArgs           []string // extra args from cmd, prepended before run args
	workDir                string
	model                  string
	mode                   string
	autoApprove            bool
	disableHooks           bool
	enableInteractiveTools bool
	extraEnv               []string

	events    chan core.Event
	sessionID atomic.Value // stores string — comatecli conversation UUID
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool
	closeOnce sync.Once

	turnInFlight  atomic.Bool
	turnMu        sync.Mutex
	turnDone      chan struct{}
	turnCompleted bool // terminal event received; process may still be exiting

	textMu   sync.Mutex
	lastText string // accumulated assistant text base for delta emission

	pendMu  sync.Mutex
	pending map[string]comatePending // toolId → approval

	cmdMu sync.Mutex
	cmds  map[*exec.Cmd]struct{}
}

var comateSessionCloseTimeout = 8 * time.Second
var comateSessionForceKillWait = 2 * time.Second

type comatePending struct {
	ToolName string
	Input    string         // human-readable summary for the approval card
	Raw      map[string]any // params, echoed back as Event.ToolInputRaw
}

// newComateSession wires up a session object. No process is spawned here —
// each Send/RespondPermission turn spawns its own. resumeID restores a prior
// conversation; the core.ContinueSession sentinel starts fresh because the
// comatecli --continue flag cannot handle pending interactions.
func newComateSession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode string, autoApprove, disableHooks, enableInteractiveTools bool, resumeID string, extraEnv []string) *comateSession {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &comateSession{
		cmd:                    cmd,
		cliExtraArgs:           extraArgs,
		workDir:                workDir,
		model:                  model,
		mode:                   mode,
		autoApprove:            autoApprove,
		disableHooks:           disableHooks,
		enableInteractiveTools: enableInteractiveTools,
		extraEnv:               extraEnv,
		events:                 make(chan core.Event, 64),
		ctx:                    sessionCtx,
		cancel:                 cancel,
		pending:                make(map[string]comatePending),
		cmds:                   make(map[*exec.Cmd]struct{}),
	}
	cs.alive.Store(true)

	// core.ContinueSession ("__continue__") means "resume most recent"; the
	// comatecli --continue flag does not handle pending interactions, so treat
	// the sentinel as a fresh conversation (codex precedent).
	if resumeID != "" && resumeID != core.ContinueSession {
		cs.sessionID.Store(resumeID)
	}

	return cs
}

// Send submits one user turn. It clears stale approvals, saves attachments
// into workDir and spawns a fresh `comatecli run -q <prompt>` process whose
// event stream is parsed by readLoop. Overlapping turns are rejected (or
// queued behind a completed-but-still-exiting process) by beginTurn.
func (cs *comateSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if err := cs.beginTurn(); err != nil {
		return err
	}
	// The deferred cleanup reads started when Send returns, so setup failures
	// release the guard while a launched process keeps it until readLoop exits.
	started := false
	defer func() {
		if !started {
			cs.endTurn()
			cs.wg.Done()
		}
	}()

	if len(images) > 0 {
		slog.Warn("comateSession: images not supported, ignoring")
	}
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(cs.workDir, messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	// A fresh query turn abandons any stale approvals from earlier turns; the
	// engine only reaches Send after the previous turn fully resolved.
	cs.pendMu.Lock()
	cs.pending = make(map[string]comatePending)
	cs.pendMu.Unlock()

	if err := cs.spawnTurn(cs.buildRunArgs(prompt)); err != nil {
		return err
	}
	started = true
	return nil
}

// RespondPermission resumes a suspended turn by spawning
// `comatecli run -c <convID> --approve|--reject`. The resumed process streams
// its events into the same channel; its turn-completed event ends the turn.
func (cs *comateSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	cs.pendMu.Lock()
	p, ok := cs.pending[requestID]
	if ok {
		delete(cs.pending, requestID)
	}
	cs.pendMu.Unlock()
	if !ok {
		return fmt.Errorf("comate: unknown or already-resolved permission request %s", requestID)
	}

	decision := "--reject"
	if result.Behavior == "allow" {
		decision = "--approve"
	}

	if cs.CurrentSessionID() == "" {
		return fmt.Errorf("comate: no active conversation to resume approval %s", requestID)
	}

	if err := cs.beginTurn(); err != nil {
		cs.pendMu.Lock()
		cs.pending[requestID] = p
		cs.pendMu.Unlock()
		return err
	}
	started := false
	defer func() {
		if !started {
			cs.endTurn()
			cs.wg.Done()
			// Restore the entry so a later retry can still resolve it.
			cs.pendMu.Lock()
			cs.pending[requestID] = p
			cs.pendMu.Unlock()
		}
	}()

	if err := cs.spawnTurn(cs.buildResumeArgs(decision)); err != nil {
		return err
	}
	started = true
	return nil
}

// buildRunArgs builds the argv for a query turn:
//
//	run --display event-stream [-c <convID>] [--cwd <dir>] [--model <m>]
//	     [--mode <mode>] [--disable-auto-approve] [--disable-hooks]
//	     [--enable-interactive-tools] -q <query>
func (cs *comateSession) buildRunArgs(query string) []string {
	args := []string{"run", "--display", "event-stream"}
	if sid := cs.CurrentSessionID(); sid != "" {
		args = append(args, "-c", sid)
	}
	if cs.workDir != "" && cs.workDir != "." {
		args = append(args, "--cwd", cs.workDir)
	}
	if cs.model != "" {
		args = append(args, "--model", cs.model)
	}
	if cs.mode != "" {
		args = append(args, "--mode", cs.mode)
	}
	if !cs.autoApprove {
		args = append(args, "--disable-auto-approve")
	}
	if cs.disableHooks {
		args = append(args, "--disable-hooks")
	}
	if cs.enableInteractiveTools {
		args = append(args, "--enable-interactive-tools")
	}
	args = append(args, "-q", query)
	return args
}

// buildResumeArgs builds the argv for an approval resume turn:
//
//	run --display event-stream -c <convID> --approve|--reject [--cwd <dir>]
func (cs *comateSession) buildResumeArgs(decision string) []string {
	args := []string{"run", "--display", "event-stream", "-c", cs.CurrentSessionID(), decision}
	if cs.workDir != "" && cs.workDir != "." {
		args = append(args, "--cwd", cs.workDir)
	}
	return args
}

// spawnTurn launches one comatecli process for the given argv and hands its
// stdout to a readLoop goroutine tracked by cs.wg. It resets the delta text
// base (each process streams its own reply from scratch) and prepends any
// extra CLI args configured via `cmd`.
func (cs *comateSession) spawnTurn(args []string) error {
	// The delta base is per-turn: each process streams its own reply.
	cs.textMu.Lock()
	cs.lastText = ""
	cs.textMu.Unlock()

	if len(cs.cliExtraArgs) > 0 {
		args = append(append([]string{}, cs.cliExtraArgs...), args...)
	}

	slog.Debug("comateSession: launching", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(cs.ctx, cs.cmd, args...)
	cmd.Dir = cs.workDir
	prepareCmdForKill(cmd)
	if len(cs.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), cs.extraEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("comateSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("comateSession: start: %w", err)
	}
	cs.addCmd(cmd)

	go cs.readLoop(cmd, stdout, &stderrBuf)
	return nil
}

// readLoop parses one process's event-stream stdout into core events until
// EOF. Event mapping:
//
//	conversation-status Completed (no approvals) → EventResult{Done:true}
//	conversation-status Completed (with approvals) → suspended, nothing terminal
//	pending-approvals (non-empty) → EventPermissionRequest per tool call
//	partial-message-data → delta-converted EventText chunks
//
// On abnormal process exit (no completion event, not a deliberate Close) it
// emits EventError so the engine never hangs.
func (cs *comateSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer cs.wg.Done()

	var gotResult bool
	var suspended bool

	defer func() {
		defer cs.endTurn()
		defer cs.removeCmd(cmd)
		exitErr := cmd.Wait()
		if gotResult || suspended {
			// Normal completion, or a turn parked on pending approvals: the
			// engine is waiting on the permission resolution and the turn
			// continues via RespondPermission — emit nothing terminal.
			if exitErr != nil {
				slog.Warn("comateSession: process exited with error after completion",
					"error", exitErr, "stderr", truncStr(strings.TrimSpace(stderrBuf.String()), 200))
			}
			return
		}
		// Abnormal end — tell the engine so it never hangs.
		if cs.ctx.Err() != nil {
			// Deliberate Close(): the kill-induced exit is expected
			// teardown noise, not an engine-visible failure.
			return
		}
		msg := "comatecli exited without a completion event"
		if exitErr != nil {
			msg = fmt.Sprintf("comatecli process failed: %v", exitErr)
		}
		if stderrMsg := strings.TrimSpace(stderrBuf.String()); stderrMsg != "" {
			msg += ": " + truncStr(stderrMsg, 300)
		}
		slog.Error("comateSession: turn ended without completion", "detail", msg)
		evt := core.Event{Type: core.EventError, Error: errors.New(msg)}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
		}
	}()

	if err := readJSONLines(stdout, func(line []byte) error {
		if len(line) == 0 {
			return nil
		}
		slog.Debug("comateSession: raw", "line", truncStr(string(line), 500))

		var ev comateEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Debug("comateSession: non-JSON line", "line", truncStr(string(line), 100))
			return nil
		}

		// Announce the CLI-assigned conversation ID immediately so the engine
		// persists the session mapping (codex thread.started pattern).
		if ev.ConversationInfo != nil && ev.ConversationInfo.ID != "" && ev.ConversationInfo.ID != cs.CurrentSessionID() {
			cs.sessionID.Store(ev.ConversationInfo.ID)
			evt := core.Event{Type: core.EventText, SessionID: ev.ConversationInfo.ID}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
			}
		}

		switch ev.Type {
		case "conversation-status":
			if ev.ConversationInfo == nil || ev.ConversationInfo.Status != "Completed" {
				break
			}
			if suspended || cs.hasPending() {
				suspended = true
				// The process reached a terminal state (it exits without
				// executing); mark it so beginTurn waits for its exit before
				// allowing a RespondPermission resume instead of rejecting.
				cs.markTurnCompleted()
				slog.Info("comateSession: turn suspended, waiting for approval",
					"conversation_id", cs.CurrentSessionID(), "pending", cs.pendingCount())
				break
			}
			gotResult = true
			cs.markTurnCompleted()
			evt := core.Event{Type: core.EventResult, SessionID: cs.CurrentSessionID(), Done: true}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
			}

		case "partial-message-data":
			if gotResult {
				break // post-completion snapshots repeat the final text
			}
			if text := extractAssistantText(ev.Messages); text != "" {
				cs.emitTextDelta(text)
			}

		case "pending-approvals":
			if len(ev.PendingApprovals) > 0 {
				// The turn parks here: comatecli streams approvals before the
				// terminal Completed status (and may exit before the latter
				// arrives), and the engine may respond as soon as the request
				// is visible — release the beginTurn gate up front and pin the
				// turn as suspended now: RespondPermission may empty cs.pending
				// before Completed is parsed, and hasPending() alone would then
				// let the suspended turn emit a premature EventResult.
				suspended = true
				cs.markTurnCompleted()
				slog.Info("comateSession: turn suspended, waiting for approval",
					"conversation_id", cs.CurrentSessionID(), "pending", len(ev.PendingApprovals))
			}
			for _, ap := range ev.PendingApprovals {
				if ap.ToolID == "" {
					continue
				}
				cs.pendMu.Lock()
				cs.pending[ap.ToolID] = comatePending{ToolName: ap.ToolName, Input: summarizeApproval(ap), Raw: ap.Params}
				cs.pendMu.Unlock()
				evt := core.Event{
					Type:         core.EventPermissionRequest,
					RequestID:    ap.ToolID,
					ToolName:     ap.ToolName,
					ToolInput:    summarizeApproval(ap),
					ToolInputRaw: ap.Params,
				}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
				}
			}
		}
		return nil
	}); err != nil {
		slog.Error("comateSession: read stdout error", "error", err)
	}
}

// ── comatecli event-stream structures ─────────────────────────
// Only the fields cc-connect consumes are declared; unknown JSON keys are
// ignored so minor CLI version bumps stay compatible.

// comateEvent is one line of `--display event-stream` output.
type comateEvent struct {
	Type             string                  `json:"type"`
	ConversationInfo *comateConversationInfo `json:"conversationInfo"`
	Messages         []comateMessage         `json:"messages"`
	PendingApprovals []comateApproval        `json:"pendingApprovals"`
}

// comateConversationInfo carries the conversation UUID (session ID) and the
// lifecycle status ("Running" / "Completed").
type comateConversationInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// comateMessage is one message inside conversation snapshots.
type comateMessage struct {
	Role     string          `json:"role"`
	Status   string          `json:"status"`
	Elements []comateElement `json:"elements"`
}

// comateElement groups the typed children of a message.
type comateElement struct {
	Children []comateChild `json:"children"`
}

// comateChild is a leaf content node; only TEXT children carry assistant
// reply text.
type comateChild struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	End     bool   `json:"end"`
}

// comateApproval describes one tool call waiting for user approval.
type comateApproval struct {
	ToolID         string         `json:"toolId"`
	ConversationID string         `json:"conversationId"`
	ToolName       string         `json:"toolName"`
	Command        string         `json:"command"`
	Params         map[string]any `json:"params"`
	ApprovalKind   string         `json:"approvalKind"`
}

// ── event handling helpers ────────────────────────────────────

// extractAssistantText concatenates all TEXT children across assistant
// messages, yielding the accumulated reply text so far.
func extractAssistantText(messages []comateMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, el := range m.Elements {
			for _, ch := range el.Children {
				if ch.Type == "TEXT" && ch.Content != "" {
					sb.WriteString(ch.Content)
				}
			}
		}
	}
	return sb.String()
}

// emitTextDelta converts an accumulated snapshot into a delta chunk.
// comatecli re-sends the full assistant text on every update while the engine
// appends every EventText (core/engine.go), so only the newly added suffix
// must be emitted (qoder precedent).
func (cs *comateSession) emitTextDelta(full string) {
	cs.textMu.Lock()
	prev := cs.lastText
	if full == prev {
		cs.textMu.Unlock()
		return
	}
	var chunk string
	switch {
	case prev == "":
		chunk = full
	case strings.HasPrefix(full, prev):
		chunk = strings.TrimPrefix(full, prev)
	default:
		// Snapshot reset or an unrelated message: re-anchor with a separator
		// so the engine renders it as a fresh block.
		chunk = "\n\n" + full
	}
	cs.lastText = full
	cs.textMu.Unlock()

	evt := core.Event{Type: core.EventText, Content: chunk}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

// summarizeApproval renders a human-readable summary for the approval card.
func summarizeApproval(ap comateApproval) string {
	if ap.Command != "" {
		return ap.Command
	}
	if len(ap.Params) > 0 {
		if b, err := json.Marshal(ap.Params); err == nil {
			return truncStr(string(b), 200)
		}
	}
	return ap.ToolName
}

// hasPending reports whether any tool approval is unresolved.
func (cs *comateSession) hasPending() bool {
	cs.pendMu.Lock()
	defer cs.pendMu.Unlock()
	return len(cs.pending) > 0
}

// pendingCount returns the number of unresolved approvals (for logging).
func (cs *comateSession) pendingCount() int {
	cs.pendMu.Lock()
	defer cs.pendMu.Unlock()
	return len(cs.pending)
}

// ── turn guard (codex pattern) ────────────────────────────────

// beginTurn rejects overlapping prompts while the model is working, but lets
// a follow-up wait for a completed process to release the guard. The engine
// can receive EventResult before that process has actually exited.
func (cs *comateSession) beginTurn() error {
	for {
		cs.turnMu.Lock()
		if err := cs.ctx.Err(); err != nil {
			cs.turnMu.Unlock()
			return err
		}
		if !cs.turnInFlight.Load() {
			// Reserve the process before releasing turnMu so Close cannot
			// finish waiting (and close events) while this turn is being set up.
			cs.wg.Add(1)
			cs.turnInFlight.Store(true)
			cs.turnDone = make(chan struct{})
			cs.turnCompleted = false
			cs.turnMu.Unlock()
			return nil
		}
		if !cs.turnCompleted {
			cs.turnMu.Unlock()
			return fmt.Errorf("comate session turn already in progress")
		}
		done := cs.turnDone
		cs.turnMu.Unlock()
		select {
		case <-done:
		case <-cs.ctx.Done():
			return cs.ctx.Err()
		}
	}
}

func (cs *comateSession) endTurn() {
	cs.turnMu.Lock()
	defer cs.turnMu.Unlock()
	cs.turnInFlight.Store(false)
	close(cs.turnDone)
}

func (cs *comateSession) markTurnCompleted() {
	cs.turnMu.Lock()
	cs.turnCompleted = true
	cs.turnMu.Unlock()
}

// ── core.AgentSession plumbing ────────────────────────────────

// Events returns the engine-facing event stream; closed once by Close.
func (cs *comateSession) Events() <-chan core.Event {
	return cs.events
}

// CurrentSessionID returns the CLI conversation UUID, empty until the CLI
// assigns one (first event of the first turn).
func (cs *comateSession) CurrentSessionID() string {
	v, _ := cs.sessionID.Load().(string)
	return v
}

// Alive reports whether the session accepts new turns.
func (cs *comateSession) Alive() bool {
	return cs.alive.Load()
}

// Close cancels the session context (which kills active processes), waits
// up to comateSessionCloseTimeout for readLoops to drain, force-kills
// surviving process groups, then closes the events channel exactly once.
// It never blocks longer than close timeout + force-kill grace.
func (cs *comateSession) Close() error {
	cs.turnMu.Lock()
	cs.alive.Store(false)
	cs.cancel()
	cs.turnMu.Unlock()
	done := make(chan struct{})
	go func() {
		cs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// readLoop has exited; safe to close the events channel.
		cs.closeOnce.Do(func() {
			close(cs.events)
		})
		return nil
	case <-time.After(comateSessionCloseTimeout):
		cmds := cs.activeCmds()
		slog.Warn("comateSession: graceful close timed out, killing active process groups",
			"wait", comateSessionCloseTimeout,
			"count", len(cmds))
		if err := forceKillAllCmds(cmds); err != nil {
			slog.Debug("comateSession: force kill failed", "error", err)
		}
		select {
		case <-done:
			cs.closeOnce.Do(func() {
				close(cs.events)
			})
			return nil
		case <-time.After(comateSessionForceKillWait):
			// Do not close(cs.events) here: readLoop may still be in
			// handleEvent and would panic on send.
			slog.Warn("comateSession: force kill wait timed out, deferring events channel close until readLoop exits",
				"wait", comateSessionForceKillWait)
			go func() {
				<-done
				cs.closeOnce.Do(func() {
					close(cs.events)
				})
			}()
			return nil
		}
	}
}

// addCmd / removeCmd / activeCmds track live turn processes so Close can
// force-kill stragglers that ignore context cancellation.

func (cs *comateSession) addCmd(cmd *exec.Cmd) {
	cs.cmdMu.Lock()
	defer cs.cmdMu.Unlock()
	cs.cmds[cmd] = struct{}{}
}

func (cs *comateSession) removeCmd(cmd *exec.Cmd) {
	cs.cmdMu.Lock()
	defer cs.cmdMu.Unlock()
	delete(cs.cmds, cmd)
}

func (cs *comateSession) activeCmds() []*exec.Cmd {
	cs.cmdMu.Lock()
	defer cs.cmdMu.Unlock()
	cmds := make([]*exec.Cmd, 0, len(cs.cmds))
	for cmd := range cs.cmds {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// forceKillAllCmds force-kills every tracked process, joining the errors.
func forceKillAllCmds(cmds []*exec.Cmd) error {
	var errs []error
	for _, cmd := range cmds {
		if err := forceKillCmd(cmd); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ── helpers ───────────────────────────────────────────────────

// readJSONLines reads newline-delimited JSON, tolerating arbitrarily long
// lines (comatecli conversation snapshots can be large).
func readJSONLines(r io.Reader, handle func([]byte) error) error {
	reader := bufio.NewReader(r)

	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			if err := handle(line); err != nil {
				return err
			}
		}

		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// truncStr shortens s to maxRunes runes, appending "..." when truncated.
func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
