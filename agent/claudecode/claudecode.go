package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("claudecode", New)
}

// Agent drives Claude Code CLI using --input-format stream-json
// and --permission-prompt-tool stdio for bidirectional communication.
//
// Modes:
//   - "interactive": permission requests are forwarded to the user via IM
//   - "auto": permission requests are auto-approved in code (no --dangerously-skip-permissions needed)
type Agent struct {
	workDir      string
	model        string
	mode         string // "auto" | "interactive"
	allowedTools []string
	mu           sync.Mutex
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

// StartSession creates a persistent interactive Claude Code session.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	tools := make([]string, len(a.allowedTools))
	copy(tools, a.allowedTools)
	a.mu.Unlock()

	return newClaudeSession(ctx, a.workDir, a.model, sessionID, a.mode, tools)
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
				if utf8.RuneCountInString(s) > 40 {
					s = string([]rune(s)[:40]) + "..."
				}
				summary = s
			}
		}
	}
	return summary, count
}

func (a *Agent) Stop() error { return nil }

// AddAllowedTools adds tools to the pre-allowed list (takes effect on next session).
func (a *Agent) AddAllowedTools(tools ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	existing := make(map[string]bool)
	for _, t := range a.allowedTools {
		existing[t] = true
	}
	for _, tool := range tools {
		if !existing[tool] {
			a.allowedTools = append(a.allowedTools, tool)
			existing[tool] = true
		}
	}
	slog.Info("claudecode: updated allowed tools", "tools", tools, "total", len(a.allowedTools))
	return nil
}

// GetAllowedTools returns the current list of pre-allowed tools.
func (a *Agent) GetAllowedTools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.allowedTools))
	copy(result, a.allowedTools)
	return result
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
			if utf8.RuneCountInString(cmd) > 200 {
				return string([]rune(cmd)[:200]) + "..."
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
	if utf8.RuneCountInString(s) > 200 {
		return string([]rune(s)[:200]) + "..."
	}
	return s
}
