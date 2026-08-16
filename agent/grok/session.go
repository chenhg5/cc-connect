package grok

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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

const (
	maxGrokJSONLine    = 16 * 1024 * 1024
	maxGrokStderr      = 1024 * 1024
	maxToolPreview     = 4000
	minRepeatUnitRunes = 4
	maxRepeatUnitRunes = 32
	minRepeatCount     = 16
)

type sessionConfig struct {
	cmd             string
	extraArgs       []string
	workDir         string
	model           string
	mode            string
	resumeID        string
	extraEnv        []string
	timeout         time.Duration
	reasoningEffort string
	maxTurns        int
}

type processCleanupRecorder struct {
	mu  sync.Mutex
	err error
}

func (recorder *processCleanupRecorder) record(err error) error {
	if err == nil {
		return nil
	}
	recorder.mu.Lock()
	recorder.err = errors.Join(recorder.err, err)
	recorder.mu.Unlock()
	return err
}

func (recorder *processCleanupRecorder) Err() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.err
}

// grokSession is a persistent cc-connect session backed by one Grok headless
// process per turn. turnMu serializes resume calls and is also the lifecycle
// barrier used by Close, so the event channel cannot close while Send is
// starting a process or a reader is still emitting events.
type grokSession struct {
	cmd             string
	extraArgs       []string
	workDir         string
	model           string
	mode            string
	timeout         time.Duration
	extraEnv        []string
	processEnv      []string
	reasoningEffort string
	maxTurns        int
	forceKill       func(*exec.Cmd) error

	events    chan core.Event
	sessionID atomic.Value // string
	ctx       context.Context
	cancel    context.CancelFunc
	alive     atomic.Bool

	turnMu       sync.Mutex
	turnCancelMu sync.Mutex
	turnCancel   context.CancelFunc
	turnDone     <-chan struct{}
	wg           sync.WaitGroup
	closeOnce    sync.Once
	closeDone    chan struct{}

	usageMu   sync.RWMutex
	lastUsage core.ContextUsage
}

func newGrokSession(ctx context.Context, cfg sessionConfig) (*grokSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	gs := &grokSession{
		cmd:             cfg.cmd,
		extraArgs:       append([]string(nil), cfg.extraArgs...),
		workDir:         cfg.workDir,
		model:           cfg.model,
		mode:            cfg.mode,
		timeout:         cfg.timeout,
		extraEnv:        append([]string(nil), cfg.extraEnv...),
		processEnv:      grokProcessEnv(cfg.extraEnv),
		reasoningEffort: cfg.reasoningEffort,
		maxTurns:        cfg.maxTurns,
		forceKill:       forceKillCmd,
		events:          make(chan core.Event, 128),
		ctx:             sessionCtx,
		cancel:          cancel,
		closeDone:       make(chan struct{}),
	}
	gs.alive.Store(true)
	if cfg.resumeID != "" && cfg.resumeID != core.ContinueSession {
		gs.sessionID.Store(cfg.resumeID)
	}
	return gs, nil
}

func (gs *grokSession) buildArgs(promptFile string) []string {
	args := append([]string(nil), gs.extraArgs...)
	args = append(args,
		"--cwd", gs.workDir,
		"--output-format", "streaming-messages-json",
		"--include-partial-messages",
		"--no-ask-user",
		"--no-auto-update",
		"--permission-mode", permissionModeFlag(gs.mode),
	)
	if gs.mode == "yolo" {
		args = append(args, "--always-approve")
	}
	if sessionID := gs.CurrentSessionID(); sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	// An empty model is load-bearing: it inherits the same model selected by
	// a direct local `grok` invocation.
	if gs.model != "" {
		args = append(args, "--model", gs.model)
	}
	if gs.reasoningEffort != "" {
		args = append(args, "--reasoning-effort", gs.reasoningEffort)
	}
	if gs.maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", gs.maxTurns))
	}
	return append(args, "--prompt-file", promptFile)
}

func (gs *grokSession) Send(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !gs.alive.Load() {
		return errors.New("grok: session is closed")
	}

	gs.turnMu.Lock()
	started := false
	defer func() {
		if !started {
			gs.turnMu.Unlock()
		}
	}()
	if !gs.alive.Load() {
		return errors.New("grok: session is closed")
	}

	turnCtx, turnCancel := gs.newTurnContext()
	gs.setTurnCancel(turnCancel, turnCtx.Done())
	defer func() {
		if !started {
			gs.clearTurnCancel(turnCancel)
			turnCancel()
		}
	}()

	fullPrompt, err := gs.promptWithAttachments(prompt, messageID, images, files)
	if err != nil {
		return err
	}
	promptFile, err := gs.writePromptFile(fullPrompt)
	if err != nil {
		return err
	}
	cleanupPrompt := true
	defer func() {
		if cleanupPrompt {
			removeTempFile(promptFile)
		}
	}()

	if err := turnCtx.Err(); err != nil {
		return fmt.Errorf("grok: turn stopped: %w", err)
	}

	args := gs.buildArgs(promptFile)
	slog.Debug("grok: launching headless turn",
		"resume", gs.CurrentSessionID() != "",
		"cmd", gs.cmd,
		"args", core.RedactArgs(args))
	cmd := exec.CommandContext(turnCtx, gs.cmd, args...)
	cmd.Dir = gs.workDir
	cmd.Env = append([]string(nil), gs.processEnv...)
	cmd.WaitDelay = 2 * time.Second
	prepareCmdForKill(cmd)
	killCmd := gs.forceKill
	if killCmd == nil {
		killCmd = forceKillCmd
	}
	cleanup := &processCleanupRecorder{}
	cmd.Cancel = func() error { return cleanup.record(killCmd(cmd)) }

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("grok: create stdout pipe: %w", err)
	}
	stderr := &cappedWriter{limit: maxGrokStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return fmt.Errorf("grok: start headless turn: %w", err)
	}

	started = true
	cleanupPrompt = false
	gs.wg.Add(1)
	go func() {
		defer gs.turnMu.Unlock()
		defer gs.wg.Done()
		defer turnCancel()
		defer gs.clearTurnCancel(turnCancel)
		defer removeTempFile(promptFile)
		gs.readLoop(turnCtx, cmd, stdout, stderr, killCmd, cleanup)
	}()
	return nil
}

func (gs *grokSession) newTurnContext() (context.Context, context.CancelFunc) {
	if gs.timeout > 0 {
		return context.WithTimeout(gs.ctx, gs.timeout)
	}
	return context.WithCancel(gs.ctx)
}

func (gs *grokSession) setTurnCancel(cancel context.CancelFunc, done <-chan struct{}) {
	gs.turnCancelMu.Lock()
	gs.turnCancel = cancel
	gs.turnDone = done
	gs.turnCancelMu.Unlock()
}

func (gs *grokSession) clearTurnCancel(_ context.CancelFunc) {
	gs.turnCancelMu.Lock()
	// Turns are serialized, so the current cancel belongs to this reader.
	gs.turnCancel = nil
	gs.turnDone = nil
	gs.turnCancelMu.Unlock()
}

func (gs *grokSession) promptWithAttachments(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (string, error) {
	attachments := make([]core.FileAttachment, 0, len(images)+len(files))
	for i, image := range images {
		name := image.FileName
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("image_%d%s", i+1, imageExtension(image.MimeType))
		}
		attachments = append(attachments, core.FileAttachment{
			MimeType: image.MimeType,
			Data:     image.Data,
			FileName: name,
		})
	}
	attachments = append(attachments, files...)
	if len(attachments) == 0 {
		return prompt, nil
	}
	if strings.TrimSpace(messageID) == "" {
		messageID = fmt.Sprintf("grok-%d", time.Now().UnixNano())
	}
	paths := core.SaveFilesToDisk(gs.workDir, messageID, attachments)
	if len(paths) != len(attachments) {
		return "", fmt.Errorf("grok: saved %d of %d attachments", len(paths), len(attachments))
	}
	return core.AppendFileRefs(prompt, paths), nil
}

func imageExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".png"
	}
}

func (gs *grokSession) writePromptFile(prompt string) (string, error) {
	dir := filepath.Join(gs.workDir, ".cc-connect", "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		dir = ""
	} else if err := os.Chmod(dir, 0o700); err != nil {
		slog.Debug("grok: tighten prompt directory permissions", "path", dir, "error", err)
	}
	file, err := os.CreateTemp(dir, "grok-prompt-*.txt")
	if err != nil {
		return "", fmt.Errorf("grok: create prompt file: %w", err)
	}
	name := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		removeTempFile(name)
		return "", fmt.Errorf("grok: secure prompt file: %w", err)
	}
	if _, err := io.WriteString(file, prompt); err != nil {
		_ = file.Close()
		removeTempFile(name)
		return "", fmt.Errorf("grok: write prompt file: %w", err)
	}
	if err := file.Close(); err != nil {
		removeTempFile(name)
		return "", fmt.Errorf("grok: close prompt file: %w", err)
	}
	return name, nil
}

func removeTempFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("grok: remove prompt file", "path", path, "error", err)
	}
}

func (gs *grokSession) readLoop(
	ctx context.Context,
	cmd *exec.Cmd,
	stdout io.ReadCloser,
	stderr *cappedWriter,
	killCmd func(*exec.Cmd) error,
	cleanup *processCleanupRecorder,
) {
	defer func() {
		if err := stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.Debug("grok: close stdout", "error", err)
		}
	}()

	state := newStreamState()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxGrokJSONLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			// Never log raw CLI output here. Warnings and malformed frames can
			// contain prompt text or credentials inherited by the child process.
			slog.Debug("grok: ignored non-JSON stdout", "bytes", len(line))
			continue
		}
		if state.terminal {
			slog.Debug("grok: ignored event after terminal result", "type", stringValue(raw["type"]))
			continue
		}
		state.terminal = gs.handleEvent(state, raw)
		if state.terminal && state.runaway != "" {
			// Stop the CLI before it keeps generating the loop into a full pipe.
			_ = cleanup.record(killCmd(cmd))
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		// Scanner stops consuming after an oversized token. Kill the process
		// before Wait so a child that is still filling stdout cannot deadlock
		// on a full pipe.
		_ = cleanup.record(killCmd(cmd))
	}
	waitErr := cmd.Wait()
	cleanupDetail := gs.logCleanupFailure(cleanup.Err())

	if state.terminal {
		return
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		stoppedErr := fmt.Errorf("grok: turn stopped: %w", ctxErr)
		gs.emitStopped(core.Event{Type: core.EventError, Error: withCleanupFailure(stoppedErr, cleanupDetail)})
		return
	}
	if scanErr != nil {
		streamErr := fmt.Errorf("grok: read NDJSON stream: %w", scanErr)
		gs.emitTerminal(ctx, core.Event{Type: core.EventError, Error: withCleanupFailure(streamErr, cleanupDetail)})
		return
	}
	if waitErr != nil {
		detail := redactEnvSecrets(stripANSI(strings.TrimSpace(stderr.String())), gs.processEnv)
		if detail == "" {
			detail = waitErr.Error()
		}
		gs.emitTerminal(ctx, core.Event{Type: core.EventError, Error: fmt.Errorf("grok: headless turn failed: %s", truncate(detail, 1200))})
		return
	}

	// A clean process should always emit result. Keep the logical session
	// usable, but finalize this turn if a future CLI version omits it.
	state.flushThinking(gs)
	state.flushText(gs)
	gs.emitTerminal(ctx, core.Event{
		Type:      core.EventResult,
		Content:   state.finalText,
		SessionID: gs.CurrentSessionID(),
		Done:      true,
	})
}

func (gs *grokSession) logCleanupFailure(err error) string {
	if err == nil {
		return ""
	}
	detail := redactEnvSecrets(stripANSI(strings.TrimSpace(err.Error())), gs.processEnv)
	if detail == "" {
		detail = "unknown cleanup failure"
	}
	detail = truncate(detail, 1200)
	slog.Warn("grok: process-tree cleanup incomplete", "error", detail)
	return detail
}

func withCleanupFailure(base error, detail string) error {
	if detail == "" {
		return base
	}
	return fmt.Errorf("%w; process-tree cleanup incomplete: %s", base, detail)
}

type streamBlock struct {
	kind      string
	id        string
	name      string
	inputSeed string
	input     strings.Builder
	value     any
	// streamed accumulates thinking/text for this content-block index.
	// Assistant complete frames reconcile against this per-index body, not a
	// global concatenation (Grok 4.6 emits multiple text blocks per message).
	streamed strings.Builder
}

type streamState struct {
	blocks         map[int]*streamBlock
	toolNames      map[string]string
	emittedTools   map[string]bool
	emittedResults map[string]bool
	// closedThinking/closedText are completed block bodies in message order.
	// Used only to align assistant complete frames to already-streamed blocks.
	closedThinking  []string
	closedText      []string
	pendingThinking strings.Builder
	pendingText     strings.Builder
	partialSeen     bool
	finalText       string
	terminal        bool
	lastInputTokens int
	lastCacheRead   int
	lastCacheCreate int
	currentModel    string
	runaway         string
}

func newStreamState() *streamState {
	return &streamState{
		blocks:         make(map[int]*streamBlock),
		toolNames:      make(map[string]string),
		emittedTools:   make(map[string]bool),
		emittedResults: make(map[string]bool),
	}
}

func (state *streamState) resetMessageBlocks() {
	state.blocks = make(map[int]*streamBlock)
	state.closedThinking = state.closedThinking[:0]
	state.closedText = state.closedText[:0]
	state.partialSeen = false
	state.pendingThinking.Reset()
	state.pendingText.Reset()
}

func (gs *grokSession) handleEvent(state *streamState, raw map[string]any) bool {
	if state.runaway != "" {
		return true
	}
	gs.captureSessionID(raw)
	switch strings.ToLower(stringValue(raw["type"])) {
	case "system":
		if model := strings.TrimSpace(stringValue(raw["model"])); model != "" {
			state.currentModel = model
		}
		return false
	case "stream_event":
		event, _ := raw["event"].(map[string]any)
		state.handlePartial(gs, event)
		if state.runaway != "" {
			return gs.finalizeRunaway(state)
		}
		return false
	case "assistant":
		state.handleAssistantComplete(gs, raw)
		if state.runaway != "" {
			return gs.finalizeRunaway(state)
		}
		return false
	case "user":
		state.handleToolResults(gs, raw)
		return false
	case "result":
		if content := stringValue(raw["result"]); state.runaway == "" && content != "" {
			if clipped, unit, n := clipRepeatedTail(content); n > 0 {
				state.pendingText.Reset()
				state.pendingText.WriteString(clipped)
				state.runaway = fmt.Sprintf("repeated %q %d times", strings.TrimSpace(unit), n)
				return gs.finalizeRunaway(state)
			}
		}
		state.flushThinking(gs)
		state.flushText(gs)
		if resultIsError(raw) {
			return gs.emit(core.Event{Type: core.EventError, Error: gs.streamError(raw)})
		}
		content := stringValue(raw["result"])
		if content == "" {
			content = state.finalText
		}
		event := core.Event{
			Type:      core.EventResult,
			Content:   content,
			SessionID: gs.CurrentSessionID(),
			Done:      true,
			Metadata:  resultMetadata(raw),
		}
		usage, _ := raw["usage"].(map[string]any)
		applyUsage(&event, usage)
		gs.updateContextUsage(event, raw, state)
		return gs.emit(event)
	case "error":
		state.flushThinking(gs)
		state.flushText(gs)
		return gs.emit(core.Event{Type: core.EventError, Error: gs.streamError(raw)})
	default:
		slog.Debug("grok: unhandled NDJSON event", "type", stringValue(raw["type"]))
		return false
	}
}

func (gs *grokSession) streamError(raw map[string]any) error {
	detail := redactEnvSecrets(stripANSI(resultErrorMessage(raw)), gs.processEnv)
	return fmt.Errorf("grok: %s", truncate(detail, 1200))
}

func (gs *grokSession) captureSessionID(raw map[string]any) {
	sessionID := stringValue(raw["session_id"])
	if sessionID == "" {
		sessionID = stringValue(raw["sessionId"])
	}
	if sessionID == "" {
		return
	}
	wasEmpty := gs.CurrentSessionID() == ""
	gs.sessionID.Store(sessionID)
	if wasEmpty {
		// Persist the mapping as soon as init arrives instead of waiting for a
		// possibly interrupted terminal result.
		gs.emit(core.Event{Type: core.EventText, SessionID: sessionID})
	}
}

func (state *streamState) handlePartial(gs *grokSession, event map[string]any) {
	if event == nil {
		return
	}
	typeName := strings.ToLower(stringValue(event["type"]))
	switch typeName {
	case "message_start":
		// New assistant message: reset per-message block index state only.
		// toolNames / emittedTools persist for the whole turn (user tool_result
		// frames need the id→name map from earlier tool_use blocks).
		state.resetMessageBlocks()
	case "content_block_start":
		state.partialSeen = true
		index := intFromAny(event["index"])
		content, _ := event["content_block"].(map[string]any)
		kind := strings.ToLower(stringValue(content["type"]))
		block := &streamBlock{
			kind:  kind,
			id:    stringValue(content["id"]),
			name:  stringValue(content["name"]),
			value: content,
		}
		switch kind {
		case "thinking":
			if text := stringValue(content["thinking"]); text != "" {
				state.appendThinkingDelta(block, text)
			}
		case "text":
			if text := stringValue(content["text"]); text != "" {
				state.appendTextDelta(block, text)
			}
		case "tool_use", "server_tool_use":
			if input, ok := content["input"].(map[string]any); ok && len(input) > 0 {
				encoded, _ := json.Marshal(input)
				block.inputSeed = string(encoded)
			}
		}
		state.blocks[index] = block
	case "content_block_delta":
		state.partialSeen = true
		index := intFromAny(event["index"])
		block := state.blocks[index]
		delta, _ := event["delta"].(map[string]any)
		switch strings.ToLower(stringValue(delta["type"])) {
		case "thinking_delta":
			state.appendThinkingDelta(block, stringValue(delta["thinking"]))
		case "text_delta":
			state.appendTextDelta(block, stringValue(delta["text"]))
		case "input_json_delta":
			if block != nil {
				block.input.WriteString(stringValue(delta["partial_json"]))
			}
		case "signature_delta":
			// Grok thinking signatures are opaque; ignore for live projection.
		}
	case "content_block_stop":
		index := intFromAny(event["index"])
		block := state.blocks[index]
		if block == nil {
			return
		}
		switch block.kind {
		case "thinking":
			state.flushThinking(gs)
			state.closedThinking = append(state.closedThinking, block.streamed.String())
		case "text":
			state.flushText(gs)
			state.closedText = append(state.closedText, block.streamed.String())
		case "tool_use", "server_tool_use":
			state.flushThinking(gs)
			state.flushText(gs)
			input := block.input.String()
			if input == "" {
				input = block.inputSeed
			}
			state.emitToolUse(gs, block.id, block.name, input)
		case "web_search_tool_result":
			state.emitWebSearchResult(gs, block)
		}
		delete(state.blocks, index)
	case "message_delta":
		// Mid-message usage / stop_reason. Capture usage when present so a
		// later interrupted turn still has the best available counters.
		if usage, ok := event["usage"].(map[string]any); ok {
			state.captureUsage(usage)
		}
	case "message_stop":
		state.flushThinking(gs)
		state.flushText(gs)
	}
}

func (state *streamState) emitToolUse(gs *grokSession, id, name, input string) {
	if id != "" && state.emittedTools[id] {
		return
	}
	if id != "" {
		state.emittedTools[id] = true
		state.toolNames[id] = name
	}
	input = normalizeJSONPreview(input)
	gs.emit(core.Event{
		Type:      core.EventToolUse,
		ToolName:  name,
		ToolInput: truncate(input, maxToolPreview),
		RequestID: id,
	})
}

func (state *streamState) emitWebSearchResult(gs *grokSession, block *streamBlock) {
	encoded, err := json.Marshal(block.value)
	if err != nil {
		return
	}
	key := block.id
	if key == "" {
		if value, ok := block.value.(map[string]any); ok {
			key = stringValue(value["tool_use_id"])
		}
	}
	if key == "" {
		key = string(encoded)
	}
	if state.emittedResults[key] {
		return
	}
	state.emittedResults[key] = true
	gs.emit(core.Event{Type: core.EventToolResult, ToolName: "web_search", ToolResult: truncate(string(encoded), maxToolPreview)})
}

// handleAssistantComplete reconciles the full assistant message against the
// per-block stream already projected live.
//
// Authority:
//   - Live UI: stream_event deltas (already flushed on content_block_stop)
//   - Gap fill / no-partial path: assistant complete content[]
//   - Tools: emit any tool_use not seen in the stream (id-deduped)
//
// Grok 4.6 commonly emits [thinking, text, tool_use...] in one message. Each
// text/thinking block must reconcile against its own closed stream body, not
// against a global concatenation of every text delta in the message.
func (state *streamState) handleAssistantComplete(gs *grokSession, raw map[string]any) {
	message, _ := raw["message"].(map[string]any)
	if usage, ok := message["usage"].(map[string]any); ok {
		state.captureUsage(usage)
	}
	if model := strings.TrimSpace(stringValue(message["model"])); model != "" {
		state.currentModel = model
	}
	contents, _ := message["content"].([]any)
	thinkIdx := 0
	textIdx := 0
	for _, value := range contents {
		block, _ := value.(map[string]any)
		switch strings.ToLower(stringValue(block["type"])) {
		case "thinking":
			complete := stringValue(block["thinking"])
			streamed := ""
			if thinkIdx < len(state.closedThinking) {
				streamed = state.closedThinking[thinkIdx]
				thinkIdx++
			}
			if suffix := missingBlockSuffix(complete, streamed); suffix != "" {
				state.pendingThinking.WriteString(suffix)
			}
			state.flushThinking(gs)
			// If this block only appeared in the complete frame, record it so a
			// later identical re-delivery does not double-emit.
			if streamed == "" && complete != "" {
				state.closedThinking = append(state.closedThinking, complete)
				thinkIdx = len(state.closedThinking)
			}
		case "text":
			complete := stringValue(block["text"])
			streamed := ""
			if textIdx < len(state.closedText) {
				streamed = state.closedText[textIdx]
				textIdx++
			}
			if suffix := missingBlockSuffix(complete, streamed); suffix != "" {
				state.appendTextDelta(nil, suffix)
			}
			if state.runaway == "" {
				state.flushText(gs)
			}
			if streamed == "" && complete != "" {
				state.closedText = append(state.closedText, complete)
				textIdx = len(state.closedText)
			}
		case "tool_use", "server_tool_use":
			id := stringValue(block["id"])
			input, _ := json.Marshal(block["input"])
			state.emitToolUse(gs, id, stringValue(block["name"]), string(input))
		case "web_search_tool_result":
			state.emitWebSearchResult(gs, &streamBlock{id: stringValue(block["id"]), value: block})
		}
	}
}

func (state *streamState) captureUsage(usage map[string]any) {
	if usage == nil {
		return
	}
	state.lastInputTokens = intFromAny(usage["input_tokens"])
	state.lastCacheRead = intFromAny(usage["cache_read_input_tokens"])
	state.lastCacheCreate = intFromAny(usage["cache_creation_input_tokens"])
}

// missingBlockSuffix returns the unstreamed suffix of one complete content
// block relative to that same block's streamed body.
func missingBlockSuffix(complete, streamed string) string {
	if complete == "" || complete == streamed {
		return ""
	}
	if streamed == "" {
		return complete
	}
	if strings.HasPrefix(complete, streamed) {
		return strings.TrimPrefix(complete, streamed)
	}
	// Stream already projected a different body for this block index (rare
	// rewrite / truncation). Keep what was streamed live; do not warn as if
	// the whole message failed to parse.
	slog.Debug("grok: assistant complete block differs from streamed block; keeping stream",
		"complete_len", len(complete), "streamed_len", len(streamed))
	return ""
}

func (state *streamState) handleToolResults(gs *grokSession, raw map[string]any) {
	message, _ := raw["message"].(map[string]any)
	contents, _ := message["content"].([]any)
	for _, value := range contents {
		block, _ := value.(map[string]any)
		if strings.ToLower(stringValue(block["type"])) != "tool_result" {
			continue
		}
		id := stringValue(block["tool_use_id"])
		name := state.toolNames[id]
		if name == "" {
			name = id
		}
		result, exitCode, success := decodeToolResult(block["content"], boolValue(block["is_error"]))
		status := "completed"
		if success != nil && !*success {
			status = "failed"
		}
		gs.emit(core.Event{
			Type:         core.EventToolResult,
			ToolName:     name,
			ToolResult:   truncate(result, maxToolPreview),
			ToolStatus:   status,
			ToolExitCode: exitCode,
			ToolSuccess:  success,
			RequestID:    id,
		})
	}
}

func decodeToolResult(content any, markedError bool) (string, *int, *bool) {
	text := toolResultText(content)
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		success := !markedError
		return text, nil, &success
	}
	result := stringValue(payload["output_for_prompt"])
	if result == "" {
		result = stringValue(payload["rawOutput"])
	}
	if result == "" {
		result = stringValue(payload["raw_output"])
	}
	if result == "" {
		if output, ok := byteSlice(payload["output"]); ok {
			result = string(output)
		}
	}
	if result == "" {
		encoded, _ := json.Marshal(payload)
		result = string(encoded)
	}
	var exitCode *int
	if payload["exit_code"] != nil {
		value := intFromAny(payload["exit_code"])
		exitCode = &value
	} else if payload["exitCode"] != nil {
		value := intFromAny(payload["exitCode"])
		exitCode = &value
	}
	succeeded := !markedError
	if value, ok := payload["success"].(bool); ok {
		succeeded = value && !markedError
	}
	if exitCode != nil && *exitCode != 0 {
		succeeded = false
	}
	return result, exitCode, &succeeded
}

func toolResultText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text := stringValue(block["text"]); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func byteSlice(value any) ([]byte, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]byte, len(values))
	for i, value := range values {
		number := intFromAny(value)
		if number < 0 || number > 255 {
			return nil, false
		}
		result[i] = byte(number)
	}
	return result, true
}

func (state *streamState) flushThinking(gs *grokSession) {
	if state.pendingThinking.Len() == 0 {
		return
	}
	text := state.pendingThinking.String()
	state.pendingThinking.Reset()
	if strings.TrimSpace(text) != "" {
		gs.emit(core.Event{Type: core.EventThinking, Content: text, SessionID: gs.CurrentSessionID()})
	}
}

func (state *streamState) flushText(gs *grokSession) {
	if state.pendingText.Len() == 0 {
		return
	}
	text := state.pendingText.String()
	state.pendingText.Reset()
	if text != "" {
		state.finalText = text
		gs.emit(core.Event{Type: core.EventText, Content: text, SessionID: gs.CurrentSessionID()})
	}
}

func (state *streamState) appendThinkingDelta(block *streamBlock, text string) {
	if text == "" || state.runaway != "" {
		return
	}
	if block != nil {
		block.streamed.WriteString(text)
	}
	state.pendingThinking.WriteString(text)
}

func (state *streamState) appendTextDelta(block *streamBlock, text string) {
	if text == "" || state.runaway != "" {
		return
	}
	if block != nil {
		block.streamed.WriteString(text)
	}
	state.pendingText.WriteString(text)
	clipped, unit, n := clipRepeatedTail(state.pendingText.String())
	if n == 0 {
		return
	}
	// Clip live pending and the open block body so closedText matches emit.
	state.pendingText.Reset()
	state.pendingText.WriteString(clipped)
	if block != nil {
		blockClipped, _, bn := clipRepeatedTail(block.streamed.String())
		if bn > 0 {
			block.streamed.Reset()
			block.streamed.WriteString(blockClipped)
		}
	}
	state.runaway = fmt.Sprintf("repeated %q %d times", strings.TrimSpace(unit), n)
	slog.Warn("grok: aborted repeating model output", "unit", unit, "count", n)
}

func (gs *grokSession) finalizeRunaway(state *streamState) bool {
	state.flushThinking(gs)
	state.flushText(gs)
	note := "\n\n[已停止：模型陷入重复输出]"
	content := state.finalText + note
	state.finalText = content
	return gs.emit(core.Event{
		Type:      core.EventResult,
		Content:   content,
		SessionID: gs.CurrentSessionID(),
		Done:      true,
	})
}

func usefulRepeatUnit(unit []rune) bool {
	if len(unit) < minRepeatUnitRunes {
		return false
	}
	seen := make(map[rune]struct{}, len(unit))
	hasLetter := false
	for _, r := range unit {
		seen[r] = struct{}{}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
	}
	return hasLetter && len(seen) >= 2
}

func clipRepeatedTail(text string) (string, string, int) {
	runes := []rune(text)
	if len(runes) < minRepeatUnitRunes*minRepeatCount {
		return text, "", 0
	}
	window := minRepeatCount * maxRepeatUnitRunes * 2
	offset := 0
	if len(runes) > window {
		offset = len(runes) - window
		runes = runes[offset:]
	}
	maxUnit := maxRepeatUnitRunes
	if maxUnit > len(runes)/minRepeatCount {
		maxUnit = len(runes) / minRepeatCount
	}
	for unitLen := minRepeatUnitRunes; unitLen <= maxUnit; unitLen++ {
		unit := runes[len(runes)-unitLen:]
		if !usefulRepeatUnit(unit) {
			continue
		}
		count := 1
		for pos := len(runes) - 2*unitLen; pos >= 0; pos -= unitLen {
			match := true
			for i := 0; i < unitLen; i++ {
				if runes[pos+i] != unit[i] {
					match = false
					break
				}
			}
			if !match {
				break
			}
			count++
		}
		if count < minRepeatCount {
			continue
		}
		keep := offset + len(runes) - unitLen*(count-2)
		if keep < 0 {
			keep = 0
		}
		full := []rune(text)
		if keep > len(full) {
			keep = len(full)
		}
		return string(full[:keep]), string(unit), count
	}
	return text, "", 0
}

func normalizeJSONPreview(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return value
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return value
	}
	return string(encoded)
}

func resultIsError(raw map[string]any) bool {
	if boolValue(raw["is_error"]) {
		return true
	}
	subtype := strings.ToLower(stringValue(raw["subtype"]))
	return subtype != "" && subtype != "success"
}

func resultErrorMessage(raw map[string]any) string {
	for _, key := range []string{"message", "error", "result"} {
		if value := strings.TrimSpace(stringValue(raw[key])); value != "" {
			return value
		}
		if value, ok := raw[key].(map[string]any); ok {
			if message := strings.TrimSpace(stringValue(value["message"])); message != "" {
				return message
			}
		}
	}
	if values, ok := raw["errors"].([]any); ok {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			switch item := value.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				if message := stringValue(item["message"]); message != "" {
					parts = append(parts, message)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return "headless turn failed"
}

func resultMetadata(raw map[string]any) map[string]any {
	metadata := make(map[string]any)
	for _, key := range []string{"stop_reason", "num_turns", "duration_ms", "duration_api_ms", "total_cost_usd"} {
		if value, ok := raw[key]; ok {
			metadata[key] = value
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func applyUsage(event *core.Event, usage map[string]any) {
	if usage == nil {
		return
	}
	event.InputTokens = intFromAny(usage["input_tokens"])
	event.OutputTokens = intFromAny(usage["output_tokens"])
	event.CacheReadInputTokens = intFromAny(usage["cache_read_input_tokens"])
	event.CacheCreationInputTokens = intFromAny(usage["cache_creation_input_tokens"])
}

func (gs *grokSession) updateContextUsage(event core.Event, raw map[string]any, state *streamState) {
	contextWindow := 0
	if models, ok := raw["modelUsage"].(map[string]any); ok {
		for _, value := range models {
			model, _ := value.(map[string]any)
			if window := intFromAny(model["contextWindow"]); window > contextWindow {
				contextWindow = window
			}
		}
	}
	if contextWindow == 0 {
		contextWindow = grokModelContextWindow(gs.processEnv, gs.workDir, state.currentModel)
	}
	inputTokens := state.lastInputTokens
	cacheRead := state.lastCacheRead
	cacheCreate := state.lastCacheCreate
	if inputTokens == 0 && cacheRead == 0 && cacheCreate == 0 {
		inputTokens = event.InputTokens
		cacheRead = event.CacheReadInputTokens
		cacheCreate = event.CacheCreationInputTokens
	}
	used := inputTokens + cacheRead + cacheCreate
	usage := core.ContextUsage{
		UsedTokens:               used,
		TotalTokens:              used + event.OutputTokens,
		InputTokens:              inputTokens,
		CachedInputTokens:        cacheRead,
		CacheCreationInputTokens: cacheCreate,
		OutputTokens:             event.OutputTokens,
		ContextWindow:            contextWindow,
	}
	gs.usageMu.Lock()
	gs.lastUsage = usage
	gs.usageMu.Unlock()
}

func (gs *grokSession) GetContextUsage() *core.ContextUsage {
	gs.usageMu.RLock()
	defer gs.usageMu.RUnlock()
	if gs.lastUsage.TotalTokens == 0 && gs.lastUsage.ContextWindow == 0 {
		return nil
	}
	usage := gs.lastUsage
	return &usage
}

func intFromAny(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func stringValue(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	default:
		return ""
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func (gs *grokSession) emit(event core.Event) bool {
	gs.turnCancelMu.Lock()
	turnDone := gs.turnDone
	gs.turnCancelMu.Unlock()
	select {
	case gs.events <- event:
		return true
	case <-gs.ctx.Done():
		return false
	case <-turnDone:
		return false
	}
}

func (gs *grokSession) emitTerminal(ctx context.Context, event core.Event) {
	if gs.emit(event) {
		return
	}
	if err := ctx.Err(); err != nil {
		gs.emitStopped(core.Event{Type: core.EventError, Error: fmt.Errorf("grok: turn stopped: %w", err)})
	}
}

func (gs *grokSession) emitStopped(event core.Event) {
	select {
	case gs.events <- event:
		return
	case <-gs.ctx.Done():
		return
	default:
	}

	// A terminal event is part of the AgentSession contract. If backpressure
	// filled the channel, sacrifice one stale partial event so the consumer can
	// still observe that the cancelled turn ended.
	select {
	case <-gs.events:
		slog.Debug("grok: event channel full, dropped buffered event for stopped turn")
	default:
	}
	select {
	case gs.events <- event:
	case <-gs.ctx.Done():
	}
}

func (gs *grokSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return errors.New("grok: headless output is read-only; use yolo, auto, dont_ask, or plan mode")
}

func (gs *grokSession) CancelTurn() error {
	gs.turnCancelMu.Lock()
	cancel := gs.turnCancel
	gs.turnCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (gs *grokSession) Events() <-chan core.Event { return gs.events }

func (gs *grokSession) CurrentSessionID() string {
	value, _ := gs.sessionID.Load().(string)
	return value
}

func (gs *grokSession) Alive() bool { return gs.alive.Load() }

func (gs *grokSession) Close() error {
	gs.closeOnce.Do(func() {
		gs.alive.Store(false)
		gs.cancel()
		gs.turnCancelMu.Lock()
		if gs.turnCancel != nil {
			gs.turnCancel()
		}
		gs.turnCancelMu.Unlock()

		// Hold the lifecycle barrier through channel teardown. An active reader
		// calls wg.Done before releasing turnMu, so Wait cannot race with Add.
		gs.turnMu.Lock()
		gs.wg.Wait()
		close(gs.events)
		close(gs.closeDone)
		gs.turnMu.Unlock()
	})
	<-gs.closeDone
	return nil
}

type cappedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (writer *cappedWriter) Write(data []byte) (int, error) {
	original := len(data)
	remaining := writer.limit - writer.buf.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = writer.buf.Write(data)
	}
	return original, nil
}

func (writer *cappedWriter) String() string { return writer.buf.String() }

var ansiEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func stripANSI(value string) string { return ansiEscape.ReplaceAllString(value, "") }

func redactEnvSecrets(value string, env []string) string {
	redactedEnv := core.RedactEnv(env)
	for i, pair := range env {
		if i >= len(redactedEnv) || redactedEnv[i] == pair {
			continue
		}
		_, secret, ok := strings.Cut(pair, "=")
		if !ok || secret == "" {
			continue
		}
		value = core.RedactToken(value, secret)
	}
	return value
}

func truncate(value string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "..."
}

var (
	_ core.AgentSession          = (*grokSession)(nil)
	_ core.AgentSessionCanceller = (*grokSession)(nil)
	_ core.ContextUsageReporter  = (*grokSession)(nil)
)
