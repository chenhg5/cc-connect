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
	expectingContinue atomic.Bool  // true when compaction_continue received, waiting for next step
	resultSent        atomic.Bool  // true when EventResult has been sent for this turn
	continuations     atomic.Int32 // number of automatic "continue" follow-up processes launched after compaction

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

// maxCompactionContinuations bounds how many automatic resume processes we
// launch after an OpenCode auto-compaction. OpenCode stops after compaction
// and waits for the next prompt; we resume the same session with a "continue"
// prompt so the turn completes instead of ending with an empty reply. If the
// agent keeps re-compacting, give up after this many rounds.
const maxCompactionContinuations = 5

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
	cmd.Stderr = &stderrBuf
	cmd.Stdin = strings.NewReader(prompt)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opencodeSession: start: %w", err)
	}

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	return nil
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
// auto-compaction opencode stops and waits for the next prompt, so when the
// initial process ends while expectingContinue is set we automatically resume
// the same session with a "continue" prompt — otherwise the turn would end
// with an empty reply (the compaction summary was suppressed and the agent
// never produced a final answer). Follow-up processes keep feeding the same
// events channel until one ends without expectingContinue.
func (s *opencodeSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	defer func() { _ = cmd.Wait() }()

	// readProcess returns true when the process ended normally (no
	// scanner/stderr error) and the engine should keep going.
	keepGoing := s.readProcess(stdout, stderrBuf)

	if !s.expectingContinue.Load() {
		slog.Debug("opencodeSession: readLoop complete, sending fallback EventResult", "session_id", s.CurrentSessionID())
		s.sendEventResult("")
		return
	}

	// Auto-compaction happened: opencode stopped and waits for input.
	// Resume the same session with "continue" so the turn finishes.
	s.expectingContinue.Store(false)
	if s.continuations.Load() >= maxCompactionContinuations {
		slog.Warn("opencodeSession: compaction continue limit reached, ending turn", "session_id", s.CurrentSessionID())
		s.sendEventResult("")
		return
	}
	if !keepGoing {
		// The previous process died with an error; don't spin a follow-up.
		return
	}
	s.continuations.Add(1)
	slog.Info("opencodeSession: compaction detected, resuming session with continue", "session_id", s.CurrentSessionID(), "round", s.continuations.Load())

	chatID := s.CurrentSessionID()
	if err := s.launch("continue", nil, chatID); err != nil {
		slog.Error("opencodeSession: failed to launch continue process", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("opencodeSession: launch continue: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return
	}
	// The new process has its own readLoop goroutine; this one exits.
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
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
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
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
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
func (s *opencodeSession) handleEvent(raw map[string]any) {
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
	// When received, we should NOT send EventText to engine, but mark that we expect
	// a continuation (next step_start will start a new turn without EventResult).
	if synthetic && metadata != nil {
		if cc, ok := metadata["compaction_continue"].(bool); ok && cc {
			slog.Info("opencodeSession: compaction_continue detected, marking expectingContinue", "session_id", s.CurrentSessionID())
			s.expectingContinue.Store(true)
			// Do NOT send EventText - this is internal continuation signal
			return
		}
	}

	// OpenCode's automatic compaction writes the compaction summary as a plain
	// text part (no synthetic flag, no metadata) — the summary is the internal
	// "Objective / Important Details / Work State" state, NOT a reply for the
	// user. When we see it, suppress it exactly like compaction_continue so it
	// never leaks into the chat as a giant formatted dump. The agent keeps
	// working afterwards and its real output arrives as normal text events.
	if text != "" && isOpencodeCompactionSummary(text) {
		slog.Info("opencodeSession: compaction summary text suppressed", "session_id", s.CurrentSessionID(), "len", len(text))
		s.expectingContinue.Store(true)
		return
	}

	if text == "" {
		return
	}

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
	evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
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

func (s *opencodeSession) handleStepStart(raw map[string]any) {
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
