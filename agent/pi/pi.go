package pi

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

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("pi", New)
}

// Agent drives the pi coding agent CLI (`pi --mode json --no-input`).
type Agent struct {
	cmd        string // path to pi binary
	workDir    string
	model      string
	mode       string // "default" | "yolo"
	thinking   string // reasoning effort: off, minimal, low, medium, high, xhigh
	sessionEnv []string
	mu         sync.Mutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	cmd, _ := opts["cmd"].(string)
	if cmd == "" {
		cmd = "pi"
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("pi: '%s' not found in PATH, install with: npm install -g @mariozechner/pi-coding-agent", cmd)
	}

	return &Agent{
		cmd:     cmd,
		workDir: workDir,
		model:   model,
		mode:    mode,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "bypass", "auto-approve":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "pi" }
func (a *Agent) CLIBinaryName() string  { return "pi" }
func (a *Agent) CLIDisplayName() string { return "Pi" }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("pi: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	return nil // Pi uses its own model registry; no static list here.
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	mode := a.mode
	model := a.model
	extraEnv := append([]string{}, a.sessionEnv...)
	a.mu.Unlock()

	thinking := a.thinking
	return newPiSession(ctx, a.cmd, a.workDir, model, mode, thinking, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("pi: read session dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		sessionID, summary, msgCount := scanPiSession(filepath.Join(sessDir, name))
		if sessionID == "" {
			continue
		}

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

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return fmt.Errorf("pi: cannot determine session directory")
	}

	path := findSessionFile(sessDir, sessionID)
	if path == "" {
		return fmt.Errorf("pi: session %q not found", sessionID)
	}
	return os.Remove(path)
}

func (a *Agent) Stop() error { return nil }

// ── ModeSwitcher ─────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("pi: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard permissions", DescZh: "标准权限模式"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
	}
}

// ── MemoryFileProvider ───────────────────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".pi", "AGENTS.md")
}

// ── ReasoningEffortSwitcher ──────────────────────────────────

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.thinking = effort
	slog.Info("pi: thinking level changed", "level", effort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thinking
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh"}
}

// ── GetWorkDir (for /status display) ─────────────────────────

func (a *Agent) GetWorkDir() string { return a.workDir }

// ── HistoryProvider ──────────────────────────────────────────

func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return nil, nil
	}

	sessFile := findSessionFile(sessDir, sessionID)
	if sessFile == "" {
		return nil, nil
	}

	return readPiHistory(sessFile, limit)
}

// ── SkillProvider ────────────────────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".pi", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".pi", "skills"))
	}
	return dirs
}

// ── Session helpers ──────────────────────────────────────────

// findSessionFile locates the .jsonl file for a given session UUID in sessDir.
// Session files are named: <timestamp>_<uuid>.jsonl — this function extracts
// the UUID portion and matches exactly to avoid partial-match vulnerabilities.
func findSessionFile(sessDir, sessionID string) string {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Extract UUID: strip .jsonl, then take everything after the last "_".
		base := strings.TrimSuffix(name, ".jsonl")
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if base[idx+1:] == sessionID {
				return filepath.Join(sessDir, name)
			}
		}
	}
	return ""
}

// piSessionDir returns the pi session directory for the given workDir.
// Pi encodes the absolute path as: replace "/" with "-", wrap with "--".
// e.g. /home/user/project → --home-user-project--
func piSessionDir(workDir string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	encoded := "--" + strings.ReplaceAll(strings.TrimPrefix(absDir, "/"), "/", "-") + "--"
	return filepath.Join(homeDir, ".pi", "agent", "sessions", encoded)
}

// scanPiSession reads a pi session .jsonl file and extracts the session ID,
// a summary (first user message), and a message count.
func scanPiSession(path string) (sessionID, summary string, msgCount int) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry["type"] {
		case "session":
			if id, ok := entry["id"].(string); ok {
				sessionID = id
			}
		case "message":
			msg, _ := entry["message"].(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			if role == "user" || role == "assistant" {
				msgCount++
			}
			// Use first user message as summary.
			if role == "user" && summary == "" {
				content, _ := msg["content"].([]any)
				for _, c := range content {
					item, _ := c.(map[string]any)
					if item != nil {
						if text, ok := item["text"].(string); ok && text != "" {
							summary = text
							runes := []rune(summary)
							if len(runes) > 80 {
								summary = string(runes[:80]) + "..."
							}
							break
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("pi: scan session error", "path", path, "error", err)
	}
	return
}

// readPiHistory reads user/assistant messages from a pi session file.
func readPiHistory(path string, limit int) ([]core.HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	var all []core.HistoryEntry
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] != "message" {
			continue
		}
		msg, _ := entry["message"].(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}

		var text string
		content, _ := msg["content"].([]any)
		for _, c := range content {
			item, _ := c.(map[string]any)
			if item != nil {
				if t, ok := item["text"].(string); ok && t != "" {
					text = t
					break
				}
			}
		}
		if text == "" {
			continue
		}
		all = append(all, core.HistoryEntry{Role: role, Content: text})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("pi: read history: %w", err)
	}

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}
