package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("claudecode", New)
}

// Agent drives the Claude Code CLI in one of two modes:
//   - "auto":        uses --dangerously-skip-permissions (all tools auto-approved)
//   - "interactive":  respects tool permissions; optionally scoped via --allowedTools
type Agent struct {
	workDir      string
	model        string
	mode         string   // "auto" | "interactive"
	allowedTools []string // only used in interactive mode
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	if mode == "" {
		mode = "interactive"
	}

	var allowedTools []string
	if tools, ok := opts["allowed_tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok {
				allowedTools = append(allowedTools, s)
			}
		}
	}

	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claudecode: 'claude' CLI not found in PATH, please install Claude Code first")
	}

	return &Agent{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		allowedTools: allowedTools,
	}, nil
}

func (a *Agent) Name() string { return "claudecode" }

func (a *Agent) Execute(ctx context.Context, sessionID string, prompt string) (<-chan core.Event, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}

	switch a.mode {
	case "auto":
		args = append(args, "--dangerously-skip-permissions")
	default:
		if len(a.allowedTools) > 0 {
			args = append(args, "--allowedTools", strings.Join(a.allowedTools, ","))
		}
	}

	slog.Debug("claudecode: executing", "args", args, "dir", a.workDir)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = a.workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudecode: start: %w", err)
	}

	ch := make(chan core.Event, 16)

	go func() {
		defer close(ch)
		defer func() {
			if err := cmd.Wait(); err != nil {
				stderrMsg := strings.TrimSpace(stderrBuf.String())
				slog.Error("claudecode: process failed", "error", err, "stderr", stderrMsg)
				if stderrMsg != "" {
					ch <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				}
			}
		}()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		var lastContent string
		var detectedSessionID string

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}

			eventType, _ := raw["type"].(string)
			subType, _ := raw["subtype"].(string)

			switch eventType {
			case "system":
				if sid, ok := raw["session_id"].(string); ok {
					detectedSessionID = sid
					ch <- core.Event{Type: core.EventText, SessionID: sid}
				}

			case "assistant":
				switch subType {
				case "tool_use":
					name := strOr(raw, "name", "tool")
					input := summarizeInput(name, raw["input"])
					ch <- core.Event{
						Type:      core.EventToolUse,
						ToolName:  name,
						ToolInput: input,
					}
				default:
					if text, ok := raw["text"].(string); ok {
						lastContent += text
					}
				}

			case "result":
				if result, ok := raw["result"].(string); ok {
					lastContent = result
				}
				if sid, ok := raw["session_id"].(string); ok {
					detectedSessionID = sid
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- core.Event{Type: core.EventError, Error: fmt.Errorf("read output: %w", err)}
			return
		}

		ch <- core.Event{
			Type:      core.EventResult,
			Content:   lastContent,
			SessionID: detectedSessionID,
			Done:      true,
		}
	}()

	return ch, nil
}

func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claudecode: cannot determine home dir: %w", err)
	}

	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return nil, fmt.Errorf("claudecode: resolve work_dir: %w", err)
	}

	projectKey := strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-")
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectKey)

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudecode: read project dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(name, ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary, msgCount := scanSessionMeta(filepath.Join(projectDir, name))

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// scanSessionMeta reads a session JSONL and returns (firstUserMessage, messageCount).
// Only counts "user" and "assistant" type entries as messages.
func scanSessionMeta(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var summary string
	var count int

	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "user" || entry.Type == "assistant" {
			count++
			if summary == "" && entry.Type == "user" && entry.Message.Content != "" {
				s := entry.Message.Content
				if len(s) > 40 {
					s = s[:40] + "..."
				}
				summary = s
			}
		}
	}
	return summary, count
}

func (a *Agent) Stop() error { return nil }

// strOr returns the first non-empty string value found for the given keys.
func strOr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return "unknown"
}

// summarizeInput produces a short human-readable description of tool input.
func summarizeInput(tool string, input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}

	switch tool {
	case "Read", "Edit", "Write":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := m["command"].(string); ok {
			if len(cmd) > 80 {
				return cmd[:80] + "..."
			}
			return cmd
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
		if p, ok := m["glob_pattern"].(string); ok {
			return p
		}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 100 {
		return s[:100] + "..."
	}
	return s
}
