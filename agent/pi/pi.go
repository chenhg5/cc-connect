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

// Agent drives the pi coding agent CLI.
type Agent struct {
	cmd          string   // path to pi binary
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	workDir      string
	model        string
	mode         string // "default" | "yolo"
	thinking     string // reasoning effort: off, minimal, low, medium, high, xhigh
	rpc          bool   // true = --mode rpc (persistent, extension_ui); false = --mode json (one-shot, default)
	sessionEnv   []string
	mu           sync.Mutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	thinking, _ := opts["thinking"].(string)
	rpc, _ := opts["rpc"].(bool)

	cmd, extraArgs := core.ParseCmdOpts(opts, "pi")

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("pi: '%s' not found in PATH, install with: npm install -g @mariozechner/pi-coding-agent", cmd)
	}

	// If model not specified in opts, try defaultModel from settings.json
	if model == "" {
		if def, err := readDefaultModel(); err == nil && def != "" {
			model = def
		}
	}

	return &Agent{
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		workDir:      workDir,
		model:        model,
		mode:         mode,
		thinking:     thinking,
		rpc:          rpc,
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
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "Pi" }

// WorkspaceAgentOptions implements core.WorkspaceAgentOptionSnapshotter.
// It returns the user-configured options that must propagate to per-workspace
// agents reconstructed by the engine in multi-workspace mode. work_dir is
// intentionally omitted — the engine sets the target workspace. sessionEnv is
// also omitted (runtime-only). model and mode are copied by the engine via
// GetModel/GetMode, so we don't repeat them here.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	opts := map[string]any{}
	if a.cmd != "" && a.cmd != "pi" {
		opts["cmd"] = a.cmd
	}
	if a.rpc {
		opts["rpc"] = true
	}
	if a.thinking != "" {
		opts["thinking"] = a.thinking
	}
	return opts
}

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
	models, err := readSettingsModels()
	if err != nil {
		slog.Debug("pi: AvailableModels: read settings", "error", err)
		return nil
	}
	return models
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
	thinking := a.thinking
	extraArgs := append([]string{}, a.cliExtraArgs...)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	rpc := a.rpc
	a.mu.Unlock()
	return newPiSession(ctx, a.cmd, extraArgs, a.workDir, model, mode, thinking, rpc, sessionID, extraEnv)
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
	// Use PI_CODING_AGENT_DIR if set, otherwise default to ~/.pi/agent/.
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		agentDir = filepath.Join(homeDir, ".pi", "agent")
	}
	return filepath.Join(agentDir, "AGENTS.md")
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

// ── WorkDirSwitcher ───────────────────────────────────────────

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("pi: work_dir changed", "work_dir", dir)
}

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
	dirs := []string{filepath.Join(absDir, ".pi", "agent", "skills")}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		// Default pi agent skill directory.
		dirs = append(dirs, filepath.Join(homeDir, ".pi", "agent", "skills"))
		// Common shared skill directory used by lark-cli and other agent tools.
		dirs = append(dirs, filepath.Join(homeDir, ".agents", "skills"))

		// If PI_CODING_AGENT_DIR is set, also scan skills under that directory.
		if agentDir := os.Getenv("PI_CODING_AGENT_DIR"); agentDir != "" {
			if filepath.IsAbs(agentDir) {
				dirs = append(dirs, filepath.Join(agentDir, "skills"))
			} else {
				dirs = append(dirs, filepath.Join(homeDir, agentDir, "skills"))
			}
		}
	}
	return dirs
}

// ── Models JSON helpers ─────────────────────────────────────

// modelsJSON represents the structure of ~/.pi/agent/models.json.
type modelsJSON struct {
	Providers map[string]struct {
		Models []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"contextWindow"`
		} `json:"models"`
	} `json:"providers"`
}

// loadModelsContextWindows reads ~/.pi/agent/models.json and returns
// a map of model ID → contextWindow. Keys include both the short ID
// (e.g. "deepseek/deepseek-v4-pro") and the fully-qualified
// provider/ID (e.g. "my-provider/my-model").
// Returns nil on any error (caller falls back to 200K).
func loadModelsContextWindows() map[string]int {
	dir := piSettingsDir()
	if dir == "" {
		slog.Warn("pi: cannot determine pi settings dir for models.json")
		return nil
	}
	path := filepath.Join(dir, "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("pi: models.json not found, using 200K fallback", "path", path)
		} else {
			slog.Warn("pi: read models.json", "path", path, "error", err)
		}
		return nil
	}
	var cfg modelsJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("pi: parse models.json", "path", path, "error", err)
		return nil
	}
	m := make(map[string]int)
	for provider, p := range cfg.Providers {
		for _, mdl := range p.Models {
			m[mdl.ID] = mdl.ContextWindow
			m[provider+"/"+mdl.ID] = mdl.ContextWindow
		}
	}
	return m
}

// ── Settings helpers ─────────────────────────────────────────

// piSettingsDir returns the pi agent config directory.
// Respects PI_CODING_AGENT_DIR env var; defaults to ~/.pi/agent.
func piSettingsDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// settingsPath returns the path to settings.json.
func settingsPath() string {
	dir := piSettingsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// piSettings represents the structure of pi's settings.json relevant fields.
type piSettings struct {
	EnabledModels  []string `json:"enabledModels"`
	DefaultModel   string   `json:"defaultModel"`
	DefaultProvider string  `json:"defaultProvider"`
}

// readSettings reads and parses pi's settings.json.
func readSettings() (*piSettings, error) {
	path := settingsPath()
	if path == "" {
		return nil, fmt.Errorf("pi: cannot determine settings path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pi: read settings: %w", err)
	}
	var s piSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("pi: parse settings: %w", err)
	}
	return &s, nil
}

// readSettingsModels returns the enabledModels from settings.json as ModelOptions.
func readSettingsModels() ([]core.ModelOption, error) {
	s, err := readSettings()
	if err != nil {
		return nil, err
	}
	if len(s.EnabledModels) == 0 {
		return nil, nil
	}
	models := make([]core.ModelOption, 0, len(s.EnabledModels))
	for _, m := range s.EnabledModels {
		option := core.ModelOption{Name: m}
		// Derive a short alias from the last segment after the final "/".
		if idx := strings.LastIndex(m, "/"); idx >= 0 && idx+1 < len(m) {
			option.Alias = m[idx+1:]
		}
		models = append(models, option)
	}
	return models, nil
}

// readDefaultModel returns the defaultModel from settings.json.
func readDefaultModel() (string, error) {
	s, err := readSettings()
	if err != nil {
		return "", err
	}
	// If defaultProvider is set, qualify the defaultModel with it.
	if s.DefaultProvider != "" && s.DefaultModel != "" && !strings.Contains(s.DefaultModel, "/") {
		return s.DefaultProvider + "/" + s.DefaultModel, nil
	}
	return s.DefaultModel, nil
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
