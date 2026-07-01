package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("acp", New)
}

// Agent runs an ACP (Agent Client Protocol) agent subprocess over stdio JSON-RPC.
type Agent struct {
	workDir      string
	cmd          string
	cliExtraArgs []string // extra args from cmd, prepended before args
	args         []string
	staticEnv    map[string]string
	extraEnv     []string
	sessionEnv   []string
	authMethod   string // optional, e.g. "cursor_login" for Cursor CLI (see authenticate RPC)
	displayName  string // optional, for doctor (default "ACP")

	// mode is the pending permission mode to apply to new sessions.
	// When set, StartSession applies it via the session's SetLiveMode
	// right after session/new. Empty means "use whatever the agent selects
	// by default".
	mode string

	// listUnsupported caches a negative result after we probe the agent
	// for sessionCapabilities.list once. Eliminates spawn cost on
	// subsequent `/ls` invocations against agents that don't implement
	// session/list (e.g. some Copilot/OpenClaw builds).
	listUnsupported atomic.Bool

	// modesCache holds the latest `modes` block we observed via
	// session/new or session/load. It's populated by the session
	// handshake so that future PermissionModes() calls can reflect the
	// actual modes this specific ACP agent offers (rather than a
	// hard-coded fallback that may not match).
	modesMu      sync.RWMutex
	modesCache   []core.PermissionModeInfo
	modesCurrent string

	// modelsCache holds the latest `models` block observed via session/new
	// or session/load. availableModels feeds the `/model` picker;
	// currentModel reflects the active model. pendingModel holds a
	// user-selected model to apply to future sessions (mirrors `mode`).
	modelsMu        sync.RWMutex
	availableModels []acpModelInfo
	currentModel    string
	pendingModel    string

	// sessionConfigProbed marks whether probeSessionConfig has run once.
	// A single probe fills both the modes and models caches, serving
	// /mode and /model, so an agent advertising neither won't trigger a
	// fresh process spawn on every command.
	sessionConfigProbed atomic.Bool

	mu sync.RWMutex
}

// sessionCallbacks lets a running acpSession report what it learned
// during the handshake back to its parent Agent. The session is owned
// by cc-connect's engine (not the agent), so without this the agent
// would never see availableModes / capability advertisements.
type sessionCallbacks interface {
	reportModes(block acpModesBlock)
	reportModels(block acpModelsBlock)
	reportListSupported(supported bool)
}

// Ensure *Agent satisfies sessionCallbacks at compile time.
var _ sessionCallbacks = (*Agent)(nil)

// New builds an acp agent from project options.
// Required: options["command"] — executable name or path for the ACP agent.
// Optional: options["args"], options["env"], options["auth_method"],
// options["display_name"], options["mode"].
func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	cmdStr, cliExtraArgs := core.ParseCmdOpts(opts, "")
	if cmdStr == "" {
		return nil, fmt.Errorf("acp: agent option \"cmd\" or \"command\" is required (path or name of the ACP agent binary)")
	}
	if _, err := exec.LookPath(cmdStr); err != nil {
		return nil, fmt.Errorf("acp: command %q not found in PATH: %w", cmdStr, err)
	}

	args := parseStringSlice(opts["args"])
	staticEnv := envMapFromOpts(opts)
	extra := envPairsFromOpts(opts)
	authMethod, _ := opts["auth_method"].(string)
	authMethod = strings.TrimSpace(authMethod)
	displayName, _ := opts["display_name"].(string)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "ACP"
	}
	mode, _ := opts["mode"].(string)
	mode = strings.TrimSpace(mode)

	return &Agent{
		workDir:      workDir,
		cmd:          cmdStr,
		cliExtraArgs: cliExtraArgs,
		args:         args,
		staticEnv:    staticEnv,
		extraEnv:     extra,
		authMethod:   authMethod,
		displayName:  displayName,
		mode:         mode,
	}, nil
}

func envMapFromOpts(opts map[string]any) map[string]string {
	raw, ok := opts["env"]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
		return out
	default:
		return nil
	}
}

func envPairsFromOpts(opts map[string]any) []string {
	raw, ok := opts["env"]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		var out []string
		for k, v := range m {
			out = append(out, k+"="+v)
		}
		return out
	case map[string]any:
		var out []string
		for k, v := range m {
			out = append(out, fmt.Sprintf("%s=%v", k, v))
		}
		return out
	default:
		return nil
	}
}

func parseStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			switch t := e.(type) {
			case string:
				out = append(out, t)
			default:
				out = append(out, fmt.Sprint(t))
			}
		}
		return out
	default:
		return nil
	}
}

func (a *Agent) Name() string { return "acp" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	a.workDir = dir
	a.mu.Unlock()
	slog.Info("acp: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	opts := map[string]any{
		"cmd": a.cmd,
	}
	if len(a.args) > 0 {
		opts["args"] = append([]string(nil), a.args...)
	}
	if len(a.staticEnv) > 0 {
		env := make(map[string]string, len(a.staticEnv))
		for k, v := range a.staticEnv {
			env[k] = v
		}
		opts["env"] = env
	}
	if a.authMethod != "" {
		opts["auth_method"] = a.authMethod
	}
	if a.displayName != "" {
		opts["display_name"] = a.displayName
	}
	return opts
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	a.sessionEnv = env
	a.mu.Unlock()
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	command := a.cmd
	allArgs := append(append([]string{}, a.cliExtraArgs...), a.args...)
	workDir := a.workDir
	authMethod := a.authMethod
	pendingMode := a.mode
	extra := append([]string(nil), a.extraEnv...)
	extra = append(extra, a.sessionEnv...)
	a.mu.RUnlock()

	return newACPSession(ctx, acpSessionConfig{
		command:         command,
		args:            allArgs,
		extraEnv:        extra,
		workDir:         workDir,
		resumeSessionID: sessionID,
		authMethod:      authMethod,
		initialMode:     pendingMode,
		callbacks:       a,
	})
}

func (a *Agent) Stop() error { return nil }

// -- AgentDoctorInfo --

func (a *Agent) CLIBinaryName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return filepath.Base(a.cmd)
}

func (a *Agent) CLIDisplayName() string {
	a.mu.RLock()
	n := a.displayName
	a.mu.RUnlock()
	if n == "" {
		return "ACP"
	}
	return n
}

// -- ModeSwitcher --
//
// cc-connect's engine treats ModeSwitcher as the point of truth for
// both displaying `/mode` options and applying a mode selection. For
// the generic ACP adapter we keep the Key == ACP modeId so downstream
// `session/set_mode` calls don't need any translation.

// SetMode stores a permission mode to apply to future sessions started
// via StartSession. If the caller-provided mode matches a known cached
// mode id (case-insensitive), it is normalised to that id. Otherwise
// it is stored as-is — some IM users may configure modes before the
// agent has started any session and thus advertised its mode list.
func (a *Agent) SetMode(mode string) {
	normalised := mode
	if m := a.matchModeID(mode); m != "" {
		normalised = m
	}
	a.mu.Lock()
	a.mode = normalised
	a.mu.Unlock()
	slog.Info("acp: mode changed for future sessions", "mode", normalised)
}

// GetMode returns the mode cc-connect will treat as "current" when
// rendering the `/mode` picker or applying SetLiveMode.
//
// Precedence: the most recent explicit SetMode wins (that's the user's
// intent — `/mode plan` should immediately be reflected in the next
// `/mode` listing even before the session/set_mode RPC has returned).
// Only if no one has ever called SetMode for this Agent do we fall
// back to whatever the server advertised as currentModeId during the
// last handshake.
func (a *Agent) GetMode() string {
	a.mu.RLock()
	pending := a.mode
	a.mu.RUnlock()
	if pending != "" {
		return pending
	}
	a.modesMu.RLock()
	defer a.modesMu.RUnlock()
	return a.modesCurrent
}

// PermissionModes returns the modes this ACP agent offers. The list is
// populated from the modes observed on session/new or session/load —
// either the `modes` block or a configOptions mode selector. If nothing
// has been observed yet (no session started), it triggers a one-time
// probe so `/mode` can list options without requiring the user to start a
// session first — this mirrors AvailableModels for `/model`.
//
// ACP doesn't send per-mode Desc/NameZh, so Description (if the server
// sent one) maps to Desc for both locales. IM-side translators are
// free to map well-known ids to localised strings later.
func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	a.modesMu.RLock()
	empty := len(a.modesCache) == 0
	a.modesMu.RUnlock()

	if empty {
		a.ensureSessionConfigProbed()
	}

	a.modesMu.RLock()
	defer a.modesMu.RUnlock()
	out := make([]core.PermissionModeInfo, len(a.modesCache))
	copy(out, a.modesCache)
	return out
}

// matchModeID returns the canonical mode id for a user-typed string
// (case-insensitive match on id or display name). Empty string if no
// match or if we haven't observed modes yet.
func (a *Agent) matchModeID(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	a.modesMu.RLock()
	defer a.modesMu.RUnlock()
	for _, m := range a.modesCache {
		if strings.ToLower(m.Key) == lower || strings.ToLower(m.Name) == lower {
			return m.Key
		}
	}
	return ""
}

// -- sessionCallbacks impl --

func (a *Agent) reportModes(block acpModesBlock) {
	infos := make([]core.PermissionModeInfo, 0, len(block.AvailableModes))
	for _, m := range block.AvailableModes {
		infos = append(infos, core.PermissionModeInfo{
			Key:    m.ID,
			Name:   m.Name,
			NameZh: m.Name,
			Desc:   m.Description,
			DescZh: m.Description,
		})
	}
	a.modesMu.Lock()
	a.modesCache = infos
	a.modesCurrent = block.CurrentModeID
	a.modesMu.Unlock()
}

func (a *Agent) reportListSupported(supported bool) {
	if !supported {
		a.listUnsupported.Store(true)
	} else {
		a.listUnsupported.Store(false)
	}
}

func (a *Agent) reportModels(block acpModelsBlock) {
	a.modelsMu.Lock()
	a.availableModels = append(a.availableModels[:0], block.AvailableModels...)
	if block.CurrentModelID != "" {
		a.currentModel = block.CurrentModelID
	}
	a.modelsMu.Unlock()
}

// -- ModelSwitcher implementation --
//
// Models come from either the `models` block or a configOptions entry with
// category "model" (both are normalised into the same cache during the
// handshake / probe).

// SetModel records the user-selected model as the preference for future
// sessions. For a running session the engine separately calls the
// session's SetLiveModel to switch live.
func (a *Agent) SetModel(model string) {
	a.modelsMu.Lock()
	a.pendingModel = model
	// Optimistically update currentModel so GetModel / rendering reflect
	// the user's intent immediately.
	if model != "" {
		a.currentModel = model
	}
	a.modelsMu.Unlock()
	slog.Info("acp: model changed", "model", model)
}

// GetModel returns the current model. The most recent SetModel
// (pendingModel, the user's intent) wins; otherwise it falls back to the
// currentModel the server reported during the handshake. This mirrors
// GetMode's precedence logic.
func (a *Agent) GetModel() string {
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	if a.pendingModel != "" {
		return a.pendingModel
	}
	return a.currentModel
}

// AvailableModels returns the models this ACP agent advertised, whether
// via the `models` block or a configOptions model selector. When the
// cache is empty (no session started yet), it triggers a one-time probe
// to fetch them.
//
// Note: the ctx argument only satisfies the core.ModelSwitcher
// interface; the probe uses its own timeout (see probeSessionConfig) and
// is therefore not bounded by the caller's short ctx (cmdModel's 10s).
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	_ = ctx
	a.modelsMu.RLock()
	empty := len(a.availableModels) == 0
	a.modelsMu.RUnlock()

	if empty {
		a.ensureSessionConfigProbed()
	}

	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	if len(a.availableModels) == 0 {
		return nil
	}
	out := make([]core.ModelOption, 0, len(a.availableModels))
	for _, m := range a.availableModels {
		out = append(out, core.ModelOption{Name: m.ModelID, Desc: m.Description})
	}
	return out
}

// ensureSessionConfigProbed triggers a one-time probe (initialize +
// session/new) when no session has advertised the session config yet.
// A single probe fills both the modes and models caches, so /mode and
// /model both benefit. Subsequent calls are cheap no-ops once probed.
func (a *Agent) ensureSessionConfigProbed() {
	if a.sessionConfigProbed.Load() {
		return
	}
	// Nothing to spawn without a command (e.g. a zero-value Agent used in
	// unit tests). Skip the probe rather than attempting a doomed exec.
	a.mu.RLock()
	cmd := a.cmd
	a.mu.RUnlock()
	if cmd == "" {
		return
	}
	a.probeSessionConfig()
}

// probeSessionConfig spawns a short-lived ACP process, runs
// initialize + session/new to obtain the mode and model selectors (from
// the `modes`/`models` blocks or configOptions), then shuts it down.
// Used to answer /model and /mode when no session is
// active.
//
// The probe uses sessionConfigProbeTimeout rather than inheriting the
// caller's possibly-short ctx (cmdModel/cmdMode only pass 10s). Some
// agents (e.g. kiro CLI) initialize multiple MCP servers on startup and
// can take longer than 10s, which would otherwise surface as
// "probe initialize/session/new: context deadline exceeded". The child
// process is reaped by teardown, so at worst the probe ends itself when
// the timeout fires.
func (a *Agent) probeSessionConfig() {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absWorkDir, _ := filepath.Abs(workDir)
	if absWorkDir == "" {
		absWorkDir = workDir
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), sessionConfigProbeTimeout)
	defer cancel()

	tr, _, teardown, err := a.probeSpawn(probeCtx, absWorkDir)
	if err != nil {
		slog.Warn("acp: probeSessionConfig spawn failed", "error", err)
		return
	}
	defer teardown()

	if _, err := probeInitialize(probeCtx, tr); err != nil {
		slog.Warn("acp: probeSessionConfig initialize failed", "error", err)
		return
	}

	newRes, err := tr.call(probeCtx, "session/new", map[string]any{
		"cwd":        absWorkDir,
		"mcpServers": []any{},
	})
	if err != nil {
		slog.Warn("acp: probeSessionConfig session/new failed", "error", err)
		return
	}
	// session/new completed, so mark as probed to avoid respawning.
	a.sessionConfigProbed.Store(true)
	var sn struct {
		SessionID     string            `json:"sessionId"`
		Modes         *acpModesBlock    `json:"modes"`
		Models        *acpModelsBlock   `json:"models"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(newRes, &sn) != nil {
		return
	}
	// Mechanism A: top-level models/modes blocks.
	if sn.Models != nil && len(sn.Models.AvailableModels) > 0 {
		a.reportModels(*sn.Models)
		slog.Info("acp: probeSessionConfig fetched models", "count", len(sn.Models.AvailableModels))
	}
	if sn.Modes != nil && len(sn.Modes.AvailableModes) > 0 {
		a.reportModes(*sn.Modes)
		slog.Info("acp: probeSessionConfig fetched modes", "count", len(sn.Modes.AvailableModes))
	}
	// Mechanism B: configOptions (model/mode selectors). The probe only
	// needs to populate the agent-level caches so /model and /mode can
	// list options; the configId used for live switching is recorded by
	// the real session's absorbConfigOptions.
	for i := range sn.ConfigOptions {
		opt := sn.ConfigOptions[i]
		switch opt.Category {
		case "model":
			if models := flattenModelOptions(opt); len(models) > 0 {
				a.reportModels(acpModelsBlock{CurrentModelID: opt.CurrentValue, AvailableModels: models})
				slog.Info("acp: probeSessionConfig fetched model configOption", "count", len(models))
			}
		case "mode":
			if modes := flattenModeOptions(opt); len(modes) > 0 {
				a.reportModes(acpModesBlock{CurrentModeID: opt.CurrentValue, AvailableModes: modes})
				slog.Info("acp: probeSessionConfig fetched mode configOption", "count", len(modes))
			}
		}
	}
}
