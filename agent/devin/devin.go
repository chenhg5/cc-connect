// Package devin integrates Devin CLI (https://cli.devin.ai/) as a
// first-class cc-connect agent.
//
// Devin speaks the Agent Client Protocol (ACP) over stdio via its
// `devin acp` subcommand, so the transport and session plumbing is
// shared with the generic agent/acp package. This package:
//
//  1. Registers Devin under the stable config name `type = "devin"`.
//  2. Pins Devin-specific defaults — binary name "devin", subcommand
//     "acp", display name "Devin", and mode aliases such as
//     normal -> accept-edits — while leaving ACP options overridable.
//  3. Reports Name() = "devin" so session keys and /doctor attribute
//     activity to Devin rather than the generic "acp" adapter.
//  4. Lists models via `devin models list`, maps a requested model onto
//     an advertised ACP option, sets thought_level after the model, and
//     exposes memory files, skill dirs, and /compact.
//
// Authentication is delegated entirely to the local Devin CLI: after a
// one-time `devin auth login`, the spawned `devin acp` subprocess
// reads the credentials stored on disk, so cc-connect never needs to
// see or forward any API tokens. Windsurf Enterprise users can
// alternatively inject WINDSURF_API_KEY via the agent env option.
package devin

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/agent/acp"
	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("devin", New)
}

// Compile-time checks. Embedding *acp.Agent promotes the ACP methods;
// the assertions below fail if a promoted or overridden method disappears.
var (
	_ core.ModelSwitcher           = (*Agent)(nil)
	_ core.ModelValidator          = (*Agent)(nil)
	_ core.ReasoningEffortSwitcher = (*Agent)(nil)
	_ core.MemoryFileProvider      = (*Agent)(nil)
	_ core.SkillProvider           = (*Agent)(nil)
	_ core.ContextCompressor       = (*Agent)(nil)
	_ core.SessionDeleter          = (*Agent)(nil)
	_ core.HistoryProvider         = (*Agent)(nil)
	_ core.ModeSwitcher            = (*Agent)(nil)
	_ core.WorkDirSwitcher         = (*Agent)(nil)
	_ core.SessionIDValidator      = (*Agent)(nil)
	_ core.SessionTitleProvider    = (*Agent)(nil)
	_ core.AgentDoctorInfo         = (*Agent)(nil)
)

// Agent embeds *acp.Agent so it inherits the ACP session, history,
// delete, and mode implementations. Methods defined here shadow the
// embedded ones when Devin needs its own behavior.
type Agent struct {
	*acp.Agent

	mu        sync.RWMutex
	modelsCmd string
	env       map[string]string

	// modelsRun executes `models list --format json`. Nil uses the
	// real Devin binary. Wrappers and tests inject a runner through
	// SetModelsRunner and never exec devin.
	modelsRun      ModelsRunner
	modelsCache    []core.ModelOption
	modelsCachedAt time.Time
	families       []catalogFamily

	fetchDone chan struct{}
	diskOnce  sync.Once
}

// ModelsRunner returns stdout of `models list --format json`.
// binary is the models CLI (default "devin" on PATH), not the ACP wrapper.
type ModelsRunner func(ctx context.Context, binary string, env []string) ([]byte, error)

// StartSession keeps the catalog refresh running, then opens the ACP session.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.prewarmModels()
	return a.Agent.StartSession(ctx, sessionID)
}

// Name returns the stable agent type identifier used in config,
// session store keys, and audit logging.
func (a *Agent) Name() string { return "devin" }

// CLIBinaryName is the real Devin CLI, even when options.cmd is a
// wrapper script. Doctor looks this name up on PATH.
func (a *Agent) CLIBinaryName() string { return "devin" }

// CompressCommand is Devin's native compaction prompt.
func (a *Agent) CompressCommand() string { return "/compact" }

// New builds a Devin agent from project options.
//
// Option handling:
//   - "command" defaults to "devin".
//   - "args" defaults to ["acp"].
//   - "display_name" defaults to "Devin".
//   - "mode_aliases" is filled with Devin's legacy names; user keys win.
//   - "model", "reasoning_effort", and "models_cmd" configure the model
//     sent to ACP. models_cmd defaults to the `devin` binary on PATH,
//     not the wrapper in "cmd". reasoning_effort is applied as the
//     thought_level config option after the model.
func New(opts map[string]any) (core.Agent, error) {
	opts = applyDevinDefaults(opts)
	rawEffort := strings.TrimSpace(stringOpt(opts["reasoning_effort"]))
	delete(opts, "reasoning_effort")
	a, err := acp.New(opts)
	if err != nil {
		return nil, err
	}
	base, ok := a.(*acp.Agent)
	if !ok {
		return a, nil
	}
	agent := &Agent{
		Agent:     base,
		modelsCmd: strings.TrimSpace(stringOpt(opts["models_cmd"])),
		env:       envMap(opts["env"]),
	}
	agent.FallbackReasoningEfforts = append([]string(nil), devinEfforts...)
	agent.CompactCommand = "/compact"
	agent.CompactionNotifyMethod = "_cognition.ai/compaction"
	agent.ShowModelDisplayName = true
	agent.ModelValueResolver = agent.resolveModelValue
	agent.setProbeCommand(opts)
	if rawEffort != "" {
		agent.SetReasoningEffort(rawEffort)
	}
	agent.prewarmModels()
	return agent, nil
}

// applyDevinDefaults returns opts with Devin-specific defaults filled
// in for any missing or blank fields. The input map is updated in place.
func applyDevinDefaults(opts map[string]any) map[string]any {
	if opts == nil {
		opts = make(map[string]any)
	}
	if existing, _ := opts["command"].(string); strings.TrimSpace(existing) == "" {
		opts["command"] = "devin"
	}
	if _, ok := opts["args"]; !ok {
		opts["args"] = []string{"acp"}
	}
	if existing, _ := opts["display_name"].(string); strings.TrimSpace(existing) == "" {
		opts["display_name"] = "Devin"
	}
	opts["mode_aliases"] = mergeModeAliases(opts["mode_aliases"])
	return opts
}

// PermissionModes returns the modes discovered from the live ACP
// session when that list exists. Before the first handshake it returns
// Devin's four modes with English and Chinese labels.
func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	var live []core.PermissionModeInfo
	if a != nil && a.Agent != nil {
		live = a.Agent.PermissionModes()
	}
	return permissionModesOrStatic(live)
}

func permissionModesOrStatic(live []core.PermissionModeInfo) []core.PermissionModeInfo {
	if len(live) > 0 {
		return live
	}
	return staticPermissionModes()
}

func staticPermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "accept-edits", Name: "Code", NameZh: "编码", Desc: "Write and edit code", DescZh: "编写并修改代码"},
		{Key: "ask", Name: "Ask", NameZh: "问答", Desc: "Answer questions without editing", DescZh: "只回答问题，不修改代码"},
		{Key: "plan", Name: "Plan", NameZh: "计划", Desc: "Plan changes before editing", DescZh: "先规划再修改"},
		{Key: "bypass", Name: "Bypass Permissions", NameZh: "跳过权限确认", Desc: "Skip permission prompts", DescZh: "跳过权限确认"},
	}
}

// WorkspaceAgentOptions round-trips the settings a multi-workspace
// clone needs. model, reasoning_effort, and mode_aliases come from the
// embedded ACP agent and include only configured values. models_cmd is
// Devin's.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	var opts map[string]any
	if a.Agent != nil {
		opts = a.Agent.WorkspaceAgentOptions()
	}
	if opts == nil {
		opts = map[string]any{}
	}
	a.mu.RLock()
	cmd := a.modelsCmd
	a.mu.RUnlock()
	if cmd != "" {
		opts["models_cmd"] = cmd
	}
	return opts
}

func stringOpt(v any) string {
	s, _ := v.(string)
	return s
}

func envMap(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				continue
			}
			out[k] = s
		}
		return out
	default:
		return nil
	}
}

// setProbeCommand points ACP probes at the real `devin acp` when cmd is a
// wrapper (e.g. a script that runs a slow model preflight before starting
// ACP). Probes only list/load/delete sessions and need no wrapper setup.
func (a *Agent) setProbeCommand(opts map[string]any) {
	cmd, _ := core.ParseCmdOpts(opts, "")
	real := a.modelsBinary()
	if !filepath.IsAbs(real) {
		return
	}
	resolved := cmd
	if path, err := exec.LookPath(cmd); err == nil {
		resolved = path
	}
	if resolved == real {
		return
	}
	a.ProbeCommand = real
	a.ProbeArgs = []string{"acp"}
}
