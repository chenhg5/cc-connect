package acp

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
	model        string // optional, for Trae CLI ACP only

	// mode is the pending permission mode to apply to new sessions.
	// When set, StartSession applies it via session/set_mode right after
	// session/new. Empty means "use whatever the agent selects by default".
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

	mu sync.RWMutex
}

// sessionCallbacks lets a running acpSession report what it learned
// during the handshake back to its parent Agent. The session is owned
// by cc-connect's engine (not the agent), so without this the agent
// would never see availableModes / capability advertisements.
type sessionCallbacks interface {
	reportModes(block acpModesBlock)
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
	model, _ := opts["model"].(string)
	model = strings.TrimSpace(model)

	agent := &Agent{
		workDir:      workDir,
		cmd:          cmdStr,
		cliExtraArgs: cliExtraArgs,
		args:         args,
		staticEnv:    staticEnv,
		extraEnv:     extra,
		authMethod:   authMethod,
		displayName:  displayName,
		mode:         mode,
		model:        model,
	}
	if isTraeCLICommand(cmdStr) {
		return &TraeAgent{Agent: agent}, nil
	}
	return agent, nil
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
	if a.model != "" {
		opts["model"] = a.model
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
	if model := a.traeModelOverrideLocked(); model != "" {
		allArgs = append([]string{"-c", fmt.Sprintf("model=%q", model)}, allArgs...)
	}
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

// TraeAgent wraps the generic ACP adapter with Trae CLI-specific model
// discovery and selection. ACP itself has no generic model-switch RPC, but
// Trae exposes `models --json` and accepts `-c model=...` before `acp serve`.
type TraeAgent struct {
	*Agent
}

// -- ModelSwitcher for Trae CLI ACP --
//
// ACP itself does not define a generic model-list/model-set RPC. Trae CLI exposes
// models through its local CLI and accepts model overrides before `acp serve`, so
// cc-connect can provide IM-side model cards for Trae without affecting other ACP
// agents such as Cursor, Copilot, Devin, or OpenClaw.

func (a *TraeAgent) SetModel(model string) {
	model = strings.TrimSpace(model)
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	slog.Info("acp: Trae CLI model changed for future sessions", "model", model)
}

func (a *TraeAgent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

func (a *TraeAgent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.RLock()
	cmd := a.cmd
	extra := append([]string(nil), a.extraEnv...)
	extra = append(extra, a.sessionEnv...)
	workDir := a.workDir
	a.mu.RUnlock()
	return fetchTraeModels(ctx, cmd, workDir, extra)
}

func (a *Agent) traeModelOverrideLocked() string {
	if !isTraeCLICommand(a.cmd) {
		return ""
	}
	return strings.TrimSpace(a.model)
}

func isTraeCLICommand(cmd string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(cmd)))
	return base == "traecli" || base == "traex"
}

type traeModelJSON struct {
	Name        string `json:"name"`
	RealName    string `json:"real_name"`
	Description string `json:"description"`
}

func fetchTraeModels(ctx context.Context, cmd, workDir string, extraEnv []string) []core.ModelOption {
	c := exec.CommandContext(ctx, cmd, "models", "--json")
	c.Dir = workDir
	c.Env = core.MergeEnv(os.Environ(), extraEnv)
	out, err := c.Output()
	if err != nil {
		slog.Debug("acp: Trae CLI models failed", "error", err)
		return nil
	}
	var raw []traeModelJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		slog.Debug("acp: parse Trae CLI models failed", "error", err)
		return parseTraeModelLines(string(out))
	}
	models := make([]core.ModelOption, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, m := range raw {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = strings.TrimSpace(m.RealName)
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		desc := strings.TrimSpace(m.Description)
		if real := strings.TrimSpace(m.RealName); real != "" && !strings.EqualFold(real, name) {
			if desc != "" {
				desc = real + " — " + desc
			} else {
				desc = real
			}
		}
		models = append(models, core.ModelOption{Name: name, Desc: desc})
	}
	return models
}

func parseTraeModelLines(out string) []core.ModelOption {
	var models []core.ModelOption
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "WARNING:") {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, core.ModelOption{Name: name})
	}
	return models
}

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
// populated from the latest `modes.availableModes` observed on
// session/new or session/load; before the first successful handshake
// it returns an empty slice, and the engine will hide the mode picker.
//
// ACP doesn't send per-mode Desc/NameZh, so Description (if the server
// sent one) maps to Desc for both locales. IM-side translators are
// free to map well-known ids to localised strings later.
func (a *Agent) PermissionModes() []core.PermissionModeInfo {
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
