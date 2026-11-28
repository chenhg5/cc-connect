package droid

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("droid", New)
}

type Agent struct {
	cmd                   string
	cliExtraArgs          []string
	configEnv             []string
	workDir               string
	model                 string
	reasoningEffort       string
	auto                  string
	skipPermissionsUnsafe bool
	providers             []core.ProviderConfig
	activeIdx             int
	sessionEnv            []string
	usageGeneration       uint64
	usageSessionID        string
	usageHomeDir          string
	usageWorkDir          string
	mu                    sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}

	cmd, cliExtraArgs := core.ParseCmdOpts(opts, "droid")

	model, _ := opts["model"].(string)
	reasoningEffort, _ := opts["reasoning_effort"].(string)
	auto, _ := opts["auto"].(string)

	skipPermissionsUnsafe := false
	if v, ok := opts["skip_permissions_unsafe"].(bool); ok {
		skipPermissionsUnsafe = v
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("droid: %q CLI not found in PATH", cmd)
	}

	a := &Agent{
		cmd:                   cmd,
		cliExtraArgs:          cliExtraArgs,
		configEnv:             core.ParseConfigEnv(opts),
		workDir:               workDir,
		model:                 model,
		reasoningEffort:       normalizeReasoningEffort(reasoningEffort),
		auto:                  normalizeAuto(auto),
		skipPermissionsUnsafe: skipPermissionsUnsafe,
		activeIdx:             -1,
	}
	if mode, _ := opts["mode"].(string); strings.TrimSpace(mode) != "" {
		applyModeLocked(a, mode)
	} else if a.skipPermissionsUnsafe {
		a.auto = ""
	}
	return a, nil
}

func (a *Agent) Name() string { return "droid" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workDir != dir {
		a.resetUsageLocked()
	}
	a.workDir = dir
	slog.Info("droid: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("droid: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	if models := configuredProviderModels(a); len(models) > 0 {
		return models
	}

	builtins := []core.ModelOption{
		{Name: "gpt-5.3-codex", Desc: "GPT-5.3-Codex"},
		{Name: "gpt-5.4", Desc: "GPT-5.4"},
		{Name: "claude-opus-4-6", Desc: "Claude Opus 4.6"},
		{Name: "claude-sonnet-4-6", Desc: "Claude Sonnet 4.6"},
	}
	custom := a.loadDroidCustomModels()
	if len(custom) == 0 {
		return builtins
	}

	seen := make(map[string]struct{}, len(builtins)+len(custom))
	all := make([]core.ModelOption, 0, len(builtins)+len(custom))
	for _, m := range builtins {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		all = append(all, m)
	}
	for _, m := range custom {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		all = append(all, m)
	}
	return all
}

func configuredProviderModels(a *Agent) []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func normalizeAuto(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none", "off", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningEffort = normalizeReasoningEffort(effort)
	slog.Info("droid: reasoning effort changed", "reasoning_effort", a.reasoningEffort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.reasoningEffort
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"none", "off", "low", "medium", "high", "xhigh", "max"}
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldHome, _ := droidHomeDir(a.sessionEnvLocked())
	a.sessionEnv = append([]string(nil), env...)
	if newHome, _ := droidHomeDir(a.sessionEnvLocked()); newHome != oldHome {
		a.resetUsageLocked()
	}
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	cmd := a.cmd
	cliExtraArgs := append([]string(nil), a.cliExtraArgs...)
	workDir := a.workDir
	model := a.model
	reasoningEffort := a.reasoningEffort
	auto := a.auto
	skipPermissionsUnsafe := a.skipPermissionsUnsafe
	extraEnv := a.sessionEnvLocked()
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := strings.TrimSpace(a.providers[a.activeIdx].Model); m != "" {
			model = m
		}
	}
	homeDir, _ := droidHomeDir(extraEnv)
	absWorkDir, _ := filepath.Abs(workDir)
	a.resetUsageLocked()
	generation := a.usageGeneration
	a.usageHomeDir = homeDir
	a.usageWorkDir = absWorkDir
	if sessionID != core.ContinueSession {
		a.usageSessionID = sessionID
	}
	a.mu.Unlock()

	session, err := newDroidSession(ctx, cmd, cliExtraArgs, workDir, model, reasoningEffort, auto, skipPermissionsUnsafe, sessionID, extraEnv)
	if err != nil {
		return nil, err
	}
	session.onSessionID = func(id string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.usageGeneration == generation {
			a.usageSessionID = id
		}
	}
	session.usageReporter = func(ctx context.Context, id string) (*core.UsageReport, error) {
		return readDroidSessionUsage(ctx, homeDir, absWorkDir, id)
	}
	return session, nil
}

func (a *Agent) resetUsageLocked() {
	a.usageGeneration++
	a.usageSessionID = ""
	a.usageHomeDir = ""
	a.usageWorkDir = ""
}

func (a *Agent) sessionEnvLocked() []string {
	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	return append(env, a.sessionEnv...)
}

func (a *Agent) homeDir() (string, error) {
	a.mu.RLock()
	env := a.sessionEnvLocked()
	a.mu.RUnlock()
	return droidHomeDir(env)
}

func droidHomeDir(env []string) (string, error) {
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	for i := len(env) - 1; i >= 0; i-- {
		if name, value, ok := strings.Cut(env[i], "="); ok && name == key {
			if value == "" {
				return "", fmt.Errorf("droid: %s is empty", key)
			}
			return value, nil
		}
	}
	return os.UserHomeDir()
}

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	opts := map[string]any{
		"model":                   a.model,
		"reasoning_effort":        a.reasoningEffort,
		"auto":                    a.auto,
		"skip_permissions_unsafe": a.skipPermissionsUnsafe,
	}
	if a.cmd != "" {
		opts["cmd"] = append([]string{a.cmd}, a.cliExtraArgs...)
	}
	if len(a.configEnv) > 0 {
		env := make(map[string]string, len(a.configEnv))
		for _, entry := range a.configEnv {
			if key, value, ok := strings.Cut(entry, "="); ok {
				env[key] = value
			}
		}
		opts["env"] = env
	}
	return opts
}

func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string
	if apiKey := strings.TrimSpace(p.APIKey); apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+apiKey)
	}
	if baseURL := strings.TrimSpace(p.BaseURL); baseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+baseURL)
	}
	for k, v := range p.Env {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	homeDir, err := a.homeDir()
	if err != nil {
		return nil, fmt.Errorf("droid: cannot determine home dir: %w", err)
	}

	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	baseDir := filepath.Join(homeDir, ".factory", "sessions")
	return listDroidSessionsFromBase(baseDir, workDir)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	homeDir, err := a.homeDir()
	if err != nil {
		return fmt.Errorf("droid: cannot determine home dir: %w", err)
	}

	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	baseDir := filepath.Join(homeDir, ".factory", "sessions")
	path, err := findDroidSessionFileFromBase(baseDir, workDir, sessionID)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	applyModeLocked(a, mode)
	changedMode := "default"
	if a.skipPermissionsUnsafe {
		changedMode = "yolo"
	} else if a.auto != "" {
		changedMode = a.auto
	}
	slog.Info("droid: mode changed", "mode", changedMode)
}

func applyModeLocked(a *Agent, mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "low":
		a.auto = "low"
		a.skipPermissionsUnsafe = false
	case "medium":
		a.auto = "medium"
		a.skipPermissionsUnsafe = false
	case "high":
		a.auto = "high"
		a.skipPermissionsUnsafe = false
	case "yolo", "unsafe":
		a.auto = ""
		a.skipPermissionsUnsafe = true
	default:
		a.auto = ""
		a.skipPermissionsUnsafe = false
	}
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.skipPermissionsUnsafe {
		return "yolo"
	}
	if a.auto != "" {
		return a.auto
	}
	return "default"
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Droid CLI default permissions", DescZh: "Droid CLI 默认权限"},
		{Key: "low", Name: "Auto Low", NameZh: "低自动化", Desc: "Allow low-risk operations", DescZh: "允许低风险操作"},
		{Key: "medium", Name: "Auto Medium", NameZh: "中自动化", Desc: "Allow medium-risk operations", DescZh: "允许中风险操作"},
		{Key: "high", Name: "Auto High", NameZh: "高自动化", Desc: "Allow high-risk operations", DescZh: "允许高风险操作"},
		{Key: "yolo", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Skip all permission checks", DescZh: "跳过所有权限检查"},
	}
}

func (a *Agent) ProjectMemoryFile() string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := a.homeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".factory", "AGENTS.md")
}

func (a *Agent) CompressCommand() string { return "/compress" }

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldHome, _ := droidHomeDir(a.sessionEnvLocked())
	defer func() {
		if newHome, _ := droidHomeDir(a.sessionEnvLocked()); newHome != oldHome {
			a.resetUsageLocked()
		}
	}()
	a.providers = providers
	if len(a.providers) == 0 {
		a.activeIdx = -1
		return
	}
	if a.activeIdx >= len(a.providers) {
		a.activeIdx = -1
	}
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldHome, _ := droidHomeDir(a.sessionEnvLocked())
	defer func() {
		if newHome, _ := droidHomeDir(a.sessionEnvLocked()); newHome != oldHome {
			a.resetUsageLocked()
		}
	}()
	if name == "" {
		a.activeIdx = -1
		slog.Info("droid: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("droid: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]core.ProviderConfig, len(a.providers))
	copy(out, a.providers)
	return out
}

func (a *Agent) SkillDirs() []string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	dirs := []string{filepath.Join(absDir, ".factory", "skills")}

	homeDir, err := a.homeDir()
	if err != nil {
		return dirs
	}

	dirs = append(dirs, filepath.Join(homeDir, ".factory", "skills"))

	patterns := []string{
		filepath.Join(homeDir, ".factory", "plugins", "cache", "*", "*", "*", "skills"),
		filepath.Join(homeDir, ".factory", "plugins", "cache", "*", "*", "*", "*", "skills"),
	}
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		dirs = append(dirs, matches...)
	}

	uniq := make([]string, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		uniq = append(uniq, d)
	}
	return uniq
}

var _ core.Agent = (*Agent)(nil)
var _ core.ProviderSwitcher = (*Agent)(nil)
var _ core.ContextCompressor = (*Agent)(nil)
var _ core.UsageReporter = (*Agent)(nil)
var _ core.WorkspaceAgentOptionSnapshotter = (*Agent)(nil)
var _ core.HistoryProvider = (*Agent)(nil)
var _ core.ModeSwitcher = (*Agent)(nil)
var _ core.SkillProvider = (*Agent)(nil)
