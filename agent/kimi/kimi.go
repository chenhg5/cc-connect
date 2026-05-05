package kimi

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
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("kimi", New)
}

// Agent drives Kimi Code CLI using --print --output-format stream-json.
//
// Modes:
//   - "default": standard mode (note: --print implicitly enables --yolo)
//   - "yolo":    auto-approve all tool calls
//   - "plan":    read-only plan mode
//   - "quiet":   alias for --quiet (print + text + final-message-only)
type Agent struct {
	workDir    string
	model      string
	mode       string
	cmd        string // CLI binary name, default "kimi"
	timeout    time.Duration
	providers  []core.ProviderConfig
	activeIdx  int // -1 = no provider set
	sessionEnv []string
	mu         sync.RWMutex
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
		cmd = "kimi"
	}

	var timeoutMins int64
	switch v := opts["timeout_mins"].(type) {
	case int64:
		timeoutMins = v
	case int:
		timeoutMins = int64(v)
	case float64:
		timeoutMins = int64(v)
	default:
		if v != nil {
			slog.Debug("kimi: timeout_mins has unexpected type", "type", fmt.Sprintf("%T", v))
		}
	}
	var timeout time.Duration
	if timeoutMins > 0 {
		timeout = time.Duration(timeoutMins) * time.Minute
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("kimi: %q CLI not found in PATH, install with: pip install kimi-cli", cmd)
	}

	return &Agent{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		cmd:       cmd,
		timeout:   timeout,
		activeIdx: -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "force", "bypass", "auto":
		return "yolo"
	case "plan":
		return "plan"
	case "quiet":
		return "quiet"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "kimi" }
func (a *Agent) CLIBinaryName() string  { return "kimi" }
func (a *Agent) CLIDisplayName() string { return "Kimi" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("kimi: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("kimi: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "kimi-k2-0711-preview", Desc: "Kimi K2 (most capable)"},
		{Name: "kimi-k2-0711", Desc: "Kimi K2"},
		{Name: "kimi-k2-5-preview", Desc: "Kimi K2.5 (balanced)"},
		{Name: "kimi-k2-5", Desc: "Kimi K2.5"},
	}
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	cmd := a.cmd
	workDir := a.workDir
	timeout := a.timeout
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newKimiSession(ctx, cmd, workDir, model, mode, sessionID, extraEnv, timeout)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listKimiSessions(a.workDir)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	path := findKimiSessionDir(sessionID)
	if path == "" {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return os.RemoveAll(path)
}

func (a *Agent) Stop() error { return nil }

// ── ModeSwitcher ────────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("kimi: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard mode (print output)", DescZh: "标准模式（打印输出）"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式", Desc: "Read-only plan mode, no execution", DescZh: "只读规划模式，不做修改"},
		{Key: "quiet", Name: "Quiet", NameZh: "静默", Desc: "Quiet mode (final message only)", DescZh: "静默模式（仅最终消息）"},
	}
}

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".kimi", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kimi", "skills"))
	}
	return dirs
}

// ── ContextCompressor implementation ──────────────────────────

func (a *Agent) CompressCommand() string { return "" }

// ── MemoryFileProvider implementation ─────────────────────────

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
	return filepath.Join(homeDir, ".kimi", "AGENTS.md")
}

// ── ProviderSwitcher ────────────────────────────────────────────

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		slog.Info("kimi: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("kimi: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]core.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string
	if p.APIKey != "" {
		env = append(env, "KIMI_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// ── Session listing ─────────────────────────────────────────────

func kimiSessionsBaseDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".kimi", "sessions")
}

func listKimiSessions(workDir string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	sessionsBase := kimiSessionsBaseDir()
	if sessionsBase == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(sessionsBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("kimi: read sessions dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectDir := filepath.Join(sessionsBase, entry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, se := range sessionEntries {
			if !se.IsDir() {
				continue
			}
			sessionDir := filepath.Join(projectDir, se.Name())
			info := parseKimiSessionDir(sessionDir, absWorkDir)
			if info != nil {
				sessions = append(sessions, *info)
			}
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func parseKimiSessionDir(sessionDir, filterWorkDir string) *core.AgentSessionInfo {
	statePath := filepath.Join(sessionDir, "state.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}

	var state struct {
		CustomTitle string `json:"custom_title"`
		Archived    bool   `json:"archived"`
	}
	if json.Unmarshal(stateData, &state) != nil {
		return nil
	}
	if state.Archived {
		return nil
	}

	sessionID := filepath.Base(sessionDir)

	info, err := os.Stat(sessionDir)
	if err != nil {
		return nil
	}

	msgCount := 0
	summary := ""
	contextPath := filepath.Join(sessionDir, "context.jsonl")
	if f, err := os.Open(contextPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			var entry struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			if entry.Role == "user" || entry.Role == "assistant" {
				msgCount++
				if entry.Role == "user" && entry.Content != "" && summary == "" {
					summary = strings.TrimSpace(entry.Content)
				}
			}
		}
	}

	_ = filterWorkDir // Kimi does not store cwd in session metadata; list all sessions.

	if summary == "" {
		summary = state.CustomTitle
	}
	if utf8.RuneCountInString(summary) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   info.ModTime(),
	}
}

func findKimiSessionDir(sessionID string) string {
	sessionsBase := kimiSessionsBaseDir()
	if sessionsBase == "" {
		return ""
	}

	entries, err := os.ReadDir(sessionsBase)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectDir := filepath.Join(sessionsBase, entry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, se := range sessionEntries {
			if !se.IsDir() {
				continue
			}
			if se.Name() == sessionID {
				return filepath.Join(projectDir, se.Name())
			}
		}
	}
	return ""
}
