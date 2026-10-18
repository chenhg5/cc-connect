package acp

import (
	"context"
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
	// When set, StartSession applies it via session/set_mode right after
	// session/new. Empty means "use whatever the agent selects by default".
	mode string

	// listUnsupported caches a negative result after we probe the agent
	// for sessionCapabilities.list once. Eliminates spawn cost on
	// subsequent `/ls` invocations against agents that don't implement
	// session/list (e.g. some Copilot/OpenClaw builds).
	listUnsupported atomic.Bool

	// deleteUnsupported caches a negative sessionCapabilities.delete probe.
	// Zero value means "unknown" — DeleteSession still probes once.
	deleteUnsupported atomic.Bool

	// model is the configured model applied to the next session.
	// Live sessions keep whatever the agent already reported.
	model string

	// modeAliases maps user-typed mode names to canonical ACP mode ids.
	// Wrappers (Devin) fill this so legacy values such as "normal" match
	// the ids the agent actually advertises. Empty for generic ACP.
	modeAliases map[string]string

	// ModelValueResolver maps a configured model onto a value the
	// session's model config option actually accepts. impliedEffort is
	// the effort suffix the input carried, without a leading dash.
	// Nil means only an exact option value is sent.
	ModelValueResolver func(input string, optionValues []string) (value string, impliedEffort string)

	// CompactCommand is the prompt that should wait for vendor
	// compaction before the turn result is emitted. "/compact" is always
	// included; this field adds the agent's own compress command.
	// Waiting is skipped when CompactionNotifyMethod is empty.
	CompactCommand string

	// CompactionNotifyMethod is the vendor notification that reports async context compaction
	// progress ({"status":"started"|"completed"|"failed"}). Empty disables compaction tracking.
	CompactionNotifyMethod string

	// FallbackReasoningEfforts is used by AvailableReasoningEfforts
	// until a session advertises a thought_level option.
	FallbackReasoningEfforts []string

	// ProbeCommand, when set, replaces cmd (and cliExtraArgs/args) for the
	// short-lived probe processes behind session/list, history and delete.
	// Wrappers use it so a slow wrapper start-up cannot time out probes.
	ProbeCommand string
	ProbeArgs    []string

	// ShowModelDisplayName makes sessions report the model option's display
	// name (e.g. "Claude Opus 5.5") for the reply footer instead of its id.
	ShowModelDisplayName bool

	// reasoningEffort is the configured effort applied to future
	// sessions. It is not the thought_level the last session reported.
	reasoningEffort string

	// configuredModels is the static "models" option from config.
	// AvailableModels uses it only when no session has advertised options.
	configuredModels []core.ModelOption

	// cachedModelOptions is the model list from the latest session
	// (configOptions category "model", or the legacy models block).
	// lastReportedModel is that session's current model id. It is not
	// the configured model and is not written back to WorkspaceAgentOptions.
	modelsMu           sync.RWMutex
	cachedModelOptions []core.ModelOption
	lastReportedModel  string
	lastThoughtLevel   string
	thoughtLevelValues []string

	titlesMu sync.RWMutex
	titles   map[string]string

	// modesCache holds the latest `modes` block we observed via
	// session/new or session/load. It's populated by the session
	// handshake so that future PermissionModes() calls can reflect the
	// actual modes this specific ACP agent offers (rather than a
	// hard-coded fallback that may not match).
	modesMu      sync.RWMutex
	modesCache   []core.PermissionModeInfo
	modesCurrent string
	modeDesc     map[string]string

	mu sync.RWMutex
}

// sessionCallbacks lets a running acpSession report what it learned
// during the handshake back to its parent Agent. The session is owned
// by cc-connect's engine (not the agent), so without this the agent
// would never see availableModes / capability advertisements.
type sessionCallbacks interface {
	reportModes(block acpModesBlock)
	reportListSupported(supported bool)
	reportDeleteSupported(supported bool)
	reportModelOptions(current string, options []core.ModelOption)
	reportThoughtLevel(current string, values []string)
	reportModeDescriptions(byID map[string]string)
	reportSessionTitle(sessionID, title string)
}

// Ensure *Agent satisfies the optional core interfaces Phase A adds,
// plus the callback surface sessions use to publish discoveries.
var (
	_ sessionCallbacks              = (*Agent)(nil)
	_ core.ModelSwitcher            = (*Agent)(nil)
	_ core.ReasoningEffortSwitcher  = (*Agent)(nil)
	_ core.StartupWarner            = (*acpSession)(nil)
	_ core.SessionDeleter           = (*Agent)(nil)
	_ core.HistoryProvider          = (*Agent)(nil)
	_ core.SessionIDValidator       = (*Agent)(nil)
	_ core.SessionTitleProvider     = (*Agent)(nil)
	_ core.ContextUsageReporter     = (*acpSession)(nil)
	_ core.UsageSourceReporter      = (*acpSession)(nil)
	_ core.ModelDisplayNameReporter = (*acpSession)(nil)
)

// New builds an acp agent from project options.
// Required: options["command"] — executable name or path for the ACP agent.
// Optional: options["args"], options["env"], options["auth_method"],
// options["display_name"], options["mode"], options["mode_aliases"],
// options["model"], options["models"] (provider-style {model, alias}
// or {name, desc} entries), options["compact_command"],
// options["compaction_notify_method"]. mode_aliases values rewrite mode
// before it is stored, so "normal" can be saved as "accept-edits".
// An empty compaction_notify_method disables compaction tracking.
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
	aliases := parseModeAliases(opts["mode_aliases"])
	if aliased := lookupModeAlias(aliases, mode); aliased != "" {
		mode = aliased
	}
	model, _ := opts["model"].(string)
	model = strings.TrimSpace(model)
	effort, _ := opts["reasoning_effort"].(string)
	effort = strings.TrimSpace(effort)
	compact, _ := opts["compact_command"].(string)
	compact = strings.TrimSpace(compact)
	notify, _ := opts["compaction_notify_method"].(string)
	notify = strings.TrimSpace(notify)

	return &Agent{
		workDir:                workDir,
		cmd:                    cmdStr,
		cliExtraArgs:           cliExtraArgs,
		args:                   args,
		staticEnv:              staticEnv,
		extraEnv:               extra,
		authMethod:             authMethod,
		displayName:            displayName,
		mode:                   mode,
		modeAliases:            aliases,
		model:                  model,
		reasoningEffort:        effort,
		CompactCommand:         compact,
		CompactionNotifyMethod: notify,
		configuredModels:       parseConfiguredModels(opts["models"]),
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
	if a.model != "" {
		opts["model"] = a.model
	}
	if a.reasoningEffort != "" {
		opts["reasoning_effort"] = a.reasoningEffort
	}
	if a.CompactCommand != "" {
		opts["compact_command"] = a.CompactCommand
	}
	if a.CompactionNotifyMethod != "" {
		opts["compaction_notify_method"] = a.CompactionNotifyMethod
	}
	if len(a.modeAliases) > 0 {
		opts["mode_aliases"] = cloneModeAliases(a.modeAliases)
	}
	if len(a.configuredModels) > 0 {
		opts["models"] = append([]core.ModelOption(nil), a.configuredModels...)
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
	pendingModel := a.model
	pendingEffort := a.reasoningEffort
	resolver := a.ModelValueResolver
	compactCommand := a.CompactCommand
	compactionNotify := a.CompactionNotifyMethod
	showModelName := a.ShowModelDisplayName
	pendingAliases := cloneModeAliases(a.modeAliases)
	extra := append([]string(nil), a.extraEnv...)
	extra = append(extra, a.sessionEnv...)
	a.mu.RUnlock()

	return newACPSession(ctx, acpSessionConfig{
		command:                command,
		args:                   allArgs,
		extraEnv:               extra,
		workDir:                workDir,
		resumeSessionID:        sessionID,
		authMethod:             authMethod,
		initialMode:            pendingMode,
		initialModel:           pendingModel,
		initialEffort:          pendingEffort,
		modelResolver:          resolver,
		compactCommand:         compactCommand,
		compactionNotifyMethod: compactionNotify,
		showModelName:          showModelName,
		modeAliases:            pendingAliases,
		callbacks:              a,
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

// matchModeID returns the canonical mode id for a user-typed string.
// Aliases apply even before the first handshake. Otherwise the match is
// case-insensitive on id or display name, and empty if nothing matches.
func (a *Agent) matchModeID(input string) string {
	a.mu.RLock()
	aliases := a.modeAliases
	a.mu.RUnlock()
	a.modesMu.RLock()
	modes := make([]coreMode, len(a.modesCache))
	for i, m := range a.modesCache {
		modes[i] = coreMode{Key: m.Key, Name: m.Name}
	}
	a.modesMu.RUnlock()
	return matchModeWithAliases(input, aliases, modes)
}

// -- sessionCallbacks impl --

func (a *Agent) reportModes(block acpModesBlock) {
	a.modesMu.Lock()
	defer a.modesMu.Unlock()
	infos := make([]core.PermissionModeInfo, 0, len(block.AvailableModes))
	for _, m := range block.AvailableModes {
		desc := strings.TrimSpace(m.Description)
		if desc == "" {
			desc = lookupModeDesc(a.modeDesc, m.ID)
		}
		infos = append(infos, core.PermissionModeInfo{
			Key:    m.ID,
			Name:   m.Name,
			NameZh: m.Name,
			Desc:   desc,
			DescZh: desc,
		})
	}
	a.modesCache = infos
	a.modesCurrent = block.CurrentModeID
}

func (a *Agent) reportListSupported(supported bool) {
	a.listUnsupported.Store(!supported)
}

func (a *Agent) reportDeleteSupported(supported bool) {
	a.deleteUnsupported.Store(!supported)
}

func (a *Agent) reportModelOptions(current string, options []core.ModelOption) {
	current = strings.TrimSpace(current)
	a.modelsMu.Lock()
	defer a.modelsMu.Unlock()
	if current != "" {
		a.lastReportedModel = current
	}
	if len(options) == 0 {
		return
	}
	a.cachedModelOptions = append([]core.ModelOption(nil), options...)
}

// RememberReportedModel records a model id advertised by a session.
// The live ACP session does this through reportModelOptions; tests and
// wrappers can call this when they only have the id.
func (a *Agent) RememberReportedModel(model string) {
	a.reportModelOptions(model, nil)
}

// RememberModelOptions records the model choices a session advertised.
// Empty options do not clear a list already stored.
func (a *Agent) RememberModelOptions(current string, options []core.ModelOption) {
	a.reportModelOptions(current, options)
}

// LastReportedModel returns the model id most recently advertised by a
// session. Empty until a session reports one.
func (a *Agent) LastReportedModel() string {
	if a == nil {
		return ""
	}
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	return a.lastReportedModel
}

func (a *Agent) reportThoughtLevel(current string, values []string) {
	current = strings.TrimSpace(current)
	a.modelsMu.Lock()
	defer a.modelsMu.Unlock()
	if current != "" {
		a.lastThoughtLevel = current
	}
	if len(values) == 0 {
		return
	}
	cleaned := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			cleaned = append(cleaned, v)
		}
	}
	if len(cleaned) == 0 {
		return
	}
	a.thoughtLevelValues = cleaned
}

func (a *Agent) reportModeDescriptions(byID map[string]string) {
	if len(byID) == 0 {
		return
	}
	a.modesMu.Lock()
	defer a.modesMu.Unlock()
	if a.modeDesc == nil {
		a.modeDesc = make(map[string]string, len(byID))
	}
	for id, desc := range byID {
		id = strings.TrimSpace(id)
		desc = strings.TrimSpace(desc)
		if id == "" || desc == "" {
			continue
		}
		a.modeDesc[id] = desc
	}
	for i := range a.modesCache {
		d := lookupModeDesc(a.modeDesc, a.modesCache[i].Key)
		if d == "" {
			continue
		}
		a.modesCache[i].Desc = d
		a.modesCache[i].DescZh = d
	}
}

func lookupModeDesc(descs map[string]string, key string) string {
	key = strings.TrimSpace(key)
	if key == "" || len(descs) == 0 {
		return ""
	}
	if d := strings.TrimSpace(descs[key]); d != "" {
		return d
	}
	for id, desc := range descs {
		if strings.EqualFold(strings.TrimSpace(id), key) {
			return strings.TrimSpace(desc)
		}
	}
	return ""
}

func (a *Agent) reportSessionTitle(sessionID, title string) {
	a.rememberTitle(sessionID, title)
}
