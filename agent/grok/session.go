package grok

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

// sessionConfig holds StartSession snapshot values.
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

// grokSession runs multi-turn chats via headless `grok -p` processes.
// Each Send() launches one process; conversation continuity uses --resume.
//
// Grok's streaming-json emits thought/text as *token deltas* (often one word
// or even one character per line). We coalesce those into whole EventThinking /
// EventText payloads so the engine does not spam the IM platform with one
// bubble per token (the WeChat "☁️ The / user / sent / …" failure mode).
type grokSession struct {
	cmd             string
	extraArgs       []string
	workDir         string
	model           string
	mode            string
	timeout         time.Duration
	extraEnv        []string
	reasoningEffort string
	maxTurns        int

	events    chan core.Event
	sessionID atomic.Value // string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool

	// sendMu serializes Send() so two turns never race on the same session.
	sendMu sync.Mutex

	// Stream coalescing (owned by the single readLoop goroutine per turn).
	pendingThought strings.Builder
	pendingText    strings.Builder
}

func newGrokSession(ctx context.Context, cfg sessionConfig) (*grokSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	gs := &grokSession{
		cmd:             cfg.cmd,
		extraArgs:       append([]string{}, cfg.extraArgs...),
		workDir:         cfg.workDir,
		model:           cfg.model,
		mode:            cfg.mode,
		timeout:         cfg.timeout,
		extraEnv:        append([]string{}, cfg.extraEnv...),
		reasoningEffort: cfg.reasoningEffort,
		maxTurns:        cfg.maxTurns,
		events:          make(chan core.Event, 64),
		ctx:             sessionCtx,
		cancel:          cancel,
	}
	gs.alive.Store(true)
	if cfg.resumeID != "" && cfg.resumeID != core.ContinueSession {
		gs.sessionID.Store(cfg.resumeID)
	}
	return gs, nil
}

// buildArgs constructs the CLI argv for one headless turn.
// promptFile, when non-empty, is passed via --prompt-file (preferred for
// multi-line prompts). Otherwise -p receives promptInline.
func (gs *grokSession) buildArgs(promptInline, promptFile string) []string {
	args := append([]string{}, gs.extraArgs...)
	args = append(args, "--output-format", "streaming-json")

	// Working directory — prefer explicit --cwd so resume land in the right project.
	if gs.workDir != "" {
		args = append(args, "--cwd", gs.workDir)
	}

	// Permission / approval. Headless has no TUI to click "allow", so non-plan
	// modes always pass --always-approve to avoid hanging the IM turn.
	perm := permissionModeFlag(gs.mode)
	args = append(args, "--permission-mode", perm)
	if gs.mode != "plan" {
		args = append(args, "--always-approve")
	}

	if sid := gs.CurrentSessionID(); sid != "" {
		args = append(args, "--resume", sid)
	}
	if gs.model != "" {
		args = append(args, "-m", gs.model)
	}
	if gs.reasoningEffort != "" {
		args = append(args, "--reasoning-effort", gs.reasoningEffort)
	}
	if gs.maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", gs.maxTurns))
	}

	if promptFile != "" {
		args = append(args, "--prompt-file", promptFile)
	} else {
		args = append(args, "-p", promptInline)
	}
	return args
}

func (gs *grokSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !gs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	gs.sendMu.Lock()
	// Unlock is deferred in the goroutine after the process finishes so
	// concurrent Send() calls queue rather than overlapping processes.
	// We unlock early only on pre-start failure.

	// Persist attachments so Grok tools can open them by path.
	attachDir := filepath.Join(gs.workDir, ".cc-connect", "attachments")
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
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Warn("grokSession: failed to save image", "error", err)
			continue
		}
		imageRefs = append(imageRefs, fpath)
	}

	var fileRefs []string
	for i, f := range files {
		fname := filepath.Base(f.FileName)
		if fname == "" || fname == "." || fname == ".." {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixMilli(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o644); err != nil {
			slog.Warn("grokSession: failed to save file", "error", err)
			continue
		}
		fileRefs = append(fileRefs, fpath)
	}

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

	// Prefer --prompt-file so multi-line / long prompts survive argv limits.
	var promptFile string
	var promptInline string
	tmp, err := os.CreateTemp("", "cc-connect-grok-prompt-*.txt")
	if err != nil {
		promptInline = fullPrompt
	} else {
		if _, werr := tmp.WriteString(fullPrompt); werr != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			promptInline = fullPrompt
		} else {
			tmp.Close()
			// World-readable is intentional: when run_as_user differs from the
			// supervisor user the agent still needs to read the prompt file.
			_ = os.Chmod(tmp.Name(), 0o644)
			promptFile = tmp.Name()
		}
	}

	args := gs.buildArgs(promptInline, promptFile)

	var cancel context.CancelFunc
	var ctx context.Context
	if gs.timeout > 0 {
		ctx, cancel = context.WithTimeout(gs.ctx, gs.timeout)
	} else {
		ctx, cancel = context.WithCancel(gs.ctx)
	}

	started := false
	defer func() {
		if !started {
			cancel()
			if promptFile != "" {
				os.Remove(promptFile)
			}
			gs.sendMu.Unlock()
		}
	}()

	slog.Debug("grokSession: launching",
		"resume", gs.CurrentSessionID() != "",
		"args", core.RedactArgs(args))

	cmd := exec.CommandContext(ctx, gs.cmd, args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = gs.workDir
	env := os.Environ()
	if len(gs.extraEnv) > 0 {
		env = core.MergeEnv(env, gs.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("grokSession: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("grokSession: start: %w", err)
	}

	started = true
	gs.wg.Add(1)
	go func() {
		defer gs.wg.Done()
		defer cancel()
		defer gs.sendMu.Unlock()
		defer func() {
			if promptFile != "" {
				os.Remove(promptFile)
			}
			for _, f := range append(imageRefs, fileRefs...) {
				os.Remove(f)
			}
		}()
		gs.readLoop(ctx, cmd, stdout, &stderrBuf)
	}()

	return nil
}

func (gs *grokSession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	go func() {
		<-ctx.Done()
		stdout.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var scanErr error
	var lastUsage map[string]any
	var sawEnd bool

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		slog.Debug("grokSession: raw", "line", truncate(line, 500))

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("grokSession: non-JSON line", "line", line)
			continue
		}
		if usage, ok := raw["usage"].(map[string]any); ok {
			lastUsage = usage
		}
		if gs.handleEvent(raw) {
			sawEnd = true
		}
	}
	scanErr = scanner.Err()
	waitErr := cmd.Wait()

	if scanErr != nil {
		slog.Error("grokSession: scanner error", "error", scanErr)
		gs.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", scanErr)})
	}

	if waitErr != nil && !sawEnd {
		stderrMsg := strings.TrimSpace(stderrBuf.String())
		if stderrMsg != "" {
			slog.Error("grokSession: process failed", "error", waitErr, "stderr", stderrMsg)
			gs.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)})
			return
		}
		// Process failed with empty stderr and no end event — still surface error.
		if ctx.Err() != nil {
			gs.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("grok turn canceled: %w", ctx.Err())})
			return
		}
		gs.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("grok process: %w", waitErr)})
		return
	}

	// Guarantee a terminal EventResult so the engine closes the turn even if
	// the CLI omitted the final {"type":"end"} line (e.g. truncated stream).
	if !sawEnd {
		gs.flushThought()
		gs.flushText()
		evt := core.Event{
			Type:      core.EventResult,
			SessionID: gs.CurrentSessionID(),
			Done:      true,
		}
		applyUsage(&evt, lastUsage)
		gs.emit(evt)
	}
}

// handleEvent maps one streaming-json object to core events.
// Returns true when this was a terminal "end" event.
func (gs *grokSession) handleEvent(raw map[string]any) bool {
	typ, _ := raw["type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))

	switch typ {
	case "thought", "thinking", "reasoning":
		// Token deltas — buffer only; flush when the stream leaves thinking.
		if text := eventDataString(raw); text != "" {
			gs.pendingThought.WriteString(text)
		}
		return false

	case "text", "message", "assistant", "agent_message", "agent_message_chunk":
		// Leaving the thought stream: emit one consolidated thinking event.
		gs.flushThought()
		if text := eventDataString(raw); text != "" {
			gs.pendingText.WriteString(text)
		}
		return false

	case "tool", "tool_use", "tool_call", "function_call":
		gs.flushThought()
		gs.flushText()
		name, input, id := extractTool(raw)
		if name != "" {
			gs.emit(core.Event{
				Type:      core.EventToolUse,
				ToolName:  name,
				ToolInput: truncate(input, 500),
				RequestID: id,
			})
		}
		return false

	case "tool_result", "tool_response", "function_result":
		gs.flushThought()
		gs.flushText()
		name, result, _ := extractToolResult(raw)
		if result != "" || name != "" {
			gs.emit(core.Event{
				Type:       core.EventToolResult,
				ToolName:   name,
				ToolResult: truncate(result, 500),
			})
		}
		return false

	case "error":
		gs.flushThought()
		gs.flushText()
		msg := eventDataString(raw)
		if msg == "" {
			if m, ok := raw["message"].(string); ok {
				msg = m
			}
		}
		if msg == "" {
			msg = "grok error"
		}
		gs.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg)})
		return false

	case "end", "result", "done", "complete":
		gs.flushThought()
		gs.flushText()
		if sid, ok := raw["sessionId"].(string); ok && sid != "" {
			gs.sessionID.Store(sid)
		} else if sid, ok := raw["session_id"].(string); ok && sid != "" {
			gs.sessionID.Store(sid)
		}
		evt := core.Event{
			Type:      core.EventResult,
			SessionID: gs.CurrentSessionID(),
			Done:      true,
		}
		if usage, ok := raw["usage"].(map[string]any); ok {
			applyUsage(&evt, usage)
		}
		// Some end events include a final text blob.
		if text := eventDataString(raw); text != "" {
			// Prefer streaming text already emitted; only use as Content for footer/fallback.
			evt.Content = text
		}
		gs.emit(evt)
		return true

	default:
		// Best-effort: nested content with type field, or data-only text chunks.
		if text := eventDataString(raw); text != "" && (raw["type"] == nil || typ == "") {
			gs.pendingText.WriteString(text)
		} else {
			slog.Debug("grokSession: unhandled event type", "type", typ)
		}
		return false
	}
}

func (gs *grokSession) flushThought() {
	if gs.pendingThought.Len() == 0 {
		return
	}
	text := gs.pendingThought.String()
	gs.pendingThought.Reset()
	if strings.TrimSpace(text) == "" {
		return
	}
	gs.emit(core.Event{Type: core.EventThinking, Content: text})
}

func (gs *grokSession) flushText() {
	if gs.pendingText.Len() == 0 {
		return
	}
	text := gs.pendingText.String()
	gs.pendingText.Reset()
	if text == "" {
		return
	}
	gs.emit(core.Event{Type: core.EventText, Content: text, SessionID: gs.CurrentSessionID()})
}

func eventDataString(raw map[string]any) string {
	// Primary shape: {"type":"text","data":"..."}
	switch v := raw["data"].(type) {
	case string:
		return v
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
		if t, ok := v["content"].(string); ok {
			return t
		}
	}
	if t, ok := raw["text"].(string); ok {
		return t
	}
	if t, ok := raw["content"].(string); ok {
		return t
	}
	if t, ok := raw["delta"].(string); ok {
		return t
	}
	return ""
}

func extractTool(raw map[string]any) (name, input, id string) {
	if n, ok := raw["name"].(string); ok {
		name = n
	}
	if n, ok := raw["tool"].(string); ok && name == "" {
		name = n
	}
	if n, ok := raw["toolName"].(string); ok && name == "" {
		name = n
	}
	if idv, ok := raw["id"].(string); ok {
		id = idv
	}
	if idv, ok := raw["toolCallId"].(string); ok && id == "" {
		id = idv
	}
	if idv, ok := raw["tool_call_id"].(string); ok && id == "" {
		id = idv
	}

	// Nested tool_call / function shapes.
	if name == "" {
		if tc, ok := raw["tool_call"].(map[string]any); ok {
			name, input, id = extractTool(tc)
			return
		}
		if fn, ok := raw["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				name = n
			}
			switch a := fn["arguments"].(type) {
			case string:
				input = a
			case map[string]any:
				b, _ := json.Marshal(a)
				input = string(b)
			}
		}
	}

	if input == "" {
		switch a := raw["input"].(type) {
		case string:
			input = a
		case map[string]any:
			b, _ := json.Marshal(a)
			input = string(b)
		}
	}
	if input == "" {
		switch a := raw["arguments"].(type) {
		case string:
			input = a
		case map[string]any:
			b, _ := json.Marshal(a)
			input = string(b)
		}
	}
	if input == "" {
		if d := eventDataString(raw); d != "" && d != name {
			input = d
		}
	}
	return name, input, id
}

func extractToolResult(raw map[string]any) (name, result, id string) {
	if n, ok := raw["name"].(string); ok {
		name = n
	}
	if n, ok := raw["toolName"].(string); ok && name == "" {
		name = n
	}
	if idv, ok := raw["tool_call_id"].(string); ok {
		id = idv
	}
	if idv, ok := raw["toolCallId"].(string); ok && id == "" {
		id = idv
	}
	if id != "" && name == "" {
		name = id
	}
	result = eventDataString(raw)
	if result == "" {
		if o, ok := raw["output"].(string); ok {
			result = o
		}
	}
	if result == "" {
		if o, ok := raw["result"].(string); ok {
			result = o
		}
	}
	return name, result, id
}

func applyUsage(evt *core.Event, usage map[string]any) {
	if usage == nil {
		return
	}
	evt.InputTokens = intFromAny(usage["input_tokens"])
	if evt.InputTokens == 0 {
		evt.InputTokens = intFromAny(usage["inputTokens"])
	}
	evt.OutputTokens = intFromAny(usage["output_tokens"])
	if evt.OutputTokens == 0 {
		evt.OutputTokens = intFromAny(usage["outputTokens"])
	}
	evt.CacheReadInputTokens = intFromAny(usage["cache_read_input_tokens"])
	if evt.CacheReadInputTokens == 0 {
		evt.CacheReadInputTokens = intFromAny(usage["cacheReadInputTokens"])
	}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func (gs *grokSession) emit(evt core.Event) {
	select {
	case gs.events <- evt:
	case <-gs.ctx.Done():
	}
}

// RespondPermission is a no-op: headless mode uses --always-approve (except plan).
func (gs *grokSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (gs *grokSession) Events() <-chan core.Event { return gs.events }

func (gs *grokSession) CurrentSessionID() string {
	v, _ := gs.sessionID.Load().(string)
	return v
}

func (gs *grokSession) Alive() bool { return gs.alive.Load() }

func (gs *grokSession) Close() error {
	gs.alive.Store(false)
	gs.cancel()
	done := make(chan struct{})
	go func() {
		gs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(gs.events)
	case <-time.After(8 * time.Second):
		slog.Warn("grokSession: close timed out, abandoning wg.Wait")
	}
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

var _ core.AgentSession = (*grokSession)(nil)
