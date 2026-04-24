package claudecodesdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	core.RegisterAgent("claudecodesdk", New)
}

// Agent implements core.Agent using a Node.js sidecar with claude-agent-sdk.
type Agent struct {
	workDir        string
	model          string
	mode           string
	nodePath       string
	sidecarDir     string
	claudePath     string
	providers      []core.ProviderConfig
	activeIdx      int
	sessionEnv     []string
	agentEnv       []string
	allowedTools   []string
	platformPrompt string
	reasoningEffort string
	config         sidecarConfig // SDK query options
	mu             sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}

	nodePath, _ := opts["node_path"].(string)
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			return nil, fmt.Errorf("claudecodesdk: node not found in PATH: %w", err)
		}
	}

	sidecarDir, _ := opts["sidecar_dir"].(string)

	claudePath, _ := opts["claude_path"].(string)
	if claudePath == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			claudePath = p
		}
	}

	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	var allowedTools []string
	if tools, ok := opts["allowed_tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok {
				allowedTools = append(allowedTools, s)
			}
		}
	}

	// Build sidecar config from options
	config := sidecarConfig{
		AllowedTools: allowedTools,
	}

	// ── System prompt ──────────────────────────────────────
	// Auto-append AgentSystemPrompt (cc-connect cron/relay instructions).
	// This matches how the claudecode agent uses --append-system-prompt.
	sysPrompt := core.AgentSystemPrompt()
	if userAppend, ok := opts["append_system_prompt"].(string); ok && userAppend != "" {
		sysPrompt = userAppend + "\n\n" + sysPrompt
	}
	config.AppendSystemPrompt = sysPrompt

	// custom_system_prompt (DISABLED: 会完全覆盖 cc-connect 系统提示，导致 cron/relay 指令丢失)
	// if v, ok := opts["custom_system_prompt"].(string); ok {
	// 	config.CustomSystemPrompt = v
	// }

	// ── Reasoning / effort ─────────────────────────────────
	if v, ok := opts["effort"].(string); ok {
		config.Effort = v
	}
	if v, ok := opts["thinking"].(map[string]any); ok {
		config.Thinking = v
	}

	// ── Tools (DISABLED: 显式工具集限制无意义，cc-connect 需要完整工具能力) ──
	// if v, ok := opts["tools"]; ok {
	// 	config.Tools = v
	// }

	// ── Settings ───────────────────────────────────────────
	if v, ok := opts["settings_path"].(string); ok {
		config.SettingsPath = v
	}
	if v, ok := opts["setting_sources"].([]any); ok {
		for _, s := range v {
			if str, ok := s.(string); ok {
				config.SettingSources = append(config.SettingSources, str)
			}
		}
	}

	// ── Model ──────────────────────────────────────────────
	if v, ok := opts["fallback_model"].(string); ok {
		config.FallbackModel = v
	}

	// ── MCP ────────────────────────────────────────────────
	if mcp, ok := opts["mcp_servers"].(map[string]any); ok {
		config.MCPServers = mcp
	}
	if disallowed, ok := opts["disallowed_tools"].([]any); ok {
		for _, t := range disallowed {
			if s, ok := t.(string); ok {
				config.DisallowedTools = append(config.DisallowedTools, s)
			}
		}
	}

	// ── Limits ─────────────────────────────────────────────
	switch v := opts["max_turns"].(type) {
	case int:
		config.MaxTurns = v
	case int64:
		config.MaxTurns = int(v)
	case float64:
		config.MaxTurns = int(v)
	}
	switch v := opts["max_budget_usd"].(type) {
	case float64:
		config.MaxBudgetUsd = v
	case int:
		config.MaxBudgetUsd = float64(v)
	}

	// ── Agent system (DISABLED: cc-connect 不需要自定义子代理) ──
	// if v, ok := opts["agent"].(string); ok {
	// 	config.Agent = v
	// }
	// if v, ok := opts["agents"].(map[string]any); ok {
	// 	config.Agents = v
	// }

	// ── Plugins (DISABLED: 无插件生态使用) ─────────────────────
	// if v, ok := opts["plugins"].([]any); ok {
	// 	config.Plugins = v
	// }

	// ── Sandbox (DISABLED: 容器/VM 环境下冗余) ────────────────
	// if v, ok := opts["sandbox"].(map[string]any); ok {
	// 	config.Sandbox = v
	// }

	// ── Other (DISABLED: 小众/桌面端特性) ────────────────────
	// if v, ok := opts["strict_mcp_config"].(bool); ok {
	// 	config.StrictMCPConfig = v
	// }
	// if v, ok := opts["include_hook_events"].(bool); ok {
	// 	config.IncludeHookEvents = v
	// }
	// if v, ok := opts["enable_file_checkpointing"].(bool); ok {
	// 	config.EnableFileCheckpoint = v
	// }
	// if v, ok := opts["debug"].(bool); ok {
	// 	config.Debug = v
	// }
	// if v, ok := opts["debug_file"].(string); ok {
	// 	config.DebugFile = v
	// }

	// ── Session (DISABLED: cc-connect 通过 session ID 自行管理恢复) ──
	// if v, ok := opts["continue"].(bool); ok {
	// 	config.Continue = v
	// }

	return &Agent{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		nodePath:     nodePath,
		sidecarDir:   sidecarDir,
		claudePath:   claudePath,
		activeIdx:    -1,
		agentEnv:     core.AgentEnvFromOpts(opts),
		allowedTools: allowedTools,
		config:       config,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "acceptedits", "accept-edits", "accept_edits", "edit":
		return "acceptEdits"
	case "plan":
		return "plan"
	case "auto":
		return "auto"
	case "bypasspermissions", "bypass-permissions", "bypass_permissions", "yolo":
		return "bypassPermissions"
	case "dontask", "dont-ask", "dont_ask":
		return "dontAsk"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "claudecodesdk" }

// CLIBinaryName implements core.AgentDoctorInfo.
func (a *Agent) CLIBinaryName() string { return "claude" }

// CLIDisplayName implements core.AgentDoctorInfo.
func (a *Agent) CLIDisplayName() string { return "Claude SDK" }

// SetWorkDir implements core.WorkDirSwitcher.
func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("claudecodesdk: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

// SetModel implements core.ModelSwitcher.
func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("claudecodesdk: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	return []core.ModelOption{
		{Name: "sonnet", Desc: "Claude Sonnet 4 (balanced)"},
		{Name: "opus", Desc: "Claude Opus 4 (most capable)"},
		{Name: "haiku", Desc: "Claude Haiku 3.5 (fastest)"},
	}
}

// SetSessionEnv implements core.SessionEnvInjector.
func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

// SetMode implements core.ModeSwitcher.
func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("claudecodesdk: mode changed", "mode", mode)
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "acceptEdits", Name: "Accept Edits", NameZh: "接受编辑", Desc: "Auto-approve file edits, ask for others", DescZh: "自动允许文件编辑，其他需确认"},
		{Key: "plan", Name: "Plan Mode", NameZh: "计划模式", Desc: "Plan only, no execution until approved", DescZh: "只做规划不执行，审批后再执行"},
		{Key: "auto", Name: "Auto", NameZh: "自动模式", Desc: "Claude decides when to ask for permission", DescZh: "由 Claude 自动判断何时需要确认"},
		{Key: "bypassPermissions", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Auto-approve everything", DescZh: "全部自动通过"},
		{Key: "dontAsk", Name: "Don't Ask", NameZh: "静默拒绝", Desc: "Auto-deny tools unless pre-approved via allowed_tools or settings.json allow rules", DescZh: "未预授权的工具自动拒绝，不弹确认"},
	}
}

// HasSystemPromptSupport implements core.SystemPromptSupporter.
func (a *Agent) HasSystemPromptSupport() bool { return true }

// SetPlatformPrompt implements core.PlatformPromptInjector.
func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.platformPrompt = prompt
}

// SetReasoningEffort implements core.ReasoningEffortSwitcher.
func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningEffort = normalizeEffort(effort)
	slog.Info("claudecodesdk: reasoning effort changed", "effort", a.reasoningEffort)
}

// GetReasoningEffort implements core.ReasoningEffortSwitcher.
func (a *Agent) GetReasoningEffort() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.reasoningEffort
}

// AvailableReasoningEfforts implements core.ReasoningEffortSwitcher.
func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"low", "medium", "high", "max"}
}

// AddAllowedTools implements core.ToolAuthorizer.
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
	slog.Info("claudecodesdk: updated allowed tools", "tools", tools, "total", len(a.allowedTools))
	return nil
}

// GetAllowedTools implements core.ToolAuthorizer.
func (a *Agent) GetAllowedTools() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]string, len(a.allowedTools))
	copy(result, a.allowedTools)
	return result
}

// GetDisallowedTools returns the current list of disallowed tools.
func (a *Agent) GetDisallowedTools() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]string, len(a.config.DisallowedTools))
	copy(result, a.config.DisallowedTools)
	return result
}

// normalizeEffort maps user-friendly aliases to Claude SDK effort values.
func normalizeEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "low":
		return "low"
	case "medium", "med":
		return "medium"
	case "high":
		return "high"
	case "max":
		return "max"
	default:
		return ""
	}
}

// providerEnvLocked returns env vars for the active provider. Caller must hold mu.
//
// When a custom base_url is configured, we use ANTHROPIC_AUTH_TOKEN (Bearer)
// instead of ANTHROPIC_API_KEY (x-api-key). Claude Code validates API keys
// against api.anthropic.com which hangs for third-party endpoints; Bearer auth
// skips that check.
func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string

	if p.BaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_AUTH_TOKEN="+p.APIKey)
			env = append(env, "ANTHROPIC_API_KEY=")
		}
		if p.Model != "" {
			env = append(env, "ANTHROPIC_MODEL="+p.Model)
		}
	} else {
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}

	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	slog.Debug("claudecodesdk: providerEnv",
		"provider", p.Name,
		"model", p.Model,
		"env", core.RedactEnv(env))
	return env
}

// StartSession creates a new sidecar process for an interactive session.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	workDir := a.workDir
	model := a.model
	mode := a.mode
	claudePath := a.claudePath
	sidecarDir := a.sidecarDir
	sessionEnv := a.sessionEnv
	agentEnv := a.agentEnv
	config := a.config
	providerEnv := a.providerEnvLocked()
	platformPrompt := a.platformPrompt
	reasoningEffort := a.reasoningEffort
	allowedTools := make([]string, len(a.allowedTools))
	copy(allowedTools, a.allowedTools)
	a.mu.RUnlock()

	// Resolve sidecar path: embedded > sidecar_dir > binary-relative
	sidecarPath, err := resolveSidecarPath(sidecarDir)
	if err != nil {
		return nil, err
	}

	// Provider model override: if active provider has a model, prefer it.
	if activeProv := a.GetActiveProvider(); activeProv != nil && activeProv.Model != "" {
		model = activeProv.Model
	}

	// Inject platform formatting instructions into system prompt.
	if platformPrompt != "" {
		config.AppendSystemPrompt += "\n## Formatting\n" + platformPrompt + "\n"
	}

	// Apply current reasoning effort (may have changed at runtime).
	if reasoningEffort != "" {
		config.Effort = reasoningEffort
	}

	// Apply current allowed tools (may have been extended via AddAllowedTools).
	if len(allowedTools) > 0 {
		config.AllowedTools = allowedTools
	}

	var extraEnv []string
	extraEnv = append(extraEnv, providerEnv...)
	extraEnv = append(extraEnv, sessionEnv...)
	extraEnv = append(extraEnv, agentEnv...)

	// Serialize sidecar config as JSON for the env var
	if configJSON, err := json.Marshal(config); err == nil && len(configJSON) > 2 {
		extraEnv = append(extraEnv, "SIDECAR_CONFIG="+string(configJSON))
	}

	return newSDKSession(ctx, a.nodePath, sidecarPath, workDir, model, mode, sessionID, claudePath, extraEnv)
}

func (a *Agent) Stop() error { return nil }

// SetProviders implements core.ProviderSwitcher.
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
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
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
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]core.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

// ProjectMemoryFile implements core.MemoryFileProvider.
func (a *Agent) ProjectMemoryFile() string {
	absDir, _ := filepath.Abs(a.workDir)
	return filepath.Join(absDir, "CLAUDE.md")
}

// GlobalMemoryFile implements core.MemoryFileProvider.
func (a *Agent) GlobalMemoryFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "CLAUDE.md")
}

// CompressCommand implements core.ContextCompressor.
func (a *Agent) CompressCommand() string { return "/compact" }

// CommandDirs implements core.CommandProvider.
func (a *Agent) CommandDirs() []string {
	absDir, _ := filepath.Abs(a.workDir)
	dirs := []string{filepath.Join(absDir, ".claude", "commands")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "commands"))
	}
	return dirs
}

// SkillDirs implements core.SkillProvider.
func (a *Agent) SkillDirs() []string {
	absDir, _ := filepath.Abs(a.workDir)
	return appendProjectClaudeSkillDirs(absDir, claudeConfigHomeDir())
}

func claudeConfigHomeDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func appendProjectClaudeSkillDirs(workDir, configHome string) []string {
	home, _ := os.UserHomeDir()
	projectDirs := walkUpClaudeSkillDirs(workDir, home)
	if configHome == "" {
		return projectDirs
	}
	return uniqueSkillDirs(append(projectDirs, filepath.Join(configHome, "skills")))
}

func walkUpClaudeSkillDirs(workDir, home string) []string {
	current := filepath.Clean(workDir)
	home = filepath.Clean(home)
	stopAt := findGitRoot(current)

	var dirs []string
	for {
		if home != "" && filepath.Clean(current) == filepath.Clean(home) {
			break
		}
		dirs = append(dirs, filepath.Join(current, ".claude", "skills"))
		if stopAt != "" && filepath.Clean(current) == filepath.Clean(stopAt) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return uniqueSkillDirs(dirs)
}

func findGitRoot(start string) string {
	current := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func uniqueSkillDirs(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

// resolveSidecarPath finds the sidecar JS file using this priority:
//  1. Embedded bundle (go:embed) → extract to cache dir
//  2. sidecar_dir config option
//  3. Binary-relative ../sidecar/sidecar.mjs
func resolveSidecarPath(sidecarDir string) (string, error) {
	// 1. Try embedded bundle first
	if len(sidecarBundle) > 0 {
		cachePath, err := extractEmbeddedSidecar()
		if err != nil {
			slog.Warn("claudecodesdk: failed to extract embedded sidecar, falling back to file", "error", err)
		} else {
			return cachePath, nil
		}
	}

	// 2. Explicit sidecar_dir from config
	if sidecarDir != "" {
		p := filepath.Join(sidecarDir, "sidecar.mjs")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// 3. Binary-relative path
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "sidecar", "sidecar.mjs")
		if abs, err := filepath.Abs(candidate); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
	}

	return "", fmt.Errorf("claudecodesdk: sidecar not found (no embedded bundle, no sidecar_dir, no binary-relative)")
}

// extractEmbeddedSidecar writes the embedded bundle to a cache directory
// keyed by content hash. Returns the path to the extracted file.
func extractEmbeddedSidecar() (string, error) {
	hash := sha256.Sum256(sidecarBundle)
	shortHash := hex.EncodeToString(hash[:8])

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}

	cacheDir := filepath.Join(home, ".cc-connect", "sidecar")
	target := filepath.Join(cacheDir, "sidecar."+shortHash+".mjs")

	// Already extracted?
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	// Write to temp file first, then rename (atomic)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", cacheDir, err)
	}

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, sidecarBundle, 0o644); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("rename: %w", err)
	}

	slog.Info("claudecodesdk: extracted embedded sidecar", "path", target, "size", len(sidecarBundle))
	return target, nil
}
