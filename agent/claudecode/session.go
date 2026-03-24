package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
}

func newClaudeSession(ctx context.Context, workDir, model, sessionID, mode string, allowedTools, disallowedTools []string, extraEnv []string, platformPrompt string, disableVerbose bool) (*claudeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	claudeBin := resolveClaudeBinary()
	effectiveDisableVerbose := disableVerbose
	if filepath.Base(claudeBin) == "claude-orig" {
		// The native Claude CLI on this machine requires --verbose together with
		// stream-json in non-TTY/print mode. Keep verbose enabled for claude-orig;
		// the earlier router-specific suppression only applied to the wrapper path.
		effectiveDisableVerbose = false
	}

	args := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--permission-prompt-tool", "stdio",
	}
	if !effectiveDisableVerbose {
		args = append(args, "--verbose")
	}

	if mode != "" && mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	switch sessionID {
	case "":
		// Truly fresh session — no resume, no continue.
	case core.ContinueSession:
		// --continue grabs the most recent session in the workspace, which
		// may belong to an active CLI terminal. Fork it so the platform
		// conversation gets its own independent context branch.
		args = append(args, "--continue", "--fork-session")
	default:
		// Resuming a known session ID — this is cc-connect's own session
		// from a previous connection, safe to resume directly.
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowedTools, ","))
	}
	if len(disallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(disallowedTools, ","))
	}

	if sysPrompt := core.AgentSystemPrompt(); sysPrompt != "" {
		if platformPrompt != "" {
			sysPrompt += "\n## Formatting\n" + platformPrompt + "\n"
		}
		args = append(args, "--append-system-prompt", sysPrompt)
	}

	cmd := exec.CommandContext(sessionCtx, claudeBin, args...)
	cmd.Dir = workDir
	// Filter out Claude Code/session bridge env from the parent process so the
	// child CLI does not attach to an existing remote/service-backed session.
	env := filterEnv(os.Environ(), "CLAUDECODE")
	env = filterEnvPrefix(env, "CLAUDE_CODE_")
	claudeConfigDir := resolveClaudeConfigDir(workDir)
	if claudeConfigDir != "" {
		if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
			cancel()
			return nil, fmt.Errorf("claudeSession: create config dir: %w", err)
		}
		env = core.MergeEnv(env, []string{"CLAUDE_CONFIG_DIR=" + claudeConfigDir})
	}
	if len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	cmd.Env = env

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
		cmd:     cmd,
		stdin:   stdin,
		events:  make(chan core.Event, 64),
		workDir: workDir,
		ctx:     sessionCtx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	cs.setPermissionMode(mode)
	cs.sessionID.Store(sessionID)
	cs.alive.Store(true)

	go cs.readLoop(stdout, &stderrBuf)

	return cs, nil
}

func (cs *claudeSession) readLoop(stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	var (
		sawStructuredEvent bool
		plainTextLines     []string
	)

	defer func() {
		cs.alive.Store(false)
		err := cs.cmd.Wait()
		stderrMsg := strings.TrimSpace(stderrBuf.String())

		if err != nil && stderrMsg != "" {
			slog.Error("claudeSession: process failed", "error", err, "stderr", stderrMsg)
			evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				return
			}
		}

		if !sawStructuredEvent && len(plainTextLines) > 0 {
			content := strings.TrimSpace(strings.Join(plainTextLines, "\n"))
			if content != "" {
				evt := core.Event{Type: core.EventText, Content: content}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
		close(cs.events)
		close(cs.done)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			if maybeWorkspaceTrustPrompt(line) {
				err := fmt.Errorf("Claude Code is waiting for workspace trust confirmation for %s; open the workspace in claude CLI once and confirm trust, then retry", cs.workDir)
				slog.Error("claudeSession: workspace trust prompt blocked stream-json", "work_dir", cs.workDir)
				evt := core.Event{Type: core.EventError, Error: err}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
			plainTextLines = append(plainTextLines, line)
			continue
		}

		eventType, _ := raw["type"].(string)
		sawStructuredEvent = true

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
			_, _ = raw["request_id"].(string)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("claudeSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
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
		err := cs.writeJSON(map[string]any{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": prompt},
		})
		if err != nil {
			return err
		}
		return nil
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

	err := cs.writeJSON(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": parts},
	})
	if err != nil {
		return err
	}
	return nil
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
	cs.cancel()

	select {
	case <-cs.done:
		return nil
	case <-time.After(8 * time.Second):
		slog.Warn("claudeSession: graceful close timed out, killing process")
		if cs.cmd != nil && cs.cmd.Process != nil {
			_ = cs.cmd.Process.Kill()
		}
		<-cs.done
		return nil
	}
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

func filterEnvPrefix(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

var ansiControlSeq = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

func maybeWorkspaceTrustPrompt(line string) bool {
	clean := ansiControlSeq.ReplaceAllString(line, "")
	clean = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, clean)
	clean = strings.Join(strings.Fields(clean), " ")
	clean = strings.ToLower(clean)
	return strings.Contains(clean, "project you trust") ||
		strings.Contains(clean, "quick safety check") ||
		strings.Contains(clean, "yes, i trust this folder")
}

func resolveClaudeBinary() string {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return "claude"
	}
	origPath, err := exec.LookPath("claude-orig")
	if err != nil {
		return "claude"
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		return "claude"
	}
	content := string(data)
	if strings.Contains(content, "ccr code") {
		slog.Info("claudeSession: using claude-orig to bypass wrapper", "wrapper", claudePath, "resolved", origPath)
		return origPath
	}
	return "claude"
}

func resolveClaudeConfigDir(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(workDir))
	return filepath.Join(os.TempDir(), "cc-connect-claude", fmt.Sprintf("%08x", h.Sum32()))
}
