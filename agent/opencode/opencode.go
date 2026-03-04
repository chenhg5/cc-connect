package opencode

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

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("opencode", New)
}

// Agent drives the OpenCode CLI in headless mode using `opencode run --format json`.
//
// Modes:
//   - "default": standard mode
//   - "yolo":    auto mode (opencode run is auto by default in non-interactive mode)
type Agent struct {
	workDir    string
	model      string
	mode       string
	cmd        string // CLI binary name, default "opencode"
	providers  []core.ProviderConfig
	activeIdx  int
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
		cmd = "opencode"
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("opencode: %q CLI not found in PATH, install from: https://github.com/opencode-ai/opencode", cmd)
	}

	return &Agent{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		cmd:       cmd,
		activeIdx: -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force", "bypasspermissions":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "opencode" }

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("opencode: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	return []core.ModelOption{
		{Name: "anthropic/claude-sonnet-4-20250514", Desc: "Claude Sonnet 4 (default)"},
		{Name: "anthropic/claude-opus-4-20250514", Desc: "Claude Opus 4"},
		{Name: "openai/gpt-4o", Desc: "GPT-4o"},
		{Name: "openai/o3", Desc: "OpenAI o3"},
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
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newOpencodeSession(ctx, cmd, workDir, model, mode, sessionID, extraEnv)
}

// ListSessions runs `opencode session list` and parses the JSON output.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listOpencodeSessions(a.cmd, a.workDir)
}

func (a *Agent) Stop() error { return nil }

// -- ModeSwitcher --

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("opencode: mode changed", "mode", a.mode)
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
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "OPENCODE.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".opencode", "OPENCODE.md")
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
		slog.Info("opencode: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("opencode: provider switched", "provider", name)
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
		env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// -- Session listing --

// opencodeSessionEntry represents a session from `opencode session list` output.
type opencodeSessionEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated string `json:"updated"`
}

func listOpencodeSessions(cmd, workDir string) ([]core.AgentSessionInfo, error) {
	c := exec.Command(cmd, "session", "list", "--format", "json")
	c.Dir = workDir

	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode: session list: %w", err)
	}

	var entries []opencodeSessionEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("opencode: parse session list: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, e := range entries {
		sessions = append(sessions, core.AgentSessionInfo{
			ID:      e.ID,
			Summary: e.Title,
		})
	}

	return sessions, nil
}
