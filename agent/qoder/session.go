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
	workDir   string
	model     string
	mode      string
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value // stores string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool
}

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
	}
	qs.alive.Store(true)

	if resumeID != "" {
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
		args = append(args, "--dangerously-skip-permissions")
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
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("qoderSession: process failed", "error", err, "stderr", truncStr(stderrMsg, 200))
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case qs.events <- evt:
				case <-qs.ctx.Done():
					return
				}
			}
		}
	}()

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
			continue
		}

		qs.handleEvent(&raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("qoderSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case qs.events <- evt:
		case <-qs.ctx.Done():
			return
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
}

type streamMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Status  string          `json:"status"`
	Content json.RawMessage `json:"content"`
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

	// Only process "finished" status to avoid duplicates from "tool_calling" status
	if ev.Message.Status != "finished" {
		return
	}

	var items []contentItem
	if err := json.Unmarshal(ev.Message.Content, &items); err != nil {
		return
	}

	for _, item := range items {
		switch item.Type {
		case "text":
			if item.Text != "" {
				evt := core.Event{Type: core.EventText, Content: item.Text}
				select {
				case qs.events <- evt:
				case <-qs.ctx.Done():
					return
				}
			}

		case "function":
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

func (qs *qoderSession) handleResult(ev *streamEvent) {
	var finalText string
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
