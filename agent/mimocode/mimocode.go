package mimocode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("mimocode", New)
}

// Agent drives MiMoCode CLI using `mimo run --format json`.
//
// Modes:
//   - "default": standard mode
//   - "yolo":    auto-approve all permissions (via --dangerously-skip-permissions)
type Agent struct {
	workDir       string
	model         string
	mode          string
	cmd           string // CLI binary name, default "mimo"
	agentName     string // passed as --agent to mimo
	providers     []core.ProviderConfig
	activeIdx     int
	sessionEnv    []string
	mu            sync.RWMutex
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
		cmd = "mimo"
	}
	agentName, _ := opts["agent"].(string)

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("mimocode: %q CLI not found in PATH, install from: https://github.com/xiaomimimo/mimocode", cmd)
	}

	return &Agent{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		cmd:       cmd,
		agentName: agentName,
		activeIdx: -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force", "bypasspermissions", "dangerously-skip-permissions":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "mimocode" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("mimocode: work_dir changed", "work_dir", dir)
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
	slog.Info("mimocode: model changed", "model", model)
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
		{Name: "mimo-auto", Desc: "MiMo Auto (default)"},
		{Name: "mimo-pro", Desc: "MiMo Pro"},
		{Name: "mimo-lite", Desc: "MiMo Lite"},
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
	agentName := a.agentName
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newMimocodeSession(ctx, cmd, workDir, model, mode, agentName, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	cmd := a.cmd
	workDir := a.workDir
	a.mu.RUnlock()

	c := exec.Command(cmd, "session", "list", "--format", "json")
	c.Dir = workDir

	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("mimocode: session list: %w", err)
	}

	var entries []mimocodeSessionEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("mimocode: parse session list: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, e := range entries {
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           e.ID,
			Summary:      e.Title,
			MessageCount: e.MessageCount,
			ModifiedAt:   e.UpdatedAt,
		})
	}

	return sessions, nil
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	a.mu.RLock()
	cmd := a.cmd
	workDir := a.workDir
	a.mu.RUnlock()

	c := exec.Command(cmd, "session", "delete", sessionID)
	c.Dir = workDir
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("mimocode: delete session %s: %w: %s", sessionID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// -- ModeSwitcher --

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("mimocode: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard mode", DescZh: "标准模式"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
	}
}

// -- ContextCompressor --

func (a *Agent) CompressCommand() string { return "/compact" }

// -- MemoryFileProvider --

func (a *Agent) ProjectMemoryFile() string {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "MIMOCODE.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".mimocode", "MIMOCODE.md")
}

// -- ProviderSwitcher --

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
		slog.Info("mimocode: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("mimocode: provider switched", "provider", name)
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
		env = append(env, "MIMOCODE_API_KEY="+p.APIKey)
	}
	if p.BaseURL != "" {
		env = append(env, "MIMOCODE_BASE_URL="+p.BaseURL)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// -- Session listing --

type mimocodeSessionEntry struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}
