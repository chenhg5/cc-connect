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
	"time"

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

	providerProxy *core.ProviderProxy // local proxy for third-party providers
	proxyLocalURL string              // local URL of the proxy

	mu sync.RWMutex
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

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("opencode: work_dir changed", "work_dir", dir)
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
	slog.Info("opencode: model changed", "model", model)
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
	a.mu.RLock()
	cmd := a.cmd
	a.mu.RUnlock()
	if models := fetchModelsFromCLI(ctx, cmd); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "anthropic/claude-sonnet-4-20250514", Desc: "Claude Sonnet 4 (default)"},
		{Name: "anthropic/claude-opus-4-20250514", Desc: "Claude Opus 4"},
		{Name: "openai/gpt-4o", Desc: "GPT-4o"},
		{Name: "openai/o3", Desc: "OpenAI o3"},
	}
}

// fetchModelsFromCLI runs `opencode models` and parses the output.
// Each line is a model ID in the format "provider/model-id".
func fetchModelsFromCLI(ctx context.Context, cmd string) []core.ModelOption {
	c := exec.CommandContext(ctx, cmd, "models")
	out, err := c.Output()
	if err != nil {
		slog.Debug("opencode: fetch models from CLI failed", "error", err)
		return nil
	}

	var models []core.ModelOption
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, core.ModelOption{
			Name: name,
			Desc: modelDesc(name),
		})
	}
	if len(models) > 0 {
		slog.Info("opencode: fetched models from CLI", "count", len(models))
	}
	return models
}

// modelDesc generates a human-readable description from a model ID like
// "github-copilot/claude-sonnet-4.5" → "Claude Sonnet 4.5 (github-copilot)".
func modelDesc(modelID string) string {
	slash := strings.IndexByte(modelID, '/')
	if slash < 0 {
		return modelID
	}
	provider := modelID[:slash]
	model := modelID[slash+1:]

	// Title-case the model name: replace hyphens with spaces, capitalize words.
	words := strings.Split(model, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	desc := strings.Join(words, " ")

	return fmt.Sprintf("%s (%s)", desc, provider)
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

func (a *Agent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopProviderProxyLocked()
	return nil
}

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
	a.stopProviderProxyLocked()
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

// providerEnvLocked returns env vars for the active provider. Caller must hold mu.
//
// When a custom base_url is configured:
//  1. We use ANTHROPIC_AUTH_TOKEN (Bearer) instead of ANTHROPIC_API_KEY
//     (x-api-key). OpenCode's Anthropic provider validates API keys against
//     api.anthropic.com which hangs for third-party endpoints; Bearer auth
//     skips that check.
//  2. If the provider sets thinking (e.g. "disabled"), a local reverse proxy
//     rewrites the thinking parameter for compatibility with providers that
//     don't support adaptive thinking.
func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		a.stopProviderProxyLocked()
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string

	if p.BaseURL != "" {
		if p.Thinking != "" {
			if err := a.ensureProviderProxyLocked(p.BaseURL, p.Thinking); err != nil {
				slog.Error("providerproxy: failed to start", "error", err)
				env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
			} else {
				env = append(env, "ANTHROPIC_BASE_URL="+a.proxyLocalURL)
				env = append(env, "NO_PROXY=127.0.0.1")
			}
		} else {
			a.stopProviderProxyLocked()
			env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
		}
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_AUTH_TOKEN="+p.APIKey)
			env = append(env, "ANTHROPIC_API_KEY=")
		}
	} else {
		a.stopProviderProxyLocked()
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}

	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *Agent) ensureProviderProxyLocked(targetURL, thinkingOverride string) error {
	if a.providerProxy != nil && a.proxyLocalURL != "" {
		return nil
	}
	a.stopProviderProxyLocked()
	proxy, localURL, err := core.NewProviderProxy(targetURL, thinkingOverride)
	if err != nil {
		return err
	}
	a.providerProxy = proxy
	a.proxyLocalURL = localURL
	return nil
}

func (a *Agent) stopProviderProxyLocked() {
	if a.providerProxy != nil {
		a.providerProxy.Close()
		a.providerProxy = nil
	}
	a.proxyLocalURL = ""
}

// -- Session listing --

// opencodeSessionEntry represents a session from `opencode session list` output.
type opencodeSessionEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated int64  `json:"updated"` // Unix timestamp in milliseconds
	Created int64  `json:"created"`
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

	msgCounts := querySessionMessageCounts()

	var sessions []core.AgentSessionInfo
	for _, e := range entries {
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           e.ID,
			Summary:      e.Title,
			MessageCount: msgCounts[e.ID],
			ModifiedAt:   time.UnixMilli(e.Updated),
		})
	}

	return sessions, nil
}

// querySessionMessageCounts uses the sqlite3 CLI to read message counts from
// OpenCode's local database. Returns an empty map on any failure.
func querySessionMessageCounts() map[string]int {
	dbPath := opencodeDBPath()
	if dbPath == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		slog.Warn("opencode: sqlite3 CLI not found, message counts unavailable", "err", err)
		return nil
	}

	out, err := exec.Command(sqlite3, dbPath,
		"SELECT session_id, COUNT(*) FROM message GROUP BY session_id").Output()
	if err != nil {
		slog.Warn("opencode: sqlite3 query failed", "db_path", dbPath, "err", err)
		return nil
	}

	counts := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(parts[1], "%d", &n); err == nil {
			counts[parts[0]] = n
		}
	}
	return counts
}

func opencodeDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}
