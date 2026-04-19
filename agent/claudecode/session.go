package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// claudeSession manages a long-running Claude Code process using
// --input-format stream-json and --permission-prompt-tool stdio.
//
// In "auto" mode, permission requests are auto-approved internally
// (avoiding --dangerously-skip-permissions which fails under root).
type claudeSession struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdinMu         sync.Mutex
	events          chan core.Event
	sessionID       atomic.Value // stores string
	permissionMode  atomic.Value // stores string
	autoApprove     atomic.Bool
	acceptEditsOnly atomic.Bool
	dontAsk         atomic.Bool
	workDir         string
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	alive           atomic.Bool

	// gracefulStopTimeout is how long Close() waits for a clean exit
	// (stdin close → Stop hooks → process exit) before escalating to
	// SIGTERM and then SIGKILL. Default: 120s to match claude-mem's
	// Stop hook timeout. The wait ends as soon as the process exits,
	// so typical shutdowns take seconds, not the full timeout.
	gracefulStopTimeout time.Duration
}

func newClaudeSession(ctx context.Context, workDir, cliBin string, cliExtraArgs []string, cliArgsFlag string, model, effort, sessionID, mode string, allowedTools, disallowedTools []string, extraEnv []string, platformPrompt string, disableVerbose bool, spawnOpts core.SpawnOptions, maxContextTokens int) (*claudeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	// innerArgs are Claude Code CLI flags — when a wrapper is used with
	// cliArgsFlag these get bundled into a single passthrough string.
	// outerArgs are flags the wrapper itself understands (e.g. --model).
	innerArgs := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--permission-prompt-tool", "stdio",
	}
	if !disableVerbose {
		innerArgs = append(innerArgs, "--verbose")
	}

	if mode != "" && mode != "default" {
		innerArgs = append(innerArgs, "--permission-mode", mode)
	}
	switch sessionID {
	case "", core.ContinueSession:
		// Truly fresh session — no resume, no continue.
	default:
		// Resuming a known session ID — this is cc-connect's own session
		// from a previous connection, safe to resume directly.
		innerArgs = append(innerArgs, "--resume", sessionID)
	}
	if len(allowedTools) > 0 {
		innerArgs = append(innerArgs, "--allowedTools", strings.Join(allowedTools, ","))
	}
	if len(disallowedTools) > 0 {
		innerArgs = append(innerArgs, "--disallowedTools", strings.Join(disallowedTools, ","))
	}

	if sysPrompt := core.AgentSystemPrompt(); sysPrompt != "" {
		if platformPrompt != "" {
			sysPrompt += "\n## Formatting\n" + platformPrompt + "\n"
		}
		innerArgs = append(innerArgs, "--append-system-prompt", sysPrompt)
	}

	if effort != "" {
		innerArgs = append(innerArgs, "--effort", effort)
	}
	if maxContextTokens > 0 {
		innerArgs = append(innerArgs, "--max-context-tokens", strconv.Itoa(maxContextTokens))
	}
	
	// outerArgs are understood by both the wrapper and Claude CLI directly.
	var outerArgs []string
	if model != "" {
		outerArgs = append(outerArgs, "--model", model)
	}

	slog.Debug("claudeSession: starting", "innerArgs", core.RedactArgs(innerArgs), "outerArgs", core.RedactArgs(outerArgs), "dir", workDir, "mode", mode, "run_as_user", spawnOpts.RunAsUser)

	// Per-spawn defense in depth: if run_as_user is set, re-run the cheap
	// preflight (sudo still works + target still can't escalate) right
	// before we build the command. This catches sudoers being edited
	// between startup preflight and now.
	if spawnOpts.IsolationMode() {
		verifyCtx, verifyCancel := context.WithTimeout(sessionCtx, 10*time.Second)
		err := core.VerifyRunAsUserCheap(verifyCtx, core.ExecSudoRunner{}, spawnOpts.RunAsUser)
		verifyCancel()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("claudeSession: run_as_user spawn refused: %w", err)
		}
	}

	// Build final argument list.
	// When cliArgsFlag is set (e.g. "-a"), inner args are bundled into a
	// single passthrough string via that flag, while outer args (--model etc.)
	// are appended directly so the wrapper can also interpret them.
	// Args containing spaces/newlines are quoted so the wrapper's command-line
	// parser (e.g. splitCommandLine) keeps them as single tokens.
	// Result: my-cli code -t foo -a "--verbose --append-system-prompt 'long text'" --model x
	var allArgs []string
	if cliArgsFlag != "" {
		allArgs = append(allArgs, cliExtraArgs...)
		allArgs = append(allArgs, cliArgsFlag, shellJoinArgs(innerArgs))
		allArgs = append(allArgs, outerArgs...)
	} else {
		allArgs = append(allArgs, cliExtraArgs...)
		allArgs = append(allArgs, innerArgs...)
		allArgs = append(allArgs, outerArgs...)
	}
	cmd := core.BuildSpawnCommand(sessionCtx, spawnOpts, cliBin, allArgs...)
	cmd.Dir = workDir
	// Filter out CLAUDECODE env var to prevent "nested session" detection,
	// since cc-connect is a bridge, not a nested Claude Code session.
	env := filterEnv(os.Environ(), "CLAUDECODE")
	if len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	// When run_as_user is set, strip the supervisor's environment down to
	// the allowlist before passing it to sudo. sudo --preserve-env also
	// enforces this, but filtering here makes the cc-connect spawn argv
	// the single source of truth.
	env = core.FilterEnvForSpawn(env, spawnOpts)
	cmd.Env = env

	var providerEnvSnapshot []string
	for _, e := range env {
		for _, prefix := range []string{"ANTHROPIC_", "CLAUDE_", "AWS_", "NO_PROXY", "DISABLE_"} {
			if strings.HasPrefix(e, prefix) {
				providerEnvSnapshot = append(providerEnvSnapshot, e)
				break
			}
		}
	}
	slog.Debug("claudeSession: spawn details",
		"bin", cliBin,
		"allArgs", core.RedactArgs(allArgs),
		"model", model,
		"providerEnv", core.RedactEnv(providerEnvSnapshot))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("claudeSession: start: %w", err)
	}

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		events:              make(chan core.Event, 64),
		workDir:             workDir,
		ctx:                 sessionCtx,
		cancel:              cancel,
		done:                make(chan struct{}),
		gracefulStopTimeout: 120 * time.Second,
	}
	cs.setPermissionMode(mode)
	cs.sessionID.Store(sessionID)
	cs.alive.Store(true)

	go cs.readLoop(stdout, &stderrBuf)

	return cs, nil
}

func (cs *claudeSession) readLoop(stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	waitErrCh, waitDone := cs.startReadLoopWait(stdout)
	defer cs.finishReadLoop(waitErrCh, stderrBuf)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		cs.handleReadLoopLine(scanner.Text())
	}

	cs.handleReadLoopScanErr(scanner.Err(), waitDone)
}

func (cs *claudeSession) startReadLoopWait(stdout io.ReadCloser) (<-chan error, <-chan struct{}) {
	waitErrCh := make(chan error, 1)
	waitDone := make(chan struct{})

	go func() {
		waitErrCh <- cs.cmd.Wait()
		close(waitDone)
	}()

	go func() {
		select {
		case <-cs.ctx.Done():
			_ = stdout.Close()
			return
		case <-waitDone:
		}

		// Grace period: give scanner a brief window to drain any data the
		// agent wrote to the pipe buffer before exiting. If scanner finishes
		// on its own (pipe fully closed, no descendants holding it),
		// cs.done fires first and we skip the force-close entirely
		select {
		case <-cs.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
		_ = stdout.Close()
	}()

	return waitErrCh, waitDone
}

func (cs *claudeSession) finishReadLoop(waitErrCh <-chan error, stderrBuf *bytes.Buffer) {
	err := <-waitErrCh

	cs.alive.Store(false)
	if err != nil {
		stderrMsg := ""
		if stderrBuf != nil {
			stderrMsg = strings.TrimSpace(stderrBuf.String())
		}
		if stderrMsg != "" {
			slog.Error("claudeSession: process failed", "error", err, "stderr", stderrMsg)
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				// INVARIANT: readLoop must close cs.events and cs.done exactly once
				// on every termination path. Callers (engine event loop) rely on
				// these closures to observe session end.
			}
		}
	}
	close(cs.events)
	close(cs.done)
}

func (cs *claudeSession) handleReadLoopScanErr(err error, waitDone <-chan struct{}) {
	if err == nil {
		return
	}

	select {
	case <-cs.ctx.Done():
		return
	case <-waitDone:
		return
	default:
	}

	slog.Error("claudeSession: scanner error", "error", err)
	evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

func (cs *claudeSession) handleReadLoopLine(line string) {
	if line == "" {
		return
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		slog.Debug("claudeSession: non-JSON line", "line", line)
		return
	}

	eventType, _ := raw["type"].(string)
	slog.Debug("claudeSession: event", "type", eventType)

	switch eventType {
	case "system":
		cs.handleSystem(raw)
	case "assistant":
		cs.handleAssistant(raw)
	case "user":
		cs.handleUser(raw)
	case "result":
		cs.handleResult(raw)
	case "control_request":
		cs.handleControlRequest(raw)
	case "control_cancel_request":
		requestID, _ := raw["request_id"].(string)
		slog.Debug("claudeSession: permission cancelled", "request_id", requestID)
	}
}

func (cs *claudeSession) handleSystem(raw map[string]any) {
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
		evt := core.Event{Type: core.EventText, SessionID: sid}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *claudeSession) handleAssistant(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	contentArr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		switch contentType {
		case "tool_use":
			toolName, _ := item["name"].(string)
			if toolName == "AskUserQuestion" {
				continue
			}
			inputSummary := summarizeInput(toolName, item["input"])
			evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: inputSummary}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				return
			}
		case "thinking":
			if thinking, ok := item["thinking"].(string); ok && thinking != "" {
				evt := core.Event{Type: core.EventThinking, Content: thinking}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		case "text":
			if text, ok := item["text"].(string); ok && text != "" {
				evt := core.Event{Type: core.EventText, Content: text}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}
}

func (cs *claudeSession) handleUser(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	contentArr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		if contentType == "tool_result" {
			isError, _ := item["is_error"].(bool)
			if isError {
				result, _ := item["content"].(string)
				slog.Debug("claudeSession: tool error", "content", result)
			}
		}
	}
}

func (cs *claudeSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
	}

	var inputTokens, outputTokens int
	if usage, ok := raw["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(v)
		}
	}

	evt := core.Event{
		Type:         core.EventResult,
		Content:      content,
		SessionID:    cs.CurrentSessionID(),
		Done:         true,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

func (cs *claudeSession) handleControlRequest(raw map[string]any) {
	requestID, _ := raw["request_id"].(string)
	request, _ := raw["request"].(map[string]any)
	if request == nil {
		return
	}
	subtype, _ := request["subtype"].(string)
	if subtype != "can_use_tool" {
		slog.Debug("claudeSession: unknown control request subtype", "subtype", subtype)
		return
	}

	toolName, _ := request["tool_name"].(string)
	input, _ := request["input"].(map[string]any)

	if cs.autoApprove.Load() {
		slog.Debug("claudeSession: auto-approving", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: input,
		})
		return
	}
	if cs.dontAsk.Load() {
		slog.Debug("claudeSession: auto-denying", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior: "deny",
			Message:  "Permission mode is set to dontAsk.",
		})
		return
	}
	if cs.acceptEditsOnly.Load() && isClaudeEditTool(toolName) {
		slog.Debug("claudeSession: auto-approving edit tool", "request_id", requestID, "tool", toolName)
		_ = cs.RespondPermission(requestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: input,
		})
		return
	}

	slog.Info("claudeSession: permission request", "request_id", requestID, "tool", toolName)
	evt := core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     toolName,
		ToolInput:    summarizeInput(toolName, input),
		ToolInputRaw: input,
	}

	if toolName == "AskUserQuestion" {
		evt.Questions = parseUserQuestions(input)
	}

	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

// Send writes a user message (with optional images and files) to the Claude process stdin.
// Images are sent as base64 in the multimodal content array.
// Files are saved to local temp files and referenced in the text prompt
// so Claude Code can read them with its built-in tools.
func (cs *claudeSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	if len(images) == 0 && len(files) == 0 {
		return cs.writeJSON(map[string]any{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": prompt},
		})
	}

	attachDir := filepath.Join(cs.workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("claudeSession: mkdir attachments failed", "error", err, "path", attachDir)
	}

	var parts []map[string]any
	var savedPaths []string

	// Save and encode images
	for i, img := range images {
		ext := extFromMime(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("claudeSession: save image failed", "error", err)
			continue
		}
		savedPaths = append(savedPaths, fpath)
		slog.Debug("claudeSession: image saved", "path", fpath, "size", len(img.Data))

		mimeType := img.MimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		parts = append(parts, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mimeType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}

	// Save files to disk so Claude Code can read them
	filePaths := core.SaveFilesToDisk(cs.workDir, files)

	// Build text part: user prompt + file path references
	textPart := prompt
	if textPart == "" && len(filePaths) > 0 {
		textPart = "Please analyze the attached file(s)."
	} else if textPart == "" {
		textPart = "Please analyze the attached image(s)."
	}
	if len(savedPaths) > 0 {
		textPart += "\n\n(Images also saved locally: " + strings.Join(savedPaths, ", ") + ")"
	}
	if len(filePaths) > 0 {
		textPart += "\n\n(Files saved locally, please read them: " + strings.Join(filePaths, ", ") + ")"
	}
	parts = append(parts, map[string]any{"type": "text", "text": textPart})

	return cs.writeJSON(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": parts},
	})
}

func extFromMime(mime string) string {
	switch mime {
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

// RespondPermission writes a control_response to the Claude process stdin.
func (cs *claudeSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	var permResponse map[string]any
	if result.Behavior == "allow" {
		updatedInput := result.UpdatedInput
		if updatedInput == nil {
			updatedInput = make(map[string]any)
		}
		permResponse = map[string]any{
			"behavior":     "allow",
			"updatedInput": updatedInput,
		}
	} else {
		msg := result.Message
		if msg == "" {
			msg = "The user denied this tool use. Stop and wait for the user's instructions."
		}
		permResponse = map[string]any{
			"behavior": "deny",
			"message":  msg,
		}
	}

	controlResponse := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   permResponse,
		},
	}

	slog.Debug("claudeSession: permission response", "request_id", requestID, "behavior", result.Behavior)
	return cs.writeJSON(controlResponse)
}

func (cs *claudeSession) writeJSON(v any) error {
	cs.stdinMu.Lock()
	defer cs.stdinMu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := cs.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

func isClaudeEditTool(toolName string) bool {
	switch toolName {
	case "Edit", "Write", "NotebookEdit", "MultiEdit":
		return true
	default:
		return false
	}
}

func (cs *claudeSession) setPermissionMode(mode string) {
	cs.permissionMode.Store(mode)
	cs.autoApprove.Store(mode == "bypassPermissions")
	cs.acceptEditsOnly.Store(mode == "acceptEdits")
	cs.dontAsk.Store(mode == "dontAsk")
}

func (cs *claudeSession) SetLiveMode(mode string) bool {
	current, _ := cs.permissionMode.Load().(string)
	if mode == "auto" || mode == "plan" || current == "auto" || current == "plan" {
		return false
	}
	cs.setPermissionMode(mode)
	return true
}

func (cs *claudeSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *claudeSession) CurrentSessionID() string {
	v, _ := cs.sessionID.Load().(string)
	return v
}

func (cs *claudeSession) Alive() bool {
	return cs.alive.Load()
}

func (cs *claudeSession) Close() error {
	// Phase 1: Close stdin to signal EOF. Claude Code exits cleanly on
	// stdin close, running Stop hooks (e.g. claude-mem session summary).
	cs.stdinMu.Lock()
	_ = cs.stdin.Close()
	cs.stdinMu.Unlock()

	graceful := cs.gracefulStopTimeout
	if graceful <= 0 {
		graceful = 8 * time.Second // legacy fallback
	}

	select {
	case <-cs.done:
		slog.Info("claudeSession: exited cleanly after stdin close")
		return nil
	case <-time.After(graceful):
		slog.Warn("claudeSession: graceful stop timed out, sending SIGTERM",
			"timeout", graceful)
	}

	// Phase 2: SIGTERM — gives the process a second chance to run
	// cleanup handlers that respond to signals but not stdin EOF.
	if cs.cmd != nil && cs.cmd.Process != nil {
		_ = cs.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-cs.done:
		slog.Info("claudeSession: exited after SIGTERM")
		return nil
	case <-time.After(5 * time.Second):
		slog.Warn("claudeSession: SIGTERM timed out, sending SIGKILL")
	}

	// Phase 3: SIGKILL — last resort.
	cs.cancel()
	if cs.cmd != nil && cs.cmd.Process != nil {
		_ = cs.cmd.Process.Kill()
	}
	<-cs.done
	return nil
}

// shellJoinArgs joins args into a single string, quoting any arg that
// contains whitespace so that a shell-style splitter (like my_cli's
// splitCommandLine) preserves each arg as one token.
//
// Uses single quotes because some splitters (e.g. my_cli) don't support
// backslash escapes inside double quotes. For values containing single
// quotes, we close the single-quoted segment, add an escaped single
// quote, and reopen: 'it'\''s' → it's
func shellJoinArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if !strings.ContainsAny(a, " \t\n\r'\"\\") {
			b.WriteString(a)
			continue
		}
		b.WriteByte('\'')
		for _, c := range a {
			if c == '\'' {
				b.WriteString("'\\''")
			} else {
				b.WriteRune(c)
			}
		}
		b.WriteByte('\'')
	}
	return b.String()
}

// filterEnv returns a copy of env with entries matching the given key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
