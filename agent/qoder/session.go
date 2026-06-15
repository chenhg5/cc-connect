package qoder

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
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// qoderSession manages a multi-turn Qoder conversation.
// Each Send() spawns `qodercli -p <prompt> -f stream-json -q`.
// Subsequent turns use `-r <sessionID>` to resume the conversation.
type qoderSession struct {
	workDir        string
	model          string
	mode           string
	extraEnv       []string
	events         chan core.Event
	sessionID      atomic.Value // stores string
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	alive          atomic.Bool
	startupWarning string

	textMu             sync.Mutex
	assistantTextByID  map[string]string
	assistantTextOrder []string
}

const maxAssistantTextCacheEntries = 1024

// StartupWarning implements core.StartupWarner. Returns a non-empty string
// when the session was started under degraded conditions (e.g. yolo mode
// silently skipped under root).
func (qs *qoderSession) StartupWarning() string { return qs.startupWarning }

func newQoderSession(ctx context.Context, workDir, model, mode, resumeID string, extraEnv []string) (*qoderSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	qs := &qoderSession{
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,

		assistantTextByID: make(map[string]string),
	}
	qs.alive.Store(true)

	// Detect root-induced yolo downgrade and surface it to the IM user via StartupWarning.
	// The actual flag skip happens inside Send() when building the CLI args.
	if mode == "yolo" && os.Geteuid() == 0 {
		qs.startupWarning = "⚠️ Running as root: --dangerously-skip-permissions (yolo mode) is not supported and has been skipped. The agent may pause on high-risk operations."
	}

	if resumeID != "" && resumeID != core.ContinueSession {
		qs.sessionID.Store(resumeID)
	}

	return qs, nil
}

func (qs *qoderSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(images) > 0 {
		slog.Warn("qoderSession: images not supported, ignoring")
	}
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(qs.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	if !qs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	args := []string{"-p", prompt, "-f", "stream-json", "-q", "-w", qs.workDir}

	sid := qs.CurrentSessionID()
	if sid != "" {
		args = append(args, "-r", sid)
	}

	if qs.mode == "yolo" {
		if os.Geteuid() == 0 {
			slog.Warn("qoderSession: --dangerously-skip-permissions not allowed under root, skipping flag")
		} else {
			args = append(args, "--dangerously-skip-permissions")
		}
	}

	if qs.model != "" {
		args = append(args, "--model", qs.model)
	}

	slog.Debug("qoderSession: launching", "resume", sid != "", "args_len", len(args))

	cmd := exec.CommandContext(qs.ctx, "qodercli", args...)
	cmd.Dir = qs.workDir
	if len(qs.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), qs.extraEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("qoderSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("qoderSession: start: %w", err)
	}

	qs.wg.Add(1)
	go qs.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (qs *qoderSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer qs.wg.Done()

	var gotResult bool
	var nonJSONLines []string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw streamEvent
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("qoderSession: non-JSON line", "line", truncStr(line, 100))
			nonJSONLines = append(nonJSONLines, line)
			continue
		}

		if raw.Type == "result" {
			gotResult = true
		}
		qs.handleEvent(&raw)
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		slog.Error("qoderSession: scanner error", "error", scanErr)
	}

	// Wait for process to exit.
	exitErr := cmd.Wait()

	// If we already got a result event, the turn completed normally.
	if gotResult {
		if exitErr != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Warn("qoderSession: process exited with error after result", "error", exitErr, "stderr", truncStr(stderrMsg, 200))
			}
		}
		return
	}

	// No result event was received — emit a fallback to prevent the engine
	// from hanging forever on the events channel.
	if len(nonJSONLines) > 0 {
		// qodercli produced plain text instead of stream-json; forward it
		// as a result so the user at least sees the response.
		slog.Warn("qoderSession: no result event, falling back to plain-text output", "lines", len(nonJSONLines))
		text := strings.Join(nonJSONLines, "\n")
		evt := core.Event{Type: core.EventResult, Content: text, SessionID: qs.CurrentSessionID(), Done: true}
		select {
		case qs.events <- evt:
		case <-qs.ctx.Done():
		}
	} else if exitErr != nil {
		// Process failed with no usable output.
		stderrMsg := strings.TrimSpace(stderrBuf.String())
		if stderrMsg == "" {
			stderrMsg = exitErr.Error()
		}
		slog.Error("qoderSession: process failed with no result", "error", exitErr, "stderr", truncStr(stderrMsg, 200))
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
		select {
		case qs.events <- evt:
		case <-qs.ctx.Done():
		}
	} else if scanErr != nil {
		// Scanner error with no output.
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", scanErr)}
		select {
		case qs.events <- evt:
		case <-qs.ctx.Done():
		}
	} else {
		// Process exited cleanly but produced nothing at all.
		slog.Warn("qoderSession: process exited with no output and no result event")
		evt := core.Event{Type: core.EventResult, Content: "", SessionID: qs.CurrentSessionID(), Done: true}
		select {
		case qs.events <- evt:
		case <-qs.ctx.Done():
		}
	}
}

// ── stream-json event structures ─────────────────────────────

type streamEvent struct {
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype"`
	SessionID string         `json:"session_id"`
	Done      bool           `json:"done"`
	Message   *streamMessage `json:"message"`
	Result    string         `json:"result"` // qodercli 0.2.x: final text in top-level result field
}

type streamMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Status     string          `json:"status"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
}

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Name     string `json:"name"`
	Input    string `json:"input"`
	Reason   string `json:"reason"`
	Content  string `json:"content"`
	Finished bool   `json:"finished"`
}

// ── event handling ───────────────────────────────────────────

func (qs *qoderSession) handleEvent(ev *streamEvent) {
	if ev.SessionID != "" {
		qs.sessionID.Store(ev.SessionID)
	}

	switch ev.Type {
	case "system":
		slog.Debug("qoderSession: init", "session_id", ev.SessionID)

	case "assistant":
		qs.handleAssistant(ev)

	case "result":
		qs.handleResult(ev)
	}
}

func (qs *qoderSession) handleAssistant(ev *streamEvent) {
	if ev.Message == nil {
		return
	}

	var items []contentItem
	if err := json.Unmarshal(ev.Message.Content, &items); err != nil {
		return
	}

	// qodercli <0.2 uses Status="finished" for final messages.
	// Newer stream-json output can reuse the same message id for thinking,
	// redacted thinking, partial text, and final text frames.
	isFinished := ev.Message.Status == "finished" ||
		ev.Message.StopReason == "end_turn" ||
		ev.Message.StopReason == "tool_use"

	for _, item := range items {
		switch item.Type {
		case "text":
			if !qs.emitAssistantText(ev.Message.ID, item.Text) {
				return
			}

		case "function":
			if !isFinished {
				continue
			}
			inputPreview := extractToolPreview(item.Input)
			evt := core.Event{Type: core.EventToolUse, ToolName: item.Name, ToolInput: inputPreview}
			select {
			case qs.events <- evt:
			case <-qs.ctx.Done():
				return
			}
		}
	}
}

func (qs *qoderSession) emitAssistantText(messageID, text string) bool {
	if text == "" {
		return true
	}

	chunk := text
	if messageID != "" {
		qs.textMu.Lock()
		if qs.assistantTextByID == nil {
			qs.assistantTextByID = make(map[string]string)
		}
		prev := qs.assistantTextByID[messageID]
		switch {
		case text == prev:
			qs.textMu.Unlock()
			return true
		case prev != "" && strings.HasPrefix(text, prev):
			chunk = strings.TrimPrefix(text, prev)
			qs.setAssistantTextLocked(messageID, text)
		case prev != "":
			// qoder occasionally sends a standalone fragment after cumulative
			// frames; emit only the new fragment and remember the joined text.
			qs.setAssistantTextLocked(messageID, prev+text)
		default:
			qs.setAssistantTextLocked(messageID, text)
		}
		qs.textMu.Unlock()
	}

	if chunk == "" {
		return true
	}
	evt := core.Event{Type: core.EventText, Content: chunk}
	select {
	case qs.events <- evt:
		return true
	case <-qs.ctx.Done():
		return false
	}
}

func (qs *qoderSession) setAssistantTextLocked(messageID, text string) {
	if _, ok := qs.assistantTextByID[messageID]; !ok {
		qs.assistantTextOrder = append(qs.assistantTextOrder, messageID)
		if len(qs.assistantTextOrder) > maxAssistantTextCacheEntries {
			evictID := qs.assistantTextOrder[0]
			copy(qs.assistantTextOrder, qs.assistantTextOrder[1:])
			qs.assistantTextOrder = qs.assistantTextOrder[:len(qs.assistantTextOrder)-1]
			delete(qs.assistantTextByID, evictID)
		}
	}
	qs.assistantTextByID[messageID] = text
}

func (qs *qoderSession) handleResult(ev *streamEvent) {
	var finalText string

	// qodercli <0.2: result text is in message.content[].text
	if ev.Message != nil {
		var items []contentItem
		if err := json.Unmarshal(ev.Message.Content, &items); err == nil {
			for _, item := range items {
				if item.Type == "text" && item.Text != "" {
					finalText = item.Text
				}
			}
		}
	}

	// qodercli 0.2.x: result text is in top-level "result" field
	if finalText == "" && ev.Result != "" {
		finalText = ev.Result
	}

	evt := core.Event{Type: core.EventResult, Content: finalText, SessionID: qs.CurrentSessionID(), Done: true}
	select {
	case qs.events <- evt:
	case <-qs.ctx.Done():
		return
	}
}

func (qs *qoderSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (qs *qoderSession) Events() <-chan core.Event {
	return qs.events
}

func (qs *qoderSession) CurrentSessionID() string {
	v, _ := qs.sessionID.Load().(string)
	return v
}

func (qs *qoderSession) Alive() bool {
	return qs.alive.Load()
}

func (qs *qoderSession) Close() error {
	qs.alive.Store(false)
	qs.cancel()
	done := make(chan struct{})
	go func() {
		qs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(qs.events)
	case <-time.After(8 * time.Second):
		slog.Warn("qoderSession: close timed out, abandoning wg.Wait")
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────

// extractToolPreview parses the JSON input of a tool call and returns a short preview string.
func extractToolPreview(inputJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &m); err != nil {
		return inputJSON
	}
	if cmd, ok := m["command"].(string); ok {
		return cmd
	}
	if file, ok := m["file_path"].(string); ok {
		return file
	}
	if pattern, ok := m["pattern"].(string); ok {
		return pattern
	}
	if query, ok := m["query"].(string); ok {
		return query
	}
	return inputJSON
}

func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
