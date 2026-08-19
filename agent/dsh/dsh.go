// Package dsh bridges cc-connect to the DeepSeek Harness CLI (dsh).
//
// Interaction model: cc-connect drives `dsh --profile headless` as a one-shot
// process per user message. The headless runner (patched via the user's
// dsh.nix `headless-cc-connect.patch`) accepts:
//
//	--session-id <id>   create-or-resume a persisted dsh session with this id
//	--provider <name>   override the provider route for this run
//	--model <model>     override the default model for this run
//	--mode <mode>       pin sandbox/approval knobs for this run
//	--preset <name>     select/recompose a blank session's agent preset
//
// The session id is owned by cc-connect: the engine's StartSession(sessionID)
// is used directly as the dsh session id, so a multi-turn Feishu conversation
// maps to one persisted dsh session (context continuity across turns).
package dsh

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("dsh", New)
}

// Agent drives the DeepSeek Harness CLI (dsh) in headless profile.
type Agent struct {
	cmd          string   // path to dsh binary
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	workDir      string
	model        string // model override ("" = use dsh settings default)
	provider     string // provider route override ("" = use dsh settings default)
	mode         string // "read-only" | "workspace-write" | "danger-full-access" | "confirm"
	sessionEnv   []string
	mu           sync.Mutex
}

// New creates the dsh agent from cc-connect config options.
func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	provider, _ := opts["provider"].(string)
	if strings.TrimSpace(provider) == "" {
		provider = readDefaultProvider()
	}
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	cmd, extraArgs := core.ParseCmdOpts(opts, "dsh")

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("dsh: '%s' not found in PATH, install via home-manager (agent/dsh/dsh.nix)", cmd)
	}

	return &Agent{
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		workDir:      workDir,
		model:        model,
		provider:     strings.TrimSpace(provider),
		mode:         mode,
	}, nil
}

// normalizeMode maps user-facing /mode values to the dsh permission modes.
// "confirm" mirrors the user's dsh web profile default preset
// (danger-full-access sandbox + ask approval); under headless (no approval
// answerer) it behaves like danger-full-access because full-access never
// needs sandbox escalation.
func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default", "confirm":
		return "confirm"
	case "read-only", "ro":
		return "read-only"
	case "workspace-write", "workspace":
		return "workspace-write"
	case "danger-full-access", "full-access", "full", "yolo", "auto", "never", "bypass":
		return "danger-full-access"
	default:
		return "confirm"
	}
}

func (a *Agent) Name() string           { return "dsh" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "DSH" }

// WorkspaceAgentOptions implements core.WorkspaceAgentOptionSnapshotter.
// work_dir is omitted — the engine sets the target workspace. model and mode
// are copied by the engine via GetModel/GetMode, so we don't repeat them here.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	opts := map[string]any{}
	if a.cmd != "" && a.cmd != "dsh" {
		opts["cmd"] = a.cmd
	}
	return opts
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

// ── ModelSwitcher ────────────────────────────────────────────

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("dsh: model changed", "model", model)
}

// GetModel returns the cc-connect-configured model, falling back to the
// model stored in dsh's own settings.yaml when none was configured here.
func (a *Agent) GetModel() string {
	a.mu.Lock()
	model := a.model
	a.mu.Unlock()
	if model != "" {
		return model
	}
	if def, err := readDefaultModel(); err == nil && def != "" {
		return def
	}
	return ""
}

// GetModelProvider returns the provider route used by the next dsh run.
func (a *Agent) GetModelProvider() string {
	a.mu.Lock()
	provider := a.provider
	a.mu.Unlock()
	if provider != "" {
		return provider
	}
	return readDefaultProvider()
}

// SetModelForProvider switches both halves of a dsh model selection. dsh's
// native runner keeps provider and model separate, so changing only the model
// would send a valid id to the wrong route when catalogs overlap.
func (a *Agent) SetModelForProvider(provider, model string) {
	a.mu.Lock()
	a.provider = strings.TrimSpace(provider)
	a.model = strings.TrimSpace(model)
	a.mu.Unlock()
	slog.Info("dsh: model route changed", "provider", provider, "model", model)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if models := a.readRuntimeModelCatalog(ctx); len(models) > 0 {
		return appendDeepSeekFallback(models, readSettingsModels("deepseek-official"))
	}
	if models := readSettingsModels("deepseek-official"); len(models) > 0 {
		return models
	}
	// Fallback: the catalog dsh ships for the deepseek-official provider.
	models := []core.ModelOption{
		{Name: "deepseek-v4-flash", Desc: "DeepSeek-V4-Flash", Provider: "deepseek-official"},
		{Name: "deepseek-v4-pro", Desc: "DeepSeek-V4-Pro", Provider: "deepseek-official"},
	}
	if current := a.GetModel(); current != "" {
		seen := false
		for _, model := range models {
			if model.Name == current {
				seen = true
				break
			}
		}
		if !seen {
			models = append(models, core.ModelOption{
				Name:     current,
				Provider: a.GetModelProvider(),
			})
		}
	}
	return models
}

// ── ModeSwitcher ─────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("dsh: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "read-only", Name: "Read Only", NameZh: "只读", Desc: "Read-only file access", DescZh: "只读文件访问,不允许任何写入"},
		{Key: "workspace-write", Name: "Workspace Write", NameZh: "工作区写入", Desc: "Writes allowed under the workspace", DescZh: "仅允许在工作区内写入,工作区外写入需审批(无审批端时自动拒绝)"},
		{Key: "danger-full-access", Name: "Full Access", NameZh: "完全访问", Desc: "No sandbox restrictions, auto-approve", DescZh: "无沙箱限制,所有操作自动放行"},
		{Key: "confirm", Name: "Confirm", NameZh: "询问确认", Desc: "Full access with write approval", DescZh: "完全访问 + 写入询问(无审批端时等同完全访问)"},
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
	home := dshHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "AGENTS.md")
}

// ── SkillProvider ────────────────────────────────────────────

func (a *Agent) SkillDirs() []string {
	home := dshHomeDir()
	dirs := make([]string, 0, 2)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "skills"))
	}
	if absDir, err := filepath.Abs(a.workDir); err == nil {
		dirs = append(dirs, filepath.Join(absDir, ".dsh", "skills"))
	}
	return dirs
}

// ── Session lifecycle ────────────────────────────────────────

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	return a.StartSessionWithPreset(ctx, sessionID, "")
}

// StartSessionWithPreset carries cc-connect's per-session preset choice to the
// headless runner. dsh itself owns composition and durable selection events;
// this adapter only forwards the requested id.
func (a *Agent) StartSessionWithPreset(ctx context.Context, sessionID, preset string) (core.AgentSession, error) {
	a.mu.Lock()
	mode := a.mode
	model := a.model
	provider := a.provider
	extraArgs := append([]string{}, a.cliExtraArgs...)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	workDir := a.workDir
	a.mu.Unlock()
	return newDSHSession(ctx, a.cmd, extraArgs, workDir, provider, model, mode, preset, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listDSHSessions(a.workDir)
}

func (a *Agent) Stop() error { return nil }
