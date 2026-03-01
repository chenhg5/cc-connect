package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// codexSession manages a multi-turn Codex conversation.
// First Send() uses `codex exec`, subsequent ones use `codex exec resume <threadID>`.
type codexSession struct {
	workDir  string
	model    string
	mode     string
	events   chan core.Event
	threadID atomic.Value // stores string — Codex thread_id
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool
}

func newCodexSession(ctx context.Context, workDir, model, mode, resumeID string) (*codexSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &codexSession{
		workDir: workDir,
		model:   model,
		mode:    mode,
		events:  make(chan core.Event, 64),
		ctx:     sessionCtx,
		cancel:  cancel,
	}
	cs.alive.Store(true)

	if resumeID != "" {
		cs.threadID.Store(resumeID)
	}

	return cs, nil
}

// Send launches a codex subprocess.
// If a threadID exists (from a prior turn or resume), uses `codex exec resume <id> <prompt>`.
// Otherwise uses `codex exec <prompt>` to start a new conversation.
func (cs *codexSession) Send(prompt string) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	tid := cs.CurrentSessionID()
	isResume := tid != ""

	var args []string
	if isResume {
		args = []string{"exec", "resume", "--json", "--skip-git-repo-check"}
	} else {
		args = []string{"exec", "--json", "--skip-git-repo-check"}
	}

	switch cs.mode {
	case "auto-edit", "full-auto":
		args = append(args, "--full-auto")
	case "yolo":
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}

	if cs.model != "" {
		args = append(args, "--model", cs.model)
	}

	if isResume {
		args = append(args, tid, prompt)
	} else {
		args = append(args, "--cd", cs.workDir, prompt)
	}

	slog.Debug("codexSession: launching", "resume", isResume, "args", args)

	cmd := exec.CommandContext(cs.ctx, "codex", args...)
	cmd.Dir = cs.workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codexSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("codexSession: start: %w", err)
	}

	cs.wg.Add(1)
	go cs.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (cs *codexSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer cs.wg.Done()
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("codexSession: process failed", "error", err, "stderr", stderrMsg)
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
			slog.Debug("codexSession: non-JSON line", "line", line)
			continue
		}

		cs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("codexSession: scanner error", "error", err)
		cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
	}
}

func (cs *codexSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "thread.started":
		if tid, ok := raw["thread_id"].(string); ok {
			cs.threadID.Store(tid)
			slog.Debug("codexSession: thread started", "thread_id", tid)
		}

	case "turn.started":
		slog.Debug("codexSession: turn started")

	case "item.started":
		cs.handleItemStarted(raw)

	case "item.completed":
		cs.handleItemCompleted(raw)

	case "turn.completed":
		cs.events <- core.Event{
			Type:      core.EventResult,
			SessionID: cs.CurrentSessionID(),
			Done:      true,
		}

	case "error":
		msg, _ := raw["message"].(string)
		if strings.Contains(msg, "Reconnecting") || strings.Contains(msg, "Falling back") {
			slog.Debug("codexSession: transient error", "message", msg)
		} else {
			slog.Warn("codexSession: error event", "message", msg)
		}
	}
}

func (cs *codexSession) handleItemStarted(raw map[string]any) {
	item, ok := raw["item"].(map[string]any)
	if !ok {
		return
	}
	itemType, _ := item["type"].(string)

	if itemType == "command_execution" {
		command, _ := item["command"].(string)
		cs.events <- core.Event{
			Type:      core.EventToolUse,
			ToolName:  "Bash",
			ToolInput: truncate(command, 200),
		}
	}
}

func (cs *codexSession) handleItemCompleted(raw map[string]any) {
	item, ok := raw["item"].(map[string]any)
	if !ok {
		return
	}
	itemType, _ := item["type"].(string)

	switch itemType {
	case "reasoning":
		text, _ := item["text"].(string)
		if text != "" {
			cs.events <- core.Event{
				Type:    core.EventThinking,
				Content: text,
			}
		}

	case "agent_message":
		text, _ := item["text"].(string)
		if text != "" {
			cs.events <- core.Event{
				Type:    core.EventText,
				Content: text,
			}
		}

	case "command_execution":
		command, _ := item["command"].(string)
		status, _ := item["status"].(string)
		output, _ := item["aggregated_output"].(string)
		exitCode, _ := item["exit_code"].(float64)

		slog.Debug("codexSession: command completed",
			"command", truncate(command, 100),
			"status", status,
			"exit_code", int(exitCode),
			"output_len", len(output),
		)

	case "error":
		msg, _ := item["message"].(string)
		if msg != "" && !strings.Contains(msg, "Falling back") {
			slog.Warn("codexSession: item error", "message", msg)
		}
	}
}

// RespondPermission is a no-op for Codex — permissions are handled via CLI flags.
func (cs *codexSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (cs *codexSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *codexSession) CurrentSessionID() string {
	v, _ := cs.threadID.Load().(string)
	return v
}

func (cs *codexSession) Alive() bool {
	return cs.alive.Load()
}

func (cs *codexSession) Close() error {
	cs.alive.Store(false)
	cs.cancel()
	cs.wg.Wait()
	close(cs.events)
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
