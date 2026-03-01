package cursor

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
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// cursorSession manages multi-turn conversations with the Cursor Agent CLI.
// Each Send() launches a new `agent --print` process with --resume for continuity.
type cursorSession struct {
	cmd      string // CLI binary name
	workDir  string
	model    string
	mode     string
	extraEnv []string
	events   chan core.Event
	chatID   atomic.Value // stores string — Cursor chat/session ID
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool
}

func newCursorSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*cursorSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &cursorSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	cs.alive.Store(true)

	if resumeID != "" {
		cs.chatID.Store(resumeID)
	}

	return cs, nil
}

func (cs *cursorSession) Send(prompt string, images []core.ImageAttachment) error {
	if len(images) > 0 {
		slog.Warn("cursorSession: images not yet supported in CLI mode, ignoring")
	}
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	chatID := cs.CurrentSessionID()
	isResume := chatID != ""

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--trust",
	}

	switch cs.mode {
	case "force":
		args = append(args, "--force")
	case "plan":
		args = append(args, "--mode", "plan")
	case "ask":
		args = append(args, "--mode", "ask")
	}

	if isResume {
		args = append(args, "--resume", chatID)
	}
	if cs.model != "" {
		args = append(args, "--model", cs.model)
	}
	args = append(args, "--workspace", cs.workDir, "--", prompt)

	slog.Debug("cursorSession: launching", "resume", isResume, "args", args)

	cmd := exec.CommandContext(cs.ctx, cs.cmd, args...)
	cmd.Dir = cs.workDir
	env := os.Environ()
	if len(cs.extraEnv) > 0 {
		env = append(env, cs.extraEnv...)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cursorSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cursorSession: start: %w", err)
	}

	cs.wg.Add(1)
	go cs.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (cs *cursorSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer cs.wg.Done()
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("cursorSession: process failed", "error", err, "stderr", stderrMsg)
				cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("cursorSession: non-JSON line", "line", line)
			continue
		}

		cs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("cursorSession: scanner error", "error", err)
		cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
	}
}

func (cs *cursorSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "system":
		cs.handleSystem(raw)

	case "user":
		// User echo — nothing to do

	case "thinking":
		cs.handleThinking(raw)

	case "assistant":
		cs.handleAssistant(raw)

	case "tool_call":
		cs.handleToolCall(raw)

	case "result":
		cs.handleResult(raw)

	default:
		slog.Debug("cursorSession: unhandled event", "type", eventType)
	}
}

func (cs *cursorSession) handleSystem(raw map[string]any) {
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
		slog.Debug("cursorSession: session init", "session_id", sid)

		model, _ := raw["model"].(string)
		cs.events <- core.Event{
			Type:      core.EventText,
			SessionID: sid,
			Content:   "", // init event, no content to display
			ToolName:  model,
		}
	}
}

func (cs *cursorSession) handleThinking(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	if subtype == "delta" {
		// Accumulate thinking deltas silently; we'll show them on "completed"
		return
	}
	// "completed" — we don't emit thinking content to the chat
	// (it's internal model reasoning, can be very verbose)
}

func (cs *cursorSession) handleAssistant(raw map[string]any) {
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
		if contentType == "text" {
			if text, ok := item["text"].(string); ok && text != "" {
				cs.events <- core.Event{
					Type:    core.EventText,
					Content: text,
				}
			}
		}
	}
}

func (cs *cursorSession) handleToolCall(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	tc, _ := raw["tool_call"].(map[string]any)
	if tc == nil {
		return
	}

	if subtype == "started" {
		name, input := extractToolInfo(tc)
		if name != "" {
			cs.events <- core.Event{
				Type:      core.EventToolUse,
				ToolName:  name,
				ToolInput: input,
			}
		}
	}
	// "completed" tool_call events contain results; we log but don't emit to chat
	if subtype == "completed" {
		name, _ := extractToolInfo(tc)
		slog.Debug("cursorSession: tool completed", "tool", name)
	}
}

// extractToolInfo parses the nested tool_call structure from Cursor's stream-json.
// Tool calls can be shellToolCall, readToolCall, editToolCall, etc.
func extractToolInfo(tc map[string]any) (name string, input string) {
	toolTypes := []struct {
		key      string
		toolName string
	}{
		{"shellToolCall", "Bash"},
		{"readToolCall", "Read"},
		{"editToolCall", "Edit"},
		{"writeToolCall", "Write"},
		{"listToolCall", "List"},
		{"searchToolCall", "Search"},
		{"grepToolCall", "Grep"},
		{"globToolCall", "Glob"},
	}

	for _, tt := range toolTypes {
		if call, ok := tc[tt.key].(map[string]any); ok {
			name = tt.toolName
			input = extractToolInput(name, call)
			return
		}
	}

	// Generic: try "description" field at top level
	if desc, ok := tc["description"].(string); ok && desc != "" {
		return "Tool", truncateStr(desc, 200)
	}

	return "", ""
}

func extractToolInput(toolName string, call map[string]any) string {
	args, _ := call["args"].(map[string]any)
	if args == nil {
		if desc, ok := call["description"].(string); ok {
			return truncateStr(desc, 200)
		}
		return ""
	}

	switch toolName {
	case "Bash":
		if cmd, ok := args["command"].(string); ok {
			return truncateStr(cmd, 200)
		}
	case "Read":
		if p, ok := args["path"].(string); ok {
			return p
		}
	case "Edit", "Write":
		if p, ok := args["path"].(string); ok {
			return p
		}
		if p, ok := args["filePath"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	}

	if desc, ok := call["description"].(string); ok && desc != "" {
		return truncateStr(desc, 200)
	}

	b, _ := json.Marshal(args)
	return truncateStr(string(b), 200)
}

func (cs *cursorSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
	}
	cs.events <- core.Event{
		Type:      core.EventResult,
		Content:   content,
		SessionID: cs.CurrentSessionID(),
		Done:      true,
	}
}

// RespondPermission is a no-op — Cursor Agent permissions are handled via --trust/--force flags.
func (cs *cursorSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (cs *cursorSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *cursorSession) CurrentSessionID() string {
	v, _ := cs.chatID.Load().(string)
	return v
}

func (cs *cursorSession) Alive() bool {
	return cs.alive.Load()
}

func (cs *cursorSession) Close() error {
	cs.alive.Store(false)
	cs.cancel()
	cs.wg.Wait()
	close(cs.events)
	return nil
}

func truncateStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
