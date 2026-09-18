package opencode

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

// opencodeSession manages multi-turn conversations with the OpenCode CLI.
// Each Send() launches a new `opencode run --format json` process
// with --session for conversation continuity.
type opencodeSession struct {
	cmd               string
	extraArgs         []string // extra args from cmd, prepended before opencode args
	workDir           string
	model             string
	mode              string
	agentName         string
	extraEnv          []string
	events            chan core.Event
	chatID            atomic.Value // stores string — OpenCode session ID
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	alive             atomic.Bool
	expectingContinue atomic.Bool  // true when compaction received; work events clear it
	resultSent        atomic.Bool  // true when EventResult has been sent for this turn
	lastEvent         atomic.Int64 // unix nano of the last event received; stall watchdog input

	// procMu guards stdinW and currentCmd: they point at the live process's
	// stdin pipe and *exec.Cmd, set by launch and read by the stall watchdog.
	procMu     sync.Mutex
	stdinW     *io.PipeWriter
	currentCmd *exec.Cmd

	// stepMu guards stepTexts: text parts are buffered per step until
	// step_finish so we can tell intermediate steps (finish reason
	// tool-calls — progress text) from the final step (reason stop — the
	// real answer) instead of leaking every per-step narration into the
	// final reply.
	stepMu    sync.Mutex
	stepTexts []string

	// usageMu guards usage: OpenCode reports per-step token usage in the
	// step-finish part's "tokens" field. Input/output/cache counts
	// accumulate across the turn (reported on EventResult and in the reply
	// footer via ContextUsageReporter); TotalTokens is kept as the latest
	// snapshot (current context load).
	usageMu sync.RWMutex
	usage   *core.ContextUsage
}

func newOpencodeSession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, agentName, resumeID string, extraEnv []string) (*opencodeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &opencodeSession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		agentName: agentName,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	s.alive.Store(true)
	go s.stallWatchdog(sessionCtx, 30*time.Second)

	if resumeID != "" && resumeID != core.ContinueSession {
		s.chatID.Store(resumeID)
	}

	return s, nil
}

func (s *opencodeSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	s.resultSent.Store(false)
	s.expectingContinue.Store(false)
	// Reset the stall-watchdog clock for the new turn. lastEvent must not
	// carry over from the previous turn: the watchdog would otherwise see a
	// multi-minute idle, kill the freshly launched process, and the turn ends
	// with an empty reply (observed live: response_len=11, input_tokens=0,
	// watchdog log "killing stalled process idle=5m3s" one second after the
	// message arrived).
	s.lastEvent.Store(time.Now().UnixNano())
	s.usageMu.Lock()
	s.usage = nil
	s.usageMu.Unlock()

	chatID := s.CurrentSessionID()
	isResume := chatID != ""

	if err := s.launch(prompt, imagePaths, chatID); err != nil {
		return err
	}

	slog.Debug("opencodeSession: launched", "resume", isResume)

	return nil
}

// launch starts one opencode run process for the given prompt, resuming
// chatID when non-empty, and begins reading its output events. It is used both
// for the initial Send and for the automatic "continue" follow-up process
// after an auto-compaction (see readLoop).
func (s *opencodeSession) launch(prompt string, imagePaths []string, chatID string) error {
	args := s.buildRunArgs(prompt, imagePaths, chatID)
	slog.Debug("opencodeSession: launching", "resume", chatID != "", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opencodeSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	// Use a writable stdin pipe instead of a one-shot strings.Reader so the
	// prompt can be written asynchronously (a pipe write blocks until the
	// reader consumes it). writeStdin closes the pipe once the prompt is
	// consumed: OpenCode's run command only starts executing after stdin EOF
	// (verified empirically), so leaving the pipe open would hang the turn
	// before the first tool call. Compaction does not need this pipe at all:
	// opencode V2 auto-retries the pending step inside the same process after
	// a checkpoint, so nothing is ever written into it after the prompt.
	pr, pw := io.Pipe()
	cmd.Stdin = pr

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("opencodeSession: start: %w", err)
	}

	s.procMu.Lock()
	s.stdinW = pw
	s.currentCmd = cmd
	s.procMu.Unlock()

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	// Write the prompt to the process's stdin. Write in a goroutine because a
	// pipe blocks until the reader consumes; opencode reads the prompt right
	// after startup.
	go writeStdin(pw, prompt, s.ctx)

	return nil
}

// writeStdin writes text to a process's stdin pipe without blocking forever:
// if the reader never consumes (process died without reading, or stopped
// reading), the write is abandoned after stdinWriteTimeout and the pipe is
// closed so the writer goroutine unblocks with ErrClosedPipe. This guards
// against a new deadlock variant where a pipe write blocks the readLoop's
// event handling.
const stdinWriteTimeout = 10 * time.Second

func writeStdin(pw *io.PipeWriter, text string, ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := io.WriteString(pw, text); err != nil {
			slog.Debug("opencodeSession: write stdin failed", "error", err)
		}
	}()
	select {
	case <-done:
		// Prompt fully consumed by the process. Close the pipe so opencode
		// sees stdin EOF and starts executing — `opencode run` waits for EOF
		// before running (verified empirically); an open pipe would hang the
		// turn before the first tool call.
		_ = pw.Close()
	case <-ctx.Done():
		_ = pw.Close()
	case <-time.After(stdinWriteTimeout):
		slog.Warn("opencodeSession: stdin write timed out, closing pipe")
		_ = pw.Close()
	}
}

func (s *opencodeSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("opencodeSession: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := opencodeImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("opencodeSession: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if prompt == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func opencodeImageExt(mimeType string) string {
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

func (s *opencodeSession) buildRunArgs(prompt string, imagePaths []string, chatID string) []string {
	args := append(append([]string{}, s.extraArgs...), "run", "--format", "json")

	if chatID != "" {
		args = append(args, "--session", chatID)
	}
	if s.agentName != "" {
		args = append(args, "--agent", s.agentName)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.workDir != "" {
		args = append(args, "--dir", s.workDir)
	}

	// Enable thinking blocks.
	args = append(args, "--thinking")

	// In yolo/auto mode, skip permission prompts entirely so headless
	// runs don't get stuck with auto-rejected external-directory ops.
	if s.mode == "yolo" {
		args = append(args, "--dangerously-skip-permissions")
	}

	for _, imagePath := range imagePaths {
		if imagePath == "" {
			continue
		}
		args = append(args, "--file", imagePath)
	}

	return args
}

// readLoop drains output events from one or more opencode processes launched
// for this turn. Normally one process handles the whole turn. After an
// auto-compaction opencode V2 creates a checkpoint and automatically retries
// the pending model step in the SAME process — it does not stop and it does
// not wait for stdin. So this loop keeps reading events through any number of
// compactions. It only sees EOF when the process exits for real: normally the
// turn finished (expectingContinue false → normal EventResult), or the
// process was killed by the stall watchdog after a long event silence
// (checkpoint generation failed / model hung → expectingContinue still set →
// end the turn with a clear notice instead of hanging forever).
func (s *opencodeSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	defer func() { _ = cmd.Wait() }()

	_ = s.readProcess(stdout, stderrBuf)

	if !s.expectingContinue.Load() {
		slog.Debug("opencodeSession: readLoop complete, sending fallback EventResult", "session_id", s.CurrentSessionID())
		s.sendEventResult("")
		return
	}

	// The process exited while a compaction was the last thing we saw: either
	// the watchdog killed it after a stall, or opencode gave up after a failed
	// checkpoint/retry. End the turn with a clear notice rather than an empty
	// reply or a hang.
	s.expectingContinue.Store(false)
	s.sendCompactionNotice()
	s.sendEventResult("")
}

// readProcess reads NDJSON events from stdout until EOF and reports whether
// the process ended cleanly (no scanner error, no stderr content). It is the
// body of one opencode process's read loop.
func (s *opencodeSession) readProcess(stdout io.ReadCloser, stderrBuf *bytes.Buffer) bool {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("opencodeSession: non-JSON line", "line", line)
			continue
		}

		s.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("opencodeSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err), ErrorKind: opencodeErrorKind(err.Error())}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return false
	}

	stderrMsg := stderrBuf.String()
	if stderrMsg != "" {
		slog.Error("opencodeSession: process error", "stderr", truncate(stderrMsg, 500))
		if strings.Contains(stderrMsg, "Session not found") {
			s.chatID.Store("")
			slog.Warn("opencodeSession: cleared stale session ID")
		}
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg), ErrorKind: opencodeErrorKind(stderrMsg)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return false
	}
	return true
}

// OpenCode NDJSON event structure:
//
//	{ "type": "text|tool_use|reasoning|step_start|step_finish",
//	  "part": { "type": "text|tool|reasoning|step-start|step-finish", ... } }

// markContinued clears the compaction-waiting flag once the process shows
// real work again (a tool call, reasoning, a new step, or a normal text
// event). Without this, expectingContinue would stay set from the previous
// compaction round and the next compaction could never re-arm the
// kill/resume path.
func (s *opencodeSession) markContinued() {
	if s.expectingContinue.Load() {
		s.expectingContinue.Store(false)
	}
}

// stallWatchdog guards against a hung opencode process. Normally opencode
// emits events continuously while a turn runs (reasoning, tool calls, text).
// If no event arrives for stallTimeout, something is stuck — most often the
// V2 auto-compaction checkpoint generation (the summary model call never
// returns) or a provider that went silent. The watchdog kills the process so
// readLoop sees EOF and ends the turn with a notice instead of hanging for
// 20+ minutes (the pre-fix behavior, observed live as a 22-minute stall).
// Started once per session; it only acts while a process is alive and the
// turn has not completed.
const stallTimeout = 5 * time.Minute

func (s *opencodeSession) stallWatchdog(ctx context.Context, tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.alive.Load() || s.resultSent.Load() {
				continue
			}
			last := s.lastEvent.Load()
			if last == 0 {
				continue // no events yet this turn (opencode still starting)
			}
			idle := time.Since(time.Unix(0, last))
			if idle < stallTimeout {
				continue
			}
			s.procMu.Lock()
			cmd := s.currentCmd
			s.procMu.Unlock()
			if cmd == nil || cmd.Process == nil {
				continue
			}
			slog.Warn("opencodeSession: no events for a long time, killing stalled process", "session_id", s.CurrentSessionID(), "idle", idle.Round(time.Second))
			_ = cmd.Process.Kill()
		}
	}
}

// sendCompactionNotice delivers a user-visible message explaining that the
// turn was terminated: the process stalled (usually during/after context
// auto-compaction — checkpoint generation failed or the model went silent).
// Delivered as an EventText before the final EventResult so the user sees the
// reason instead of an empty reply.
func (s *opencodeSession) sendCompactionNotice() {
	msg := "⚠️ 任务处理超时（长时间无响应），已自动终止。请重试；若任务较大，建议拆成更小的步骤分步发送。"
	evt := core.Event{Type: core.EventText, Content: msg}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func (s *opencodeSession) handleEvent(raw map[string]any) {
	s.lastEvent.Store(time.Now().UnixNano())

	eventType, _ := raw["type"].(string)

	switch eventType {
	case "text":
		s.handleText(raw)
	case "tool_use":
		s.handleToolUse(raw)
	case "reasoning":
		s.handleReasoning(raw)
	case "step_start":
		s.handleStepStart(raw)
	case "step_finish":
		s.handleStepFinish(raw)
	case "error":
		s.handleError(raw)
	default:
		b, _ := json.Marshal(raw)
		slog.Debug("opencodeSession: unhandled event", "type", eventType, "raw", string(b))
	}
}

func (s *opencodeSession) handleText(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	text, _ := part["text"].(string)

	// Extract metadata and synthetic flags to identify compaction_continue
	metadata, _ := part["metadata"].(map[string]any)
	synthetic, _ := part["synthetic"].(bool)

	// Check for compaction_continue: this is OpenCode's auto-continuation signal.
	// OpenCode V2 compaction is checkpoint-based: after generating the summary
	// it automatically retries the pending model step with the checkpointed
	// history — it does NOT wait for input. We must NOT kill the process here:
	// killing interrupts the retry and the resumed process re-compacts in a
	// loop (observed live: one compaction every ~30s ending in an empty
	// reply). We only mark that a compaction happened; real work events clear
	// the flag, and the stall watchdog kills the process if no event arrives
	// for a long time (checkpoint generation failed / model hung).
	if synthetic && metadata != nil {
		if cc, ok := metadata["compaction_continue"].(bool); ok && cc {
			slog.Info("opencodeSession: compaction_continue detected", "session_id", s.CurrentSessionID())
			s.expectingContinue.CompareAndSwap(false, true)
			// Do NOT send EventText - this is internal continuation signal
			return
		}
	}

	// OpenCode's automatic compaction writes the compaction summary as a plain
	// text part (no synthetic flag, no metadata) — the summary is the internal
	// "Objective / Important Details / Work State" state, NOT a reply for the
	// user. Suppress it exactly like compaction_continue so it never leaks
	// into the chat as a giant formatted dump. Do not kill the process: V2
	// auto-retries the pending step after the checkpoint.
	if text != "" && isOpencodeCompactionSummary(text) {
		slog.Info("opencodeSession: compaction summary text suppressed", "session_id", s.CurrentSessionID(), "len", len(text))
		s.expectingContinue.CompareAndSwap(false, true)
		return
	}

	if text == "" {
		return
	}

	// Any real (non-compaction) text event proves the process is working
	// (including after a compaction); clear the flag so a later stall after
	// another compaction is still detected as a compaction-then-exit.
	s.markContinued()

	// Buffer the text for the current step. step_finish decides whether this
	// step was intermediate (progress, folded into the process panel) or the
	// final answer (delivered via EventResult.Content).
	s.stepMu.Lock()
	s.stepTexts = append(s.stepTexts, text)
	s.stepMu.Unlock()
}

// isOpencodeCompactionSummary reports whether text is OpenCode's automatic
// compaction summary. Compaction summaries follow a fixed template headed by
// "## Objective" and containing at least one of the other known sections
// ("## Important Details", "## Work State"). Normal user-facing replies
// essentially never start with this exact header combination.
func isOpencodeCompactionSummary(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "## Objective") {
		return false
	}
	return strings.Contains(trimmed, "\n## Important Details") ||
		strings.Contains(trimmed, "\n## Work State") ||
		strings.Contains(trimmed, "## Objective\n")
}

func (s *opencodeSession) handleToolUse(raw map[string]any) {
	s.markContinued()
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}

	toolName, _ := part["tool"].(string)

	state, _ := part["state"].(map[string]any)
	status := ""
	if state != nil {
		status, _ = state["status"].(string)
	}

	// Extract tool input summary for display
	input := extractToolInput(state)

	if status == "completed" {
		// OpenCode bundles call + result in one event; emit both for UI.
		useEvt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- useEvt:
		case <-s.ctx.Done():
			return
		}

		output, _ := state["output"].(string)
		resultEvt := core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncate(output, 500)}
		select {
		case s.events <- resultEvt:
		case <-s.ctx.Done():
			return
		}
	} else {
		evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}

		// When a tool call is rejected (e.g. permission denied in default mode),
		// opencode exits without generating any follow-up text. Surface the rejection
		// reason so the engine has something meaningful to send rather than "(空响应)".
		// This covers the common case where the user has not configured tool permissions
		// and needs guidance to use mode="yolo" or update opencode settings.
		if status == "error" && state != nil {
			errMsg, _ := state["error"].(string)
			if errMsg != "" {
				slog.Info("opencodeSession: tool rejected, surfacing error as text", "tool", toolName, "error", errMsg)
				errEvt := core.Event{Type: core.EventText, Content: errMsg}
				select {
				case s.events <- errEvt:
				case <-s.ctx.Done():
					return
				}
			}
		}
	}
}

func extractToolInput(state map[string]any) string {
	if state == nil {
		return ""
	}
	// Prefer title as a concise description (e.g. "List files in current directory")
	if title, ok := state["title"].(string); ok && title != "" {
		return title
	}
	switch input := state["input"].(type) {
	case string:
		return input
	case map[string]any:
		// Use "description" or "command" fields if available
		if desc, ok := input["description"].(string); ok && desc != "" {
			return desc
		}
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			return cmd
		}
		b, _ := json.Marshal(input)
		return truncate(string(b), 200)
	}
	return ""
}

func (s *opencodeSession) handleReasoning(raw map[string]any) {
	s.markContinued()
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	text, _ := part["text"].(string)
	if text == "" {
		return
	}
	// OpenCode's auto-compaction is itself a model generation: before emitting
	// the compaction summary it "thinks" about producing it ("Let me analyze
	// the conversation to build a comprehensive summary..."). That internal
	// reasoning is not a user-visible step; suppress it the same way we
	// suppress the summary text itself, otherwise it leaks as a giant
	// "💭 Thinking" block followed by an empty reply.
	if isCompactionReasoning(text) {
		slog.Debug("opencodeSession: compaction reasoning suppressed", "session_id", s.CurrentSessionID(), "len", len(text))
		return
	}
	evt := core.Event{Type: core.EventThinking, Content: text}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		return
	}
}

// isCompactionReasoning reports whether a reasoning block is OpenCode's
// internal auto-compaction work (building the "Objective / Important Details"
// summary) rather than a genuine step of the current task. The phrasing is
// characteristic of the compaction prompt the model receives; a normal
// user-facing reasoning block essentially never starts with these.
func isCompactionReasoning(text string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	markers := []string{
		"let me analyze the conversation to build a comprehensive summary",
		"let me combine the prior summary with the conversation",
		"let me analyze the conversation and combine the prior summary",
		"let me combine the previous summary with the conversation",
		"let me analyze the entire conversation to build a comprehensive summary",
		"let me analyze the full conversation to produce a comprehensive summary",
		"let me analyze this conversation to build a comprehensive summary",
		"let me review the conversation to build a comprehensive summary",
		"let me review the prior summary and the conversation",
		"let me analyze the conversation and produce a comprehensive summary",
	}
	for _, m := range markers {
		if strings.HasPrefix(lower, m) {
			return true
		}
	}
	return false
}

func (s *opencodeSession) handleError(raw map[string]any) {
	errMsg := extractErrorMessage(raw)
	slog.Error("opencodeSession: agent error", "error", errMsg)
	evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg), ErrorKind: opencodeErrorKind(errMsg)}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		return
	}
}

// extractErrorMessage tries to pull a human-readable message from various
// OpenCode error JSON shapes.
func extractErrorMessage(raw map[string]any) string {
	// Shape: {"error": {"data": {"message": "..."}, "name": "..."}}
	if errObj, ok := raw["error"].(map[string]any); ok {
		if data, ok := errObj["data"].(map[string]any); ok {
			if msg, ok := data["message"].(string); ok && msg != "" {
				name, _ := errObj["name"].(string)
				if name != "" {
					return name + ": " + msg
				}
				return msg
			}
		}
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
		if name, ok := errObj["name"].(string); ok && name != "" {
			return name
		}
	}
	// Shape: {"error": "string message"}
	if errStr, ok := raw["error"].(string); ok && errStr != "" {
		return errStr
	}
	// Shape: {"part": {"error": "...", "message": "..."}}
	if part, ok := raw["part"].(map[string]any); ok {
		if msg, ok := part["error"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := part["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := raw["message"].(string); ok && msg != "" {
		return msg
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

// opencodeErrorKind classifies an opencode error message into a retriable
// error kind, mirroring codexErrorKind in agent/codex (PR #1641). The engine
// retries EventError events whose ErrorKind.IsRetriable() is true, so
// transient server failures such as
// "APIError: Our servers are currently overloaded. Please try again later."
// get the same automatic whole-turn retry the codex agent already has instead
// of failing the turn with a bare "❌ 错误:" message to the user.
func opencodeErrorKind(message string) core.ErrorKind {
	msg := strings.ToLower(message)
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit") {
		return core.ErrorKindRateLimit
	}
	if strings.Contains(msg, "at capacity") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "temporarily unavailable") ||
		strings.Contains(msg, "you can retry your request") ||
		strings.Contains(msg, "processing your request") ||
		strings.Contains(msg, "stream disconnected") ||
		strings.Contains(msg, "stream closed") ||
		strings.Contains(msg, "unexpected status 502") ||
		strings.Contains(msg, "unexpected status 503") ||
		strings.Contains(msg, "unexpected status 504") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "gateway timeout") {
		return core.ErrorKindOverloaded
	}
	return core.ErrorKindUnknown
}

func (s *opencodeSession) handleStepStart(raw map[string]any) {
	// A new step begins; the process is actively working again (this also
	// clears the compaction waiting flag set by a previous compaction).
	s.markContinued()
	// A new step begins; drop any text buffered outside a step boundary
	// (e.g. suppressed compaction summary text is never buffered, but a
	// process restart could otherwise leave stale narration behind).
	s.stepMu.Lock()
	s.stepTexts = nil
	s.stepMu.Unlock()

	sessionID, _ := raw["sessionID"].(string)
	if sessionID == "" {
		part, _ := raw["part"].(map[string]any)
		if part != nil {
			sessionID, _ = part["sessionID"].(string)
		}
	}
	if sessionID != "" {
		s.chatID.Store(sessionID)
		slog.Debug("opencodeSession: session started", "session_id", sessionID)
	}
}

func (s *opencodeSession) handleStepFinish(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	reason := ""
	if part != nil {
		reason, _ = part["reason"].(string)
	}

	// Flush the buffered texts of the finished step.
	//
	// Intermediate steps (finish reason "tool-calls") forward their narration
	// as normal EventText: on streaming-card platforms the engine folds it
	// into the collapsible thinking panel, everywhere else it surfaces as
	// per-step messages — unchanged behavior.
	//
	// The final step (reason "stop") does NOT forward its text as EventText.
	// Its narration is the real answer; it is delivered once via
	// EventResult.Content so the final reply contains ONLY the answer, never
	// the accumulated per-step narration (which would otherwise leak into the
	// final message on payload-card platforms, whose separate reply is built
	// from the full response).
	s.stepMu.Lock()
	texts := s.stepTexts
	s.stepTexts = nil
	s.stepMu.Unlock()

	// Accumulate this step's token usage (OpenCode reports it in the
	// step-finish part's "tokens" field). Input/output/cache counts sum
	// across the whole turn; total stays the latest snapshot.
	if stepUsage := parseStepTokens(part); stepUsage != nil {
		s.usageMu.Lock()
		if s.usage == nil {
			s.usage = stepUsage
		} else {
			s.usage.InputTokens += stepUsage.InputTokens
			s.usage.OutputTokens += stepUsage.OutputTokens
			s.usage.ReasoningOutputTokens += stepUsage.ReasoningOutputTokens
			s.usage.CachedInputTokens += stepUsage.CachedInputTokens
			s.usage.CacheCreationInputTokens += stepUsage.CacheCreationInputTokens
			s.usage.TotalTokens = stepUsage.TotalTokens
		}
		s.usageMu.Unlock()
	}

	if reason == "stop" {
		s.sendEventResult(strings.Join(texts, "\n"))
		return
	}
	for _, text := range texts {
		if text == "" {
			continue
		}
		evt := core.Event{Type: core.EventText, Content: text}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}
	slog.Debug("opencodeSession: step finished", "reason", reason, "session_id", s.CurrentSessionID())
}

func (s *opencodeSession) sendEventResult(content string) {
	if s.resultSent.Load() {
		slog.Debug("opencodeSession: EventResult already sent, skipping", "session_id", s.CurrentSessionID())
		return
	}
	// After an auto-compaction the agent stops and waits for the next prompt;
	// its step_finish(stop) would otherwise produce an empty reply. Defer the
	// EventResult so readLoop can resume the session with "continue" and the
	// turn ends with the agent's real answer instead.
	if s.expectingContinue.Load() {
		slog.Info("opencodeSession: deferring EventResult after compaction, will resume with continue", "session_id", s.CurrentSessionID())
		return
	}
	s.resultSent.Store(true)
	sid := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sid, Content: content, Done: true}
	// OpenCode reports per-step token usage in the step-finish part's
	// "tokens" field; carry the turn totals so the engine logs real numbers
	// (input_tokens/output_tokens were previously always 0 for opencode).
	s.usageMu.RLock()
	if u := s.usage; u != nil {
		evt.InputTokens = u.InputTokens
		evt.OutputTokens = u.OutputTokens
		evt.CacheReadInputTokens = u.CachedInputTokens
		evt.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	s.usageMu.RUnlock()
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// parseStepTokens extracts OpenCode's per-step usage from a step-finish
// part. OpenCode reports it as:
//
//	"tokens":{"total":N,"input":N,"output":N,"reasoning":N,"cache":{"write":N,"read":N}}
//
// Returns nil when the part carries no tokens field.
func parseStepTokens(part map[string]any) *core.ContextUsage {
	if part == nil {
		return nil
	}
	raw, _ := part["tokens"].(map[string]any)
	if raw == nil {
		return nil
	}
	usage := &core.ContextUsage{}
	if v, ok := raw["input"].(float64); ok {
		usage.InputTokens = int(v)
	}
	if v, ok := raw["output"].(float64); ok {
		usage.OutputTokens = int(v)
	}
	if v, ok := raw["reasoning"].(float64); ok {
		usage.ReasoningOutputTokens = int(v)
	}
	if v, ok := raw["total"].(float64); ok {
		usage.TotalTokens = int(v)
	}
	if cache, ok := raw["cache"].(map[string]any); ok {
		if v, ok := cache["read"].(float64); ok {
			usage.CachedInputTokens = int(v)
		}
		if v, ok := cache["write"].(float64); ok {
			usage.CacheCreationInputTokens = int(v)
		}
	}
	return usage
}

// GetContextUsage implements core.ContextUsageReporter so the reply footer
// shows real token counts (out/in/cr) for opencode turns, matching the
// claude/codex footer behavior. ContextWindow is unknown to the opencode
// adapter, so the ctx-% section is omitted.
func (s *opencodeSession) GetContextUsage() *core.ContextUsage {
	s.usageMu.RLock()
	defer s.usageMu.RUnlock()
	if s.usage == nil {
		return nil
	}
	cp := *s.usage
	return &cp
}

// RespondPermission is a no-op — OpenCode handles permissions internally.
func (s *opencodeSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *opencodeSession) Events() <-chan core.Event {
	return s.events
}

func (s *opencodeSession) CurrentSessionID() string {
	v, _ := s.chatID.Load().(string)
	return v
}

func (s *opencodeSession) Alive() bool {
	return s.alive.Load()
}

func (s *opencodeSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(s.events)
	case <-time.After(8 * time.Second):
		slog.Warn("opencodeSession: close timed out, abandoning wg.Wait")
	}
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
