package grok

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("grok", New)
}

// Agent drives Grok Build through its one-shot headless CLI. Each Send starts
// one process and subsequent turns resume the exact Grok session with
// --resume. This is deliberately independent of Grok's ACP server mode.
type Agent struct {
	workDir         string
	model           string
	mode            string
	cmd             string
	cliExtraArgs    []string
	configEnv       []string
	timeout         time.Duration
	reasoningEffort string
	maxTurns        int

	providers  []core.ProviderConfig
	activeIdx  int
	sessionEnv []string
	mu         sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("grok: resolve work_dir %q: %w", workDir, err)
	}
	info, err := os.Stat(absWorkDir)
	if err != nil {
		return nil, fmt.Errorf("grok: work_dir %q: %w", absWorkDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("grok: work_dir %q is not a directory", absWorkDir)
	}

	cmd, extraArgs := core.ParseCmdOpts(opts, "grok")
	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("grok: %q CLI not found in PATH; install it from https://x.ai/cli: %w", cmd, err)
	}

	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	reasoning, _ := opts["reasoning_effort"].(string)
	if reasoning == "" {
		reasoning, _ = opts["effort"].(string)
	}
	reasoning = normalizeReasoningEffort(reasoning)

	timeoutMins := intFromOpts(opts, "timeout_mins", 0)
	var timeout time.Duration
	if timeoutMins > 0 {
		timeout = time.Duration(timeoutMins) * time.Minute
	}

	return &Agent{
		workDir:         absWorkDir,
		model:           strings.TrimSpace(model),
		mode:            normalizeMode(mode),
		cmd:             cmd,
		cliExtraArgs:    append([]string(nil), extraArgs...),
		configEnv:       core.ParseConfigEnv(opts),
		timeout:         timeout,
		reasoningEffort: strings.TrimSpace(reasoning),
		maxTurns:        max(0, intFromOpts(opts, "max_turns", 0)),
		activeIdx:       -1,
	}, nil
}

func intFromOpts(opts map[string]any, key string, def int) int {
	switch value := opts[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		if value != nil {
			slog.Debug("grok: option has unexpected type", "key", key, "type", fmt.Sprintf("%T", value))
		}
		return def
	}
}

// Grok's headless stream is read-only, so an omitted mode defaults to the
// automation-safe yolo mode. Explicit modes retain Grok's native semantics;
// only yolo receives --always-approve.
func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "default", "ask":
		return "default"
	case "accept_edits", "acceptedits", "auto_edit", "autoedit", "edit":
		return "accept_edits"
	case "auto":
		return "auto"
	case "dont_ask", "dontask", "no_ask":
		return "dont_ask"
	case "plan":
		return "plan"
	case "", "yolo", "force", "bypass", "always-approve", "bypasspermissions":
		return "yolo"
	default:
		// A typo in an explicitly configured permission mode must never
		// silently escalate to unrestricted execution.
		return "default"
	}
}

func normalizeReasoningEffort(raw string) string {
	switch effort := strings.ToLower(strings.TrimSpace(raw)); effort {
	case "", "low", "medium", "high":
		return effort
	default:
		slog.Warn("grok: ignoring unsupported reasoning effort", "effort", raw)
		return ""
	}
}

func permissionModeFlag(mode string) string {
	switch mode {
	case "default":
		return "default"
	case "accept_edits":
		return "acceptEdits"
	case "auto":
		return "auto"
	case "dont_ask":
		return "dontAsk"
	case "plan":
		return "plan"
	default:
		return "bypassPermissions"
	}
}

func (a *Agent) Name() string           { return "grok" }
func (a *Agent) CLIBinaryName() string  { return a.commandName() }
func (a *Agent) CLIDisplayName() string { return "Grok Build" }

func (a *Agent) commandName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cmd
}

func (a *Agent) SetWorkDir(dir string) {
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	a.mu.Lock()
	a.workDir = dir
	a.mu.Unlock()
	slog.Info("grok: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	a.model = strings.TrimSpace(model)
	a.mu.Unlock()
	slog.Info("grok: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.RLock()
	configured := core.GetProviderModels(a.providers, a.activeIdx)
	cmdName := a.cmd
	extraArgs := append([]string(nil), a.cliExtraArgs...)
	workDir := a.workDir
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	a.mu.RUnlock()
	if len(configured) > 0 {
		return configured
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := append(extraArgs, "models")
	cmd := exec.CommandContext(probeCtx, cmdName, args...)
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), extraEnv)
	out, err := cmd.Output()
	if err == nil {
		if models := parseModelsOutput(string(out)); len(models) > 0 {
			return models
		}
	} else {
		slog.Debug("grok: model probe failed", "error", err)
	}

	// This is a chooser fallback, not a launch default. New sessions still
	// omit --model unless the user explicitly configured one.
	return []core.ModelOption{{Name: "grok-4.5", Desc: "Grok 4.5"}}
}

func parseModelsOutput(output string) []core.ModelOption {
	seen := make(map[string]bool)
	var models []core.ModelOption
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if !strings.HasPrefix(strings.ToLower(line), "grok-") {
			continue
		}
		name := strings.Fields(line)[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, core.ModelOption{Name: name, Desc: line})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

func (a *Agent) SetReasoningEffort(effort string) {
	effort = normalizeReasoningEffort(effort)
	a.mu.Lock()
	a.reasoningEffort = effort
	a.mu.Unlock()
	slog.Info("grok: reasoning effort changed", "effort", effort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.reasoningEffort
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"high", "medium", "low"}
}

func (a *Agent) SetMode(mode string) {
	mode = normalizeMode(mode)
	a.mu.Lock()
	a.mode = mode
	a.mu.Unlock()
	slog.Info("grok: permission mode changed", "mode", mode)
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "yolo", Name: "Always Approve", NameZh: "全部批准", Desc: "Headless automation; approve tool executions", DescZh: "无头自动化；自动批准工具执行"},
		{Key: "default", Name: "Ask", NameZh: "询问", Desc: "Native ask mode; approvals cannot be answered from IM", DescZh: "原生询问模式；IM 无法响应授权弹窗"},
		{Key: "auto", Name: "Auto", NameZh: "自动判断", Desc: "Run safe actions and block or escalate others", DescZh: "执行安全操作，阻止或升级其他操作"},
		{Key: "accept_edits", Name: "Accept Edits", NameZh: "自动编辑", Desc: "Approve edits; other actions may still require approval", DescZh: "批准编辑；其他操作仍可能需要授权"},
		{Key: "dont_ask", Name: "Don't Ask", NameZh: "不询问", Desc: "Run only pre-approved and read-only tools", DescZh: "仅运行预授权和只读工具"},
		{Key: "plan", Name: "Plan", NameZh: "规划", Desc: "Read-only planning mode", DescZh: "只读规划模式"},
	}
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	a.sessionEnv = append([]string(nil), env...)
	a.mu.Unlock()
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	model := core.GetProviderModel(a.providers, a.activeIdx, a.model)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	cfg := sessionConfig{
		cmd:             a.cmd,
		extraArgs:       append([]string(nil), a.cliExtraArgs...),
		workDir:         a.workDir,
		model:           model,
		mode:            a.mode,
		resumeID:        sessionID,
		extraEnv:        extraEnv,
		timeout:         a.timeout,
		reasoningEffort: a.reasoningEffort,
		maxTurns:        a.maxTurns,
	}
	a.mu.RUnlock()
	return newGrokSession(ctx, cfg)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	workDir := a.workDir
	home := resolveGrokHome(a.effectiveEnvLocked())
	a.mu.RUnlock()
	return listGrokSessions(home, workDir)
}

func (a *Agent) ValidateSessionID(_ context.Context, sessionID string) bool {
	a.mu.RLock()
	workDir := a.workDir
	home := resolveGrokHome(a.effectiveEnvLocked())
	a.mu.RUnlock()
	return findGrokSessionDir(home, workDir, sessionID) != ""
}

func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	a.mu.RLock()
	workDir := a.workDir
	cmdName := a.cmd
	extraArgs := append([]string(nil), a.cliExtraArgs...)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	home := resolveGrokHome(extraEnv)
	a.mu.RUnlock()
	if findGrokSessionDir(home, workDir, sessionID) == "" {
		return fmt.Errorf("grok: session %q does not belong to work_dir %q", sessionID, workDir)
	}
	args := append(extraArgs, "sessions", "delete", sessionID)
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), extraEnv)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := redactEnvSecrets(stripANSI(strings.TrimSpace(string(out))), extraEnv)
		return fmt.Errorf("grok: delete session %q: %w: %s", sessionID, err, truncate(detail, 500))
	}
	return nil
}

func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	a.mu.RLock()
	workDir := a.workDir
	home := resolveGrokHome(a.effectiveEnvLocked())
	a.mu.RUnlock()
	return getGrokSessionHistory(home, workDir, sessionID, limit)
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	opts := map[string]any{"mode": a.mode}
	if a.model != "" {
		opts["model"] = a.model
	}
	if cmd := joinCommand(a.cmd, a.cliExtraArgs); cmd != "" && cmd != "grok" {
		opts["cmd"] = cmd
	}
	if len(a.configEnv) > 0 {
		env := make(map[string]string, len(a.configEnv))
		for _, pair := range a.configEnv {
			if key, value, ok := strings.Cut(pair, "="); ok {
				env[key] = value
			}
		}
		opts["env"] = env
	}
	if a.timeout > 0 {
		opts["timeout_mins"] = int(a.timeout / time.Minute)
	}
	if a.reasoningEffort != "" {
		opts["reasoning_effort"] = a.reasoningEffort
	}
	if a.maxTurns > 0 {
		opts["max_turns"] = a.maxTurns
	}
	return opts
}

func joinCommand(cmd string, args []string) string {
	if cmd == "" {
		return ""
	}
	if len(args) == 0 {
		return cmd
	}
	return cmd + " " + strings.Join(args, " ")
}

func (a *Agent) SkillDirs() []string {
	a.mu.RLock()
	workDir := a.workDir
	home := resolveGrokHome(a.effectiveEnvLocked())
	a.mu.RUnlock()
	return []string{filepath.Join(workDir, ".grok", "skills"), filepath.Join(home, "skills")}
}

func (a *Agent) ProjectMemoryFile() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return filepath.Join(a.workDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	a.mu.RLock()
	home := resolveGrokHome(a.effectiveEnvLocked())
	a.mu.RUnlock()
	path := filepath.Join(home, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	a.providers = append([]core.ProviderConfig(nil), providers...)
	a.mu.Unlock()
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		return true
	}
	for i := range a.providers {
		if a.providers[i].Name == name {
			a.activeIdx = i
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
	provider := a.providers[a.activeIdx]
	return &provider
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]core.ProviderConfig(nil), a.providers...)
}

// providerEnvLocked requires a read or write lock on a.mu.
func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	provider := a.providers[a.activeIdx]
	var env []string
	if provider.APIKey != "" {
		env = append(env, "XAI_API_KEY="+provider.APIKey)
	}
	if provider.BaseURL != "" {
		env = append(env, "GROK_MODELS_BASE_URL="+provider.BaseURL)
	}
	keys := make([]string, 0, len(provider.Env))
	for key := range provider.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+provider.Env[key])
	}
	return env
}

// effectiveEnvLocked requires a read or write lock on a.mu and preserves the
// same override order used for launched sessions.
func (a *Agent) effectiveEnvLocked() []string {
	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	return env
}

var (
	_ core.Agent                           = (*Agent)(nil)
	_ core.AgentDoctorInfo                 = (*Agent)(nil)
	_ core.WorkDirSwitcher                 = (*Agent)(nil)
	_ core.ModelSwitcher                   = (*Agent)(nil)
	_ core.ReasoningEffortSwitcher         = (*Agent)(nil)
	_ core.ModeSwitcher                    = (*Agent)(nil)
	_ core.SessionEnvInjector              = (*Agent)(nil)
	_ core.ProviderSwitcher                = (*Agent)(nil)
	_ core.WorkspaceAgentOptionSnapshotter = (*Agent)(nil)
	_ core.SessionIDValidator              = (*Agent)(nil)
	_ core.SessionDeleter                  = (*Agent)(nil)
	_ core.HistoryProvider                 = (*Agent)(nil)
	_ core.MemoryFileProvider              = (*Agent)(nil)
	_ core.SkillProvider                   = (*Agent)(nil)
)
