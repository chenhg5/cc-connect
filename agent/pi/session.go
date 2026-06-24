package pi

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

// ── capped stderr writer ────────────────────────────────────

// cappedStderrWriter wraps a bytes.Buffer with a maximum size to prevent
// unbounded growth from stderr output in long-running RPC sessions.
// Writes beyond the cap are silently discarded.
type cappedStderrWriter struct {
	buf bytes.Buffer
}

const maxStderrSize = 64 * 1024

func (w *cappedStderrWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= maxStderrSize {
		return len(p), nil
	}
	n := maxStderrSize - w.buf.Len()
	if len(p) > n {
		p = p[:n]
	}
	return w.buf.Write(p)
}

func (w *cappedStderrWriter) String() string {
	return w.buf.String()
}

// piSession manages a multi-turn pi coding agent conversation.
// In "json" mode (default): each Send() spawns `pi --mode json` as a one-shot
// process. No extension_ui support, no permission forwarding.
// In "rpc" mode: spawns a persistent `pi --mode rpc` process with stdin/stdout
// JSONL protocol, supporting extension_ui_request/response for permissions.
type piSession struct {
	cmd       string
	extraArgs []string // extra args from cmd, prepended before pi args
	workDir   string
	model     string
	mode      string   // permission mode: "default" | "yolo"
	thinking  string
	rpc       bool     // true = persistent RPC process, false = one-shot json mode
	extraEnv  []string
	attachDir string
	events  chan core.Event
	sessionID atomic.Value
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup // tracks readLoopRPC goroutine (RPC mode only)
	sendWg  sync.WaitGroup // tracks in-flight Send() calls
	alive   atomic.Bool

	thinkingBuf   strings.Builder
	thinkingMu    sync.Mutex
	modelsCW      map[string]int // cached from ~/.pi/agent/models.json
	usageMu    sync.Mutex
	lastUsage  *core.ContextUsage

	// RPC-only fields (nil/zero when rpc=false)
	rpcCmd     *exec.Cmd
	rpcStdin   io.WriteCloser
	rpcStdinMu sync.Mutex
	stderrBuf  cappedStderrWriter
	rpcReady   chan struct{} // closed after first JSON line from Pi

	// Extension UI: maps Pi's extension_ui_request id -> cc-connect RequestID
	extPendingMu  sync.Mutex
	extPending    map[string]string // Pi ext_ui_id -> cc-conn RequestID
	extPendingRev map[string]string // cc-conn RequestID -> Pi ext_ui_id
	extMethod     map[string]string // cc-conn RequestID -> method ("confirm"|"input"|"select")
}

// ── Constructor ──────────────────────────────────────────────

func newPiSession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, thinking string, rpc bool, resumeID string, extraEnv []string) (*piSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &piSession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		thinking:  thinking,
		rpc:       rpc,
		extraEnv:  extraEnv,
		attachDir: filepath.Join(workDir, ".cc-connect", "attachments", fmt.Sprintf("pi_%d", time.Now().UnixNano())),
		events:    make(chan core.Event, 64),
		ctx:       ctx,
		cancel:    cancel,
		modelsCW:  loadModelsContextWindows(),
	}
	s.alive.Store(true)

	if rpc {
		s.rpcReady = make(chan struct{})
		s.extPending = make(map[string]string)
		s.extPendingRev = make(map[string]string)
		s.extMethod = make(map[string]string)

		if err := s.startRPC(resumeID); err != nil {
			cancel()
			return nil, fmt.Errorf("pi: start rpc: %w", err)
		}

		// Wait for first JSON line (indicates RPC loop is live)
		select {
		case <-s.rpcReady:
		case <-time.After(30 * time.Second):
			s.killRPC()
			cancel()
			return nil, fmt.Errorf("pi: rpc process did not become ready within 30s")
		case <-ctx.Done():
			s.killRPC()
			return nil, fmt.Errorf("pi: context cancelled while waiting for rpc ready")
		}
	} else if resumeID != "" && resumeID != core.ContinueSession {
		// JSON mode: set the session ID directly since we don't spawn a process
		// that would emit a session event.
		s.sessionID.Store(resumeID)
	}

	return s, nil
}

// ── RPC process helpers (rpc=true) ──────────────────────────

func (s *piSession) startRPC(resumeID string) error {
	args := append(append([]string{}, s.extraArgs...), "--mode", "rpc")
	if resumeID != "" {
		args = append(args, "--session-id", resumeID)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.thinking != "" {
		args = append(args, "--thinking", s.thinking)
	}

	slog.Debug("piSession: starting RPC", "cmd", s.cmd, "args", args)

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	s.rpcStdin = stdinPipe
	s.rpcCmd = cmd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = &s.stderrBuf

	prepareCmdForKill(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	s.wg.Add(1)
	go s.readLoopRPC(stdout)

	return nil
}

func (s *piSession) killRPC() {
	if s.rpcCmd != nil && s.rpcCmd.Process != nil {
		if err := forceKillCmd(s.rpcCmd); err != nil {
			slog.Warn("piSession: kill rpc process", "error", err)
		}
		_, _ = s.rpcCmd.Process.Wait()
	}
}

// readLoopRPC is the persistent RPC readLoop goroutine.
// One instance runs for the lifetime of the RPC process.
func (s *piSession) readLoopRPC(stdout io.ReadCloser) {
	defer s.wg.Done()
	defer stdout.Close()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("piSession: non-JSON line", "line", truncStr(line, 100))
			continue
		}

		s.handleEvent(raw)

		if firstLine {
			firstLine = false
			close(s.rpcReady)
		}
	}

	// Process exited — reap the child and signal the engine.
	// killRPC (now with Wait()) ensures the zombie is collected.
	s.killRPC()

	if err := scanner.Err(); err != nil {
		slog.Error("piSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
	}

	// Signal process death to the engine (unless Close() already did).
	// Following the claudecode finishReadLoop pattern: always set alive=false,
	// and emit EventError with the captured stderr when present.
	// All writes to s.events happen before the deferred wg.Done(), so
	// Close()'s wg.Wait() → close(s.events) is correctly ordered.
	stderrMsg := strings.TrimSpace(s.stderrBuf.String())
	if stderrMsg != "" {
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("pi: %s", stderrMsg)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
	}
	s.alive.Store(false)
}

// ── Send ─────────────────────────────────────────────────────

// Send writes a prompt to the Pi agent.
// In json mode (default): spawns a one-shot `pi --mode json` process.
// In rpc mode: writes a "prompt" command to the persistent RPC process stdin.
func (s *piSession) Send(msg string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.sendWg.Add(1)
	defer s.sendWg.Done()

	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	cleanAttachments(s.attachDir)

	var atFiles []string
	if len(images) > 0 {
		atFiles = append(atFiles, saveImagesToDisk(s.attachDir, images)...)
	}
	if len(files) > 0 {
		atFiles = append(atFiles, saveFilesToDisk(s.attachDir, files)...)
	}

	// Build the message with attachment contents embedded
	var promptText strings.Builder
	promptText.WriteString(msg)
	for _, f := range atFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			slog.Warn("piSession: cannot read attachment", "file", f, "error", err)
			continue
		}
		promptText.WriteString("\n\n--- " + filepath.Base(f) + " ---\n")
		promptText.Write(data)
	}

	if s.rpc {
		return s.sendRPC(promptText.String())
	}
	return s.sendJSON(promptText.String())
}

// sendJSON spawns `pi --mode json -p <prompt>` as a one-shot process,
// reads all output events, and sends them to the events channel.
func (s *piSession) sendJSON(prompt string) error {
	args := append(append([]string{}, s.extraArgs...), "--mode", "json", "-p", prompt)
	if sid := s.CurrentSessionID(); sid != "" {
		args = append(args, "--session-id", sid)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.thinking != "" {
		args = append(args, "--thinking", s.thinking)
	}

	slog.Debug("piSession: spawning json mode", "cmd", s.cmd, "sessionID", s.CurrentSessionID())

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Read events from process output
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("piSession: non-JSON line", "line", truncStr(line, 100))
			continue
		}
		s.handleEvent(raw)
	}

	err = cmd.Wait()
	if err != nil {
		slog.Error("piSession: process error", "cmd", s.cmd, "error", err, "stderr", stderrBuf.String())
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("pi: %s: %w", strings.TrimSpace(stderrBuf.String()), err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
	}

	// Signal turn completion
	sid := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}

	return nil
}

// sendRPC writes a JSON "prompt" command to the persistent RPC process stdin.
// Events are read asynchronously by readLoopRPC, including agent_end which
// triggers EventResult.
func (s *piSession) sendRPC(prompt string) error {
	cmd := map[string]any{
		"type":    "prompt",
		"message": prompt,
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("piSession: marshal prompt: %w", err)
	}
	b = append(b, '\n')

	slog.Debug("piSession: sending RPC prompt", "bytes", len(b))
	s.rpcStdinMu.Lock()
	_, err = s.rpcStdin.Write(b)
	s.rpcStdinMu.Unlock()
	if err != nil {
		return fmt.Errorf("piSession: write stdin: %w", err)
	}

	return nil
}

// ── Event Handling (shared by both modes) ───────────────────

func (s *piSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "session":
		if id, ok := raw["id"].(string); ok && id != "" {
			s.sessionID.Store(id)
		}

	case "message_update":
		s.handleMessageUpdate(raw)

	case "message_end":
		s.handleMessageEnd(raw)

	case "agent_end":
		s.handleAgentEnd(raw)
		if s.rpc {
			// RPC mode: agent_end marks turn completion; json mode relies
			// on process exit to emit EventResult.
			sid := s.CurrentSessionID()
			evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
			}
		}

	case "extension_ui_request":
		if s.rpc {
			s.handleExtensionUIRequest(raw)
		}

	// Informational — no events produced
	case "agent_start", "turn_start", "turn_end", "message_start", "extension_error":
	default:
		slog.Debug("piSession: unrecognized event type", "type", eventType, "raw", raw)
	}
}

func (s *piSession) handleExtensionUIRequest(raw map[string]any) {
	id, _ := raw["id"].(string)
	method, _ := raw["method"].(string)
	if id == "" {
		slog.Warn("piSession: extension_ui_request without id")
		return
	}

	switch method {
	case "confirm":
		s.forwardConfirm(id, raw)
	case "input":
		s.forwardInput(id, raw)
	case "select":
		s.forwardSelect(id, raw)
	default:
		slog.Debug("piSession: extension_ui_request (unhandled)", "method", method)
	}
}

func (s *piSession) forwardConfirm(id string, raw map[string]any) {
	title, _ := raw["title"].(string)
	message, _ := raw["message"].(string)

	requestID := fmt.Sprintf("pi_ext_%s", id)

	s.extPendingMu.Lock()
	s.extPending[id] = requestID
	s.extPendingRev[requestID] = id
	s.extMethod[requestID] = "confirm"
	s.extPendingMu.Unlock()

	questionText := title
	if message != "" {
		if questionText != "" {
			questionText = questionText + ": " + message
		} else {
			questionText = message
		}
	}

	evt := core.Event{
		Type:     core.EventPermissionRequest,
		RequestID: requestID,
		ToolName: "extension_confirm",
		ToolInput: fmt.Sprintf("%s: %s", title, truncStr(message, 200)),
		ToolInputRaw: map[string]any{
			"title":   title,
			"message": message,
			"method":  "confirm",
		},
		Questions: []core.UserQuestion{{
			Question: questionText,
			Header:   "Confirm",
			Options: []core.UserQuestionOption{
				{Label: "Yes"},
				{Label: "No"},
			},
			MultiSelect: false,
		}},
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		slog.Warn("piSession: context done while forwarding confirm")
	}
}

func (s *piSession) forwardInput(id string, raw map[string]any) {
	title, _ := raw["title"].(string)
	placeholder, _ := raw["placeholder"].(string)

	requestID := fmt.Sprintf("pi_ext_%s", id)

	s.extPendingMu.Lock()
	s.extPending[id] = requestID
	s.extPendingRev[requestID] = id
	s.extMethod[requestID] = "input"
	s.extPendingMu.Unlock()

	evt := core.Event{
		Type:     core.EventPermissionRequest,
		RequestID: requestID,
		ToolName: "extension_input",
		ToolInput: fmt.Sprintf("%s [%s]", title, placeholder),
		ToolInputRaw: map[string]any{
			"title":       title,
			"placeholder": placeholder,
			"method":      "input",
		},
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func (s *piSession) forwardSelect(id string, raw map[string]any) {
	title, _ := raw["title"].(string)
	options, _ := raw["options"].([]any)

	var opts []string
	for _, opt := range options {
		if o, ok := opt.(string); ok {
			opts = append(opts, o)
		}
	}

	// Convert raw string options to UserQuestionOption so cc-connect renders
	// them as a rich button card (AskUserQuestion flow) instead of a plain
	// permission prompt. Label carries the option's value verbatim so that
	// resolveAskQuestionAnswer → result.Message maps back to the same string
	// the extension passed in.
	userOpts := make([]core.UserQuestionOption, 0, len(opts))
	for _, o := range opts {
		userOpts = append(userOpts, core.UserQuestionOption{Label: o})
	}

	requestID := fmt.Sprintf("pi_ext_%s", id)

	s.extPendingMu.Lock()
	s.extPending[id] = requestID
	s.extPendingRev[requestID] = id
	s.extMethod[requestID] = "select"
	s.extPendingMu.Unlock()

	questionText := title
	if questionText == "" {
		questionText = "Select an option"
	}

	evt := core.Event{
		Type:     core.EventPermissionRequest,
		RequestID: requestID,
		ToolName: "extension_select",
		ToolInput: fmt.Sprintf("%s [%s]", title, strings.Join(opts, ", ")),
		ToolInputRaw: map[string]any{
			"title":   title,
			"options": opts,
			"method":  "select",
		},
		Questions: []core.UserQuestion{{
			Question:    questionText,
			Header:      "Select",
			Options:     userOpts,
			MultiSelect: false,
		}},
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// ── Message event handlers ──────────────────────────────────

func (s *piSession) handleMessageUpdate(raw map[string]any) {
	msg, _ := raw["assistantMessageEvent"].(map[string]any)
	if msg == nil {
		return
	}
	msgType, _ := msg["type"].(string)

	switch msgType {
	case "text_delta":
		delta, _ := msg["delta"].(string)
		if delta == "" {
			return
		}
		evt := core.Event{Type: core.EventText, Content: delta}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}

	case "thinking_delta":
		delta, _ := msg["delta"].(string)
		if delta == "" {
			return
		}
		s.thinkingMu.Lock()
		s.thinkingBuf.WriteString(delta)
		s.thinkingMu.Unlock()

	case "thinking_end":
		s.thinkingMu.Lock()
		if s.thinkingBuf.Len() == 0 {
			s.thinkingMu.Unlock()
			return
		}
		evt := core.Event{Type: core.EventThinking, Content: s.thinkingBuf.String()}
		s.thinkingBuf.Reset()
		s.thinkingMu.Unlock()
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}

	case "toolcall_end":
		s.emitToolFromMessage(msg)
	}
}

func (s *piSession) emitToolFromMessage(ame map[string]any) {
	msg, _ := ame["message"].(map[string]any)
	if msg == nil {
		msg, _ = ame["partial"].(map[string]any)
	}
	if msg == nil {
		return
	}

	content, _ := msg["content"].([]any)
	idx := 0
	if ci, ok := ame["contentIndex"].(float64); ok {
		idx = int(ci)
	}

	if idx >= 0 && idx < len(content) {
		item, _ := content[idx].(map[string]any)
		if item != nil {
			itemType, _ := item["type"].(string)
			if itemType == "toolCall" {
				name, _ := item["name"].(string)
				input := extractToolInput(item)
				evt := core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input}
				select {
				case s.events <- evt:
				case <-s.ctx.Done():
				}
			}
		}
	}
}

func (s *piSession) handleMessageEnd(raw map[string]any) {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return
	}
	role, _ := msg["role"].(string)

	switch role {
	case "toolResult":
		toolName, _ := msg["toolName"].(string)
		content, _ := msg["content"].([]any)
		var output string
		if len(content) > 0 {
			if item, ok := content[0].(map[string]any); ok {
				if text, ok := item["text"].(string); ok {
					output = text
				}
			}
		}
		if output == "" {
			output = extractToolResult(msg)
		}
		evt := core.Event{
			Type:     core.EventToolResult,
			ToolName: toolName,
			Content:  truncStr(output, 500),
		}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}

	case "assistant":
		if errMsg, ok := msg["errorMessage"].(string); ok && errMsg != "" {
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
			select {
			case s.events <- evt:
			case <-s.ctx.Done():
			}
		}
	}
}

func extractToolResult(msg map[string]any) string {
	content, _ := msg["content"].([]any)
	for _, c := range content {
		if item, ok := c.(map[string]any); ok {
			// Look for the "text" key first; fall back to any string field.
			if text, ok := item["text"].(string); ok && text != "" {
				return text
			}
			if output, ok := item["output"].(string); ok && output != "" {
				return output
			}
			if content, ok := item["content"].(string); ok && content != "" {
				return content
			}
		}
	}
	return ""
}

func (s *piSession) handleAgentEnd(raw map[string]any) {
	msgs, _ := raw["messages"].([]any)
	if len(msgs) == 0 {
		return
	}

	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		usageRaw, _ := msg["usage"].(map[string]any)
		if usageRaw == nil {
			continue
		}

		model, _ := msg["model"].(string)
		inputTokens, _ := usageRaw["input"].(float64)
		outputTokens, _ := usageRaw["output"].(float64)
		cacheReadTokens, _ := usageRaw["cacheRead"].(float64)
		cacheWriteTokens, _ := usageRaw["cacheWrite"].(float64)

		input := int(inputTokens)
		output := int(outputTokens)
		cr := int(cacheReadTokens)
		cw := int(cacheWriteTokens)

		used := input + cr + cw
		total := used + output
		var ctxWindow int
		if model != "" && s.modelsCW != nil {
			if cwVal, ok := s.modelsCW[model]; ok {
				ctxWindow = cwVal
			}
		}
		if ctxWindow == 0 {
			ctxWindow = 200_000
		}

		usage := &core.ContextUsage{
			UsedTokens:               used,
			TotalTokens:              total,
			InputTokens:              input,
			OutputTokens:             output,
			CachedInputTokens:        cr,
			CacheCreationInputTokens: cw,
			ContextWindow:            ctxWindow,
		}
		s.usageMu.Lock()
		s.lastUsage = usage
		s.usageMu.Unlock()
		return
	}
}

// lastAskQuestionAnswer extracts the most recently collected answer from
// UpdatedInput.answers (the shape produced by buildAskQuestionResponse).
// extension_select / extension_confirm ride the AskUserQuestion flow, so
// the user's choice ends up here rather than in result.Message.
// Returns "" if the structure is missing or empty.
func lastAskQuestionAnswer(updatedInput map[string]any) string {
	if updatedInput == nil {
		return ""
	}
	answers, _ := updatedInput["answers"].(map[string]any)
	if answers == nil {
		return ""
	}
	if len(answers) > 1 {
		slog.Warn("piSession: unexpected multiple answers in AskUserQuestion", "count", len(answers))
	}
	// answers is keyed by question text, typically containing 1 entry from
	// the AskUserQuestion flow. Return the first string value found.
	for _, v := range answers {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ── RespondPermission ───────────────────────────────────────

func (s *piSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !s.rpc {
		// JSON mode: no extension_ui support
		return nil
	}

	s.extPendingMu.Lock()
	extID, ok := s.extPendingRev[requestID]
	method := s.extMethod[requestID]
	if ok {
		delete(s.extPending, extID)
		delete(s.extPendingRev, requestID)
		delete(s.extMethod, requestID)
	}
	s.extPendingMu.Unlock()

	if !ok {
		slog.Warn("piSession: RespondPermission for unknown request", "requestID", requestID)
		return nil
	}

	var resp map[string]any
	switch method {
	case "input":
		resp = map[string]any{
			"type": "extension_ui_response",
			"id":   extID,
		}
		if result.Behavior == "allow" {
			resp["value"] = result.Message
		} else {
			resp["cancelled"] = true
		}
	case "select":
		resp = map[string]any{
			"type": "extension_ui_response",
			"id":   extID,
		}
		if result.Behavior == "allow" {
			// select goes through the AskUserQuestion flow, where the user's
			// choice lives in UpdatedInput.answers (built by
			// buildAskQuestionResponse). Map it back to the option's value.
			resp["value"] = lastAskQuestionAnswer(result.UpdatedInput)
		} else {
			resp["cancelled"] = true
		}
	case "confirm":
		// confirm also goes through the AskUserQuestion flow (Yes/No buttons).
		// Behavior=allow means the user picked; inspect the answer to decide
		// between confirmed=true and cancelled=true.
		ans := strings.ToLower(strings.TrimSpace(lastAskQuestionAnswer(result.UpdatedInput)))
		if result.Behavior == "allow" && (ans == "yes" || ans == "true" || ans == "ok" || ans == "confirm") {
			resp = map[string]any{
				"type":      "extension_ui_response",
				"id":        extID,
				"confirmed": true,
			}
		} else {
			resp = map[string]any{
				"type":      "extension_ui_response",
				"id":        extID,
				"cancelled": true,
			}
		}
	default:
		resp = map[string]any{
			"type":      "extension_ui_response",
			"id":        extID,
			"confirmed": result.Behavior == "allow",
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("piSession: marshal extension_ui_response: %w", err)
	}
	b = append(b, '\n')

	slog.Debug("piSession: sending extension_ui_response", "id", extID, "behavior", result.Behavior)
	s.rpcStdinMu.Lock()
	_, err = s.rpcStdin.Write(b)
	s.rpcStdinMu.Unlock()
	if err != nil {
		return fmt.Errorf("piSession: write extension_ui_response: %w", err)
	}

	return nil
}

// ── AgentSession interface ──────────────────────────────────

func (s *piSession) Events() <-chan core.Event {
	return s.events
}

func (s *piSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *piSession) Alive() bool {
	return s.alive.Load()
}

func (s *piSession) Close() error {
	s.alive.Store(false)

	// Cancel context to interrupt any in-flight Send() or readLoopRPC.
	s.cancel()

	if s.rpc {
		s.killRPC()
	}

	// Wait for all in-flight Send() calls to finish (json mode) or be
	// interrupted by ctx cancellation (both modes). Only then are we sure
	// no goroutine can still write to s.events.
	s.sendWg.Wait()
	s.wg.Wait()

	close(s.events)
	return nil
}

// ── ContextUsageReporter ─────────────────────────────────────

func (s *piSession) GetContextUsage() *core.ContextUsage {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.lastUsage == nil {
		return nil
	}
	u := *s.lastUsage
	return &u
}

// ── Attachment helpers ───────────────────────────────────────

func cleanAttachments(attachDir string) {
	if attachDir == "" {
		return
	}
	if err := os.RemoveAll(attachDir); err != nil {
		slog.Warn("piSession: failed to clean attachments dir", "dir", attachDir, "error", err)
	}
}

func saveImagesToDisk(attachDir string, images []core.ImageAttachment) []string {
	if len(images) == 0 {
		return nil
	}
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Error("piSession: failed to create attachments dir", "error", err)
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

		fname := img.FileName
		if fname == "" {
			fname = fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		} else {
			sane := sanitizePiAttachmentName(fname)
			if sane == "" {
				fname = fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
			} else {
				if !strings.HasSuffix(sane, ext) {
					sane = sane + ext
				}
				fname = sane
			}
		}

		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("piSession: save image failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	return paths
}

func saveFilesToDisk(attachDir string, files []core.FileAttachment) []string {
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Error("piSession: failed to create attachments dir", "error", err)
		return nil
	}
	var paths []string
	for i, f := range files {
		fname := sanitizePiAttachmentName(f.FileName)
		if fname == "" {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixMilli(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o644); err != nil {
			slog.Error("piSession: save file failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	return paths
}

func sanitizePiAttachmentName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// ── Utilities ────────────────────────────────────────────────

func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

func extractToolInput(item map[string]any) string {
	args, hasArgs := item["arguments"].(map[string]any)
	if !hasArgs {
		args = item
	}

	if desc, ok := args["description"].(string); ok && desc != "" {
		return desc
	}
	if cmd, ok := args["command"].(string); ok && cmd != "" {
		return cmd
	}
	if fp, ok := args["file_path"].(string); ok && fp != "" {
		return fp
	}
	if pattern, ok := args["pattern"].(string); ok && pattern != "" {
		return pattern
	}
	if query, ok := args["query"].(string); ok && query != "" {
		return query
	}

	b, _ := json.Marshal(args)
	s := truncStr(string(b), 200)
	if s == "{}" {
		return ""
	}
	return s
}
