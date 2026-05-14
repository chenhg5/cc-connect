package ymsagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// errWriteTimeout is returned by writeFrameWithTimeout when the underlying
// writeFrame did not complete within the allotted duration.
var errWriteTimeout = errors.New("yms-rca: writeFrame timeout")

// testEncoder is an injection point for unit tests so writeFrame can be
// exercised without spawning a subprocess.
type testEncoder interface {
	Encode(v any) error
}

// session manages a long-running `yms-rca rpc --no-color` subprocess.
type session struct {
	cmd      string
	workDir  string
	mode     string
	cfg      *Agent // snapshot used to build args / read confirm timeout
	extraEnv []string

	ctx    context.Context
	cancel context.CancelFunc

	proc    *exec.Cmd
	stdin   io.WriteCloser
	enc     *json.Encoder
	encMock testEncoder // non-nil only in unit tests; takes precedence over enc.
	writeMu sync.Mutex

	events chan core.Event
	wg     sync.WaitGroup

	alive atomic.Bool
	busy  atomic.Bool

	// turnResultEmitted dedups EventResult between agent_end / turn_end /
	// response/prompt ack-driven slash-command finalization. Reset by Send
	// for each new turn.
	turnResultEmitted atomic.Bool
	// promptAcked is set when pi-rpc emits `response command=prompt` for the
	// current prompt. For slash commands (no agent_end / turn_end), this is
	// paired with slashCommandEnded so either RPC event order can finalize
	// the turn after the terminal yms-command text has been emitted. Reset by
	// Send.
	promptAcked atomic.Bool
	// slashCommandEnded is set when the current turn receives the terminal
	// `message_end role=custom customType=yms-command`. yms-rca can emit this
	// before or after response/prompt, so slash-command finalization waits for
	// both latches.
	slashCommandEnded atomic.Bool

	sessionID    atomic.Value // string
	contextUsage atomic.Pointer[core.ContextUsage]
	seq          uint64
	statsSeq     uint64
	// currentPromptID is the id ("cc-N") of the most recent prompt frame
	// sent via Send. Used to detect stale response/prompt acks from a
	// prior turn — a late ack must not influence the current turn's state.
	currentPromptID atomic.Value // string

	// confirm bridge
	confirmMu      sync.Mutex
	pendingConfirm map[string]*pendingPermission

	// close coordination
	closeOnce sync.Once
	closeErr  error

	// stderr ring (best-effort, last 64KB)
	stderrMu  sync.Mutex
	stderrBuf []byte

	// de-dup of tool calls — toolCallId we've already emitted EventToolUse for
	toolMu       sync.Mutex
	seenToolUse  map[string]struct{}
	seenToolDone map[string]struct{}

	// streaming buffers
	thinkingBuf strings.Builder
}

type pendingPermission struct {
	id        string
	title     string
	createdAt time.Time
	timer     *time.Timer
	once      sync.Once
}

type confirmSnapshot struct {
	id    string
	title string
}

// newSession starts the yms-rca subprocess and returns a ready session.
func newSession(parent context.Context, snap *Agent, resumeFile string, extraEnv []string) (*session, error) {
	ctx, cancel := context.WithCancel(parent)

	args := buildArgs(snap, resumeFile)
	slog.Debug("yms-rca: launching", "cmd", snap.cmd, "args", core.RedactArgs(args))

	c := exec.CommandContext(ctx, snap.cmd, args...)
	c.Dir = snap.workDir

	extra := append([]string{}, extraEnv...)
	extra = append(extra, "NO_COLOR=1", "FORCE_COLOR=0")
	c.Env = core.MergeEnv(os.Environ(), extra)

	stdinPipe, err := c.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("yms-rca: stdin pipe: %w", err)
	}
	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("yms-rca: stdout pipe: %w", err)
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("yms-rca: stderr pipe: %w", err)
	}

	s := &session{
		cmd:            snap.cmd,
		workDir:        snap.workDir,
		mode:           snap.mode,
		cfg:            snap,
		extraEnv:       extraEnv,
		ctx:            ctx,
		cancel:         cancel,
		proc:           c,
		stdin:          stdinPipe,
		events:         make(chan core.Event, 64),
		pendingConfirm: make(map[string]*pendingPermission),
		seenToolUse:    make(map[string]struct{}),
		seenToolDone:   make(map[string]struct{}),
	}
	s.enc = json.NewEncoder(stdinPipe)
	s.enc.SetEscapeHTML(false)
	s.alive.Store(true)
	if resumeFile != "" {
		// The resume file path is informative only; the real sessionID comes
		// back via response command=get_state.
		s.sessionID.Store("")
	} else {
		s.sessionID.Store("")
	}

	if err := c.Start(); err != nil {
		cancel()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		stderr := s.stderrSnapshot()
		return nil, fmt.Errorf("yms-rca: start: %w; stderr=%s", err, stderr)
	}

	s.wg.Add(2)
	go s.readStdout(stdoutPipe)
	go s.readStderr(stderrPipe)

	// Bootstrap: ask for state and stats so /status has data immediately.
	go func() {
		if err := s.writeFrame(map[string]any{"type": "get_state", "id": "cc-init"}); err != nil {
			slog.Warn("yms-rca: get_state bootstrap failed", "err", err)
		}
		s.requestSessionStats()
	}()

	return s, nil
}

// ── core.AgentSession ──────────────────────────────────────

func (s *session) Events() <-chan core.Event { return s.events }

func (s *session) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *session) Alive() bool { return s.alive.Load() }

// Send delivers a prompt + attachments to the running yms-rca process.
func (s *session) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return errors.New("yms-rca: session closed")
	}
	if !s.busy.CompareAndSwap(false, true) {
		return errors.New("yms-rca: previous turn still running")
	}
	// New turn — reset the result-emit dedup latch so agent_end / turn_end
	// for this turn can fire EventResult exactly once. Also reset the
	// promptAcked flag so the slash-command terminator path is armed fresh.
	s.turnResultEmitted.Store(false)
	s.promptAcked.Store(false)
	s.slashCommandEnded.Store(false)

	// On any error after CAS we must release busy.
	released := false
	defer func() {
		if !released {
			s.busy.Store(false)
		}
	}()

	sid := s.CurrentSessionID()
	if sid == "" {
		sid = "preinit"
	}

	// Save plain files (UUID-prefixed, per-session) and append paths to prompt.
	var savedPaths []string
	if len(files) > 0 {
		paths, err := s.saveSessionFiles(sid, files)
		if err != nil {
			return fmt.Errorf("yms-rca: save attachments: %w", err)
		}
		savedPaths = paths
	}
	if len(savedPaths) > 0 {
		prompt = core.AppendFileRefs(prompt, savedPaths)
	}

	// Images go into the prompt frame as base64.
	var imageArr []map[string]any
	for _, img := range images {
		imageArr = append(imageArr, map[string]any{
			"type":     "image",
			"data":     base64Encode(img.Data),
			"mimeType": img.MimeType,
		})
	}

	id := fmt.Sprintf("cc-%d", atomic.AddUint64(&s.seq, 1))
	// Record the current prompt id so handleResponse can reject stale
	// `response command=prompt` frames carrying an older id (which would
	// otherwise mutate this turn's promptAcked / busy state).
	s.currentPromptID.Store(id)
	frame := map[string]any{
		"type":    "prompt",
		"id":      id,
		"message": prompt,
	}
	if imageArr != nil {
		frame["images"] = imageArr
	}

	if err := s.writeFrame(frame); err != nil {
		return fmt.Errorf("yms-rca: write prompt: %w", err)
	}
	// busy stays true; cleared by handleAgentEnd / prompt-failure handler /
	// readLoop exit / Close.
	released = true
	return nil
}

// RespondPermission delivers a user decision for a confirm request.
func (s *session) RespondPermission(requestID string, result core.PermissionResult) error {
	confirmed := strings.EqualFold(result.Behavior, "allow")
	ok := s.resolvePendingConfirm(requestID, confirmed, "", s.emit)
	if !ok {
		return fmt.Errorf("yms-rca: unknown permission request id %q", requestID)
	}
	return nil
}

// SetLiveMode applies a permission-mode switch to the running session.
// Returning true tells the engine the change was applied without restart.
func (s *session) SetLiveMode(mode string) bool {
	normalized := normalizeMode(mode)
	s.confirmMu.Lock()
	s.mode = normalized
	s.confirmMu.Unlock()

	// Auto-resolve pending confirms for non-default modes.
	switch normalized {
	case "yolo", "bypassPermissions":
		s.batchResolvePending(true, func(title string) string {
			return fmt.Sprintf("yms-rca: mode switched to %s, auto-approved: %s", normalized, title)
		})
	case "dontAsk":
		s.batchResolvePending(false, func(title string) string {
			return fmt.Sprintf("yms-rca: mode switched to %s, auto-declined: %s", normalized, title)
		})
	}
	return true
}

func (s *session) batchResolvePending(confirmed bool, reasonFn func(string) string) {
	// Copy id+title snapshot under lock; resolve outside lock to avoid
	// nested locking with the helper.
	s.confirmMu.Lock()
	snaps := make([]confirmSnapshot, 0, len(s.pendingConfirm))
	for _, p := range s.pendingConfirm {
		snaps = append(snaps, confirmSnapshot{id: p.id, title: p.title})
	}
	s.confirmMu.Unlock()
	for _, sn := range snaps {
		s.resolvePendingConfirm(sn.id, confirmed, reasonFn(sn.title), s.emit)
	}
}

// ── confirm helper ─────────────────────────────────────────

// resolvePendingConfirm is the regular-path entrypoint for writing an
// extension_ui_response back to yms-rca. Close() does NOT use this helper —
// see Close() below for the fail-closed pre-emption sequence.
//
// emit is injected by the caller; pass s.emit on normal paths so events are
// delivered to the IM consumer. Returns true if this call won the
// sync.Once race; false means another path resolved this id already.
func (s *session) resolvePendingConfirm(id string, confirmed bool, reason string, emit func(core.Event)) bool {
	s.confirmMu.Lock()
	p, ok := s.pendingConfirm[id]
	if !ok {
		s.confirmMu.Unlock()
		return false
	}
	resolved := false
	p.once.Do(func() {
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(s.pendingConfirm, id)
		resolved = true
	})
	s.confirmMu.Unlock()
	if !resolved {
		return false
	}
	if err := s.writeFrame(map[string]any{
		"type":      "extension_ui_response",
		"id":        id,
		"confirmed": confirmed,
	}); err != nil {
		slog.Warn("yms-rca: write confirm response failed", "id", id, "err", err)
	}
	if reason != "" && emit != nil {
		emit(core.Event{Type: core.EventThinking, Content: reason})
	}
	return true
}

// registerPending is called from §D2 to register a pending confirm BEFORE
// emitting EventPermissionRequest. Caller emits afterwards.
func (s *session) registerPending(id, title string) {
	p := &pendingPermission{
		id:        id,
		title:     title,
		createdAt: time.Now(),
	}
	timeout := s.cfg.confirmTimeout
	if timeout > 0 {
		p.timer = time.AfterFunc(timeout, func() {
			s.resolvePendingConfirm(id, false,
				"yms-rca: auto-declined after timeout: "+title, s.emit)
		})
	}
	s.confirmMu.Lock()
	s.pendingConfirm[id] = p
	s.confirmMu.Unlock()
}

// writeClaimedConfirmDeny writes confirmed:false for a pending whose
// sync.Once has already been claimed in Close() (see §G1 step 4).
// It must NOT consult pendingConfirm, must NOT emit events, and must NOT
// touch the once — those have all been handled in the locked claim step.
func (s *session) writeClaimedConfirmDeny(id string) {
	if err := s.writeFrame(map[string]any{
		"type":      "extension_ui_response",
		"id":        id,
		"confirmed": false,
	}); err != nil {
		slog.Warn("yms-rca: write close-time deny failed", "id", id, "err", err)
	}
}

// ── Close ──────────────────────────────────────────────────

func (s *session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.doClose() })
	return s.closeErr
}

func (s *session) doClose() error {
	// 1. Refuse further Send.
	s.alive.Store(false)

	// 2. Claim every pending under the lock so we win the once race against
	//    in-flight RespondPermission / timeout / SetLiveMode.
	s.confirmMu.Lock()
	var claimed []confirmSnapshot
	for _, p := range s.pendingConfirm {
		won := false
		p.once.Do(func() {
			if p.timer != nil {
				p.timer.Stop()
			}
			won = true
		})
		if won {
			claimed = append(claimed, confirmSnapshot{id: p.id, title: p.title})
		}
	}
	// Clear the map — anything we didn't claim has been resolved by another
	// path which already delete'd itself, but it's safe to wipe what's left.
	for id := range s.pendingConfirm {
		delete(s.pendingConfirm, id)
	}
	s.confirmMu.Unlock()

	// 3. Synchronously emit reason events for the pendings we claimed.
	//    Uses tryEmit (non-blocking) so we never wedge on a stalled IM consumer.
	//    This runs BEFORE close(s.events), so tryEmit is safe.
	for _, sn := range claimed {
		s.tryEmit(core.Event{
			Type:    core.EventThinking,
			Content: "yms-rca: session closed, pending confirm auto-denied: " + sn.title,
		})
	}

	// 4. Spawn cleanup goroutine to write deny frames; bounded by timeout.
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		for _, sn := range claimed {
			s.writeClaimedConfirmDeny(sn.id)
		}
	}()

	cleanupTimedOut := false
	if len(claimed) > 0 {
		select {
		case <-cleanupDone:
		case <-time.After(2 * time.Second):
			cleanupTimedOut = true
			slog.Warn("yms-rca: pending confirm cleanup timed out")
		}
	} else {
		// nothing to do — still consume the channel.
		<-cleanupDone
	}

	// 5. Best-effort abort (only if cleanup didn't time out, since both share writeMu).
	if !cleanupTimedOut {
		if err := s.writeFrameWithTimeout(map[string]any{"type": "abort"}, 1*time.Second); err != nil {
			slog.Debug("yms-rca: abort write failed", "err", err)
		}
	}

	// 6. Cancel ctx — closes stdin, unblocks any stuck writes, terminates child.
	s.cancel()
	if s.stdin != nil {
		_ = s.stdin.Close()
	}

	// 7. Wait for read loops with a timeout. Kill the process if needed.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("yms-rca: read loops did not exit, killing process")
		if s.proc != nil && s.proc.Process != nil {
			_ = s.proc.Process.Kill()
		}
		<-done
	}

	// 8. Clear busy.
	s.busy.Store(false)

	// 9. Close events. Cleanup goroutine is already done (it only writes
	//    stdin, never touches events), so this is safe.
	close(s.events)
	return nil
}

// ── write helpers ──────────────────────────────────────────

// writeFrame serialises a single JSON-Line frame to stdin.
func (s *session) writeFrame(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.encMock != nil {
		return s.encMock.Encode(v)
	}
	if s.enc == nil {
		return errors.New("yms-rca: encoder not initialised")
	}
	return s.enc.Encode(v)
}

// writeFrameWithTimeout caps writeFrame at d so Close can never wedge.
func (s *session) writeFrameWithTimeout(v any, d time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- s.writeFrame(v) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return errWriteTimeout
	}
}

// emit blocks on ctx.Done — use during the normal session lifetime.
func (s *session) emit(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// tryEmit is used inside Close BEFORE close(s.events) — never after.
// A pure non-blocking select is enough because close(events) only happens
// in step 9 of doClose, strictly after the only call site (step 3).
func (s *session) tryEmit(evt core.Event) {
	select {
	case s.events <- evt:
	default:
		slog.Warn("yms-rca: drop event during close", "type", evt.Type)
	}
}

// ── stdout / stderr loops ──────────────────────────────────

func (s *session) readStdout(r io.ReadCloser) {
	defer s.wg.Done()
	defer r.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Warn("yms-rca: non-JSON line", "line", truncStr(line, 200))
			s.emit(core.Event{Type: core.EventError,
				Error: fmt.Errorf("yms-rca: non-JSON stdout: %s", truncStr(line, 200))})
			continue
		}
		s.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("yms-rca: scanner error", "error", err)
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("yms-rca: read stdout: %w", err)})
	}

	// Process exited — wait for it (mirrors pi adapter pattern).
	if err := s.proc.Wait(); err != nil {
		stderr := s.stderrSnapshot()
		// Avoid noisy errors when we deliberately cancel during Close.
		if s.alive.Load() {
			slog.Error("yms-rca: process exited", "err", err, "stderr", truncStr(stderr, 200))
			s.emit(core.Event{Type: core.EventError,
				Error: fmt.Errorf("yms-rca: process exited: %v: %s", err, truncStr(stderr, 200))})
		}
	}

	// Emit a final EventResult so the engine can finalise the turn.
	// Use tryEmit: when Close() has cancelled ctx, s.emit would bail on
	// ctx.Done() and drop this lifecycle signal — but the events channel
	// is still open here (Close closes it in step 9 only AFTER wg.Wait,
	// which awaits this goroutine). The non-blocking send always succeeds
	// because the channel has buffer 64 and we drop instead of blocking.
	sid := s.CurrentSessionID()
	s.tryEmit(core.Event{Type: core.EventResult, SessionID: sid, Done: true})
	s.busy.Store(false)
}

func (s *session) readStderr(r io.ReadCloser) {
	defer s.wg.Done()
	defer r.Close()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		slog.Debug("yms-rca: stderr", "line", line)
		s.appendStderr(line)
	}
}

func (s *session) appendStderr(line string) {
	const maxBytes = 64 * 1024
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	s.stderrBuf = append(s.stderrBuf, line...)
	s.stderrBuf = append(s.stderrBuf, '\n')
	if len(s.stderrBuf) > maxBytes {
		s.stderrBuf = truncBytes(s.stderrBuf, maxBytes)
	}
}

func (s *session) stderrSnapshot() string {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	return strings.TrimSpace(string(s.stderrBuf))
}

// ── stats refresh ──────────────────────────────────────────

func (s *session) requestSessionStats() {
	id := fmt.Sprintf("cc-stats-%d", atomic.AddUint64(&s.statsSeq, 1))
	if err := s.writeFrame(map[string]any{"type": "get_session_stats", "id": id}); err != nil {
		slog.Debug("yms-rca: get_session_stats failed", "err", err)
	}
}

// GetContextUsage implements core.ContextUsageReporter.
func (s *session) GetContextUsage() *core.ContextUsage {
	return s.contextUsage.Load()
}

// ── attachments ────────────────────────────────────────────

// saveSessionFiles writes files to <workDir>/.cc-connect/attachments/<sessionID>/
// using a UUID prefix to prevent collisions. Returns absolute paths.
func (s *session) saveSessionFiles(sessionID string, files []core.FileAttachment) ([]string, error) {
	dir := filepath.Join(s.workDir, ".cc-connect", "attachments", sanitizePathComponent(sessionID))
	// Wipe previous turn's files.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range files {
		uuid, err := randomHex(8)
		if err != nil {
			return nil, err
		}
		safeName := sanitizeFileName(f.FileName)
		if safeName == "" {
			safeName = "attachment"
		}
		fname := uuid + "-" + safeName
		fpath := filepath.Join(dir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, fpath)
	}
	return paths, nil
}

func sanitizePathComponent(s string) string {
	// Allow alnum, dash, underscore, dot; replace anything else with '_'.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "session"
	}
	return out
}

func sanitizeFileName(name string) string {
	name = filepath.Base(name) // drop directory components
	if name == "." || name == "/" || name == `\` {
		return ""
	}
	// Replace path separators just in case.
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	return name
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func base64Encode(b []byte) string {
	return stdBase64Encode(b)
}

// ── frame builder ──────────────────────────────────────────

// buildArgs returns the CLI arguments for `yms-rca rpc`.
// resumeFile is the absolute path of a session file to resume, or "" for
// a new session.
func buildArgs(a *Agent, resumeFile string) []string {
	args := []string{"rpc", "--no-color"}
	if a.provider != "" {
		args = append(args, "--provider", a.provider, "--model", a.model)
	} else if a.model != "" {
		args = append(args, "--model", a.model)
	}
	if a.thinking != "" {
		args = append(args, "--thinking", a.thinking)
	}
	if a.sessionDir != "" {
		args = append(args, "--session-dir", a.sessionDir)
	}
	if resumeFile != "" {
		args = append(args, "--session-file", resumeFile)
	}
	if a.offline {
		args = append(args, "--offline")
	}
	return args
}

// stdBase64Encode is split out to keep import list tidy in this file.
// (Implemented in rpc.go to share with image / file plumbing if needed.)
