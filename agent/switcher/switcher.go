package switcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("switcher", New)
	core.RegisterAgentModelConfigSaver("switcher", saveModelConfig)
}

type backendSpec struct {
	name  string
	typ   string
	opts  map[string]any
	agent core.Agent
}

// Agent multiplexes multiple agent backends behind a single cc-connect project.
// /agent switches the active backend (e.g. Claude Code vs Codex), while /model
// delegates to the active backend's own model switcher.
type Agent struct {
	workDir         string
	mode            string
	currentAgent    string
	reasoningEffort string

	backends map[string]*backendSpec
	order    []string

	sessionEnv     []string
	platformPrompt string
	mu             sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir := stringOpt(opts, "work_dir", ".")
	mode := normalizeMode(stringOpt(opts, "mode", "default"))
	defaultBackend := strings.TrimSpace(stringOpt(opts, "agent", ""))
	reasoningEffort := strings.TrimSpace(stringOpt(opts, "reasoning_effort", ""))

	backendsRaw, ok := opts["backends"]
	if !ok {
		return nil, fmt.Errorf("switcher: [projects.agent.options.backends] is required")
	}

	backends, order, err := parseBackendSpecs(backendsRaw)
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("switcher: at least one backend is required")
	}

	if defaultBackend == "" {
		defaultBackend = order[0]
	}
	if _, ok := backends[defaultBackend]; !ok {
		return nil, fmt.Errorf("switcher: default backend %q not found in backends", defaultBackend)
	}

	return &Agent{
		workDir:         workDir,
		mode:            mode,
		currentAgent:    defaultBackend,
		reasoningEffort: reasoningEffort,
		backends:        backends,
		order:           order,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto-edit", "autoedit", "auto_edit", "edit":
		return "auto-edit"
	case "full-auto", "fullauto", "full_auto", "auto":
		return "full-auto"
	case "yolo", "bypass", "dangerously-bypass":
		return "yolo"
	default:
		return "suggest"
	}
}

func stringOpt(opts map[string]any, key, fallback string) string {
	if v, ok := opts[key].(string); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return fallback
}

func parseBackendSpecs(raw any) (map[string]*backendSpec, []string, error) {
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case []map[string]any:
		items = make([]any, len(v))
		for i := range v {
			items[i] = v[i]
		}
	default:
		return nil, nil, fmt.Errorf("switcher: backends must be an array of tables")
	}

	backends := make(map[string]*backendSpec, len(items))
	order := make([]string, 0, len(items))
	for i, item := range items {
		specMap, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("switcher: backends[%d] must be a table", i)
		}
		spec := cloneOptions(specMap)
		name := strings.TrimSpace(stringFromMap(spec, "name"))
		if name == "" {
			return nil, nil, fmt.Errorf("switcher: backends[%d].name is required", i)
		}
		typ := strings.TrimSpace(stringFromMap(spec, "type"))
		if typ == "" {
			return nil, nil, fmt.Errorf("switcher: backends[%d].type is required", i)
		}
		if _, exists := backends[name]; exists {
			return nil, nil, fmt.Errorf("switcher: duplicate backend name %q", name)
		}
		backends[name] = &backendSpec{name: name, typ: typ, opts: spec}
		order = append(order, name)
	}
	return backends, order, nil
}

func saveModelConfig(options map[string]any, model string) error {
	if options == nil {
		return fmt.Errorf("switcher agent has no options")
	}
	backendName, _ := options["agent"].(string)
	backendName = strings.TrimSpace(backendName)
	if backendName == "" {
		return fmt.Errorf("switcher agent has no active backend")
	}
	rawBackends, ok := options["backends"]
	if !ok {
		return fmt.Errorf("switcher agent has no backends")
	}

	matchBackend := func(m map[string]any) bool {
		if m == nil {
			return false
		}
		name, _ := m["name"].(string)
		return strings.EqualFold(strings.TrimSpace(name), backendName)
	}

	switch backends := rawBackends.(type) {
	case []any:
		for i := range backends {
			spec, ok := backends[i].(map[string]any)
			if !ok || !matchBackend(spec) {
				continue
			}
			spec["model"] = model
			options["backends"] = backends
			return nil
		}
	case []map[string]any:
		for i := range backends {
			if !matchBackend(backends[i]) {
				continue
			}
			backends[i]["model"] = model
			options["backends"] = backends
			return nil
		}
	default:
		return fmt.Errorf("switcher agent backends have unexpected type %T", rawBackends)
	}

	return fmt.Errorf("backend %q not found in switcher config", backendName)
}

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func cloneOptions(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return cloneOptions(vv)
	case []any:
		out := make([]any, len(vv))
		for i := range vv {
			out[i] = cloneValue(vv[i])
		}
		return out
	case []map[string]any:
		out := make([]any, len(vv))
		for i := range vv {
			out[i] = cloneOptions(vv[i])
		}
		return out
	default:
		return v
	}
}

func (a *Agent) Name() string { return "switcher" }

func (a *Agent) SnapshotOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	backends := make([]any, 0, len(a.order))
	for _, name := range a.order {
		if spec := a.backends[name]; spec != nil {
			opts := cloneOptions(spec.opts)
			if spec.agent != nil {
				if snapshotter, ok := spec.agent.(core.OptionSnapshotter); ok {
					for k, v := range snapshotter.SnapshotOptions() {
						if opts == nil {
							opts = make(map[string]any)
						}
						opts[k] = v
					}
				}
			}
			backends = append(backends, opts)
		}
	}

	snapshot := map[string]any{
		"work_dir":         a.workDir,
		"mode":             a.mode,
		"agent":            a.currentAgent,
		"reasoning_effort": a.reasoningEffort,
		"backends":         backends,
	}
	return snapshot
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	for _, spec := range a.backends {
		if spec.agent != nil {
			if sw, ok := spec.agent.(interface{ SetWorkDir(string) }); ok {
				sw.SetWorkDir(dir)
			}
		}
	}
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
	for _, spec := range a.backends {
		if spec.agent != nil {
			if inj, ok := spec.agent.(core.SessionEnvInjector); ok {
				inj.SetSessionEnv(env)
			}
		}
	}
}

func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.platformPrompt = prompt
	for _, spec := range a.backends {
		if spec.agent != nil {
			if inj, ok := spec.agent.(core.PlatformPromptInjector); ok {
				inj.SetPlatformPrompt(prompt)
			}
		}
	}
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	spec := a.activeSpecLocked()
	if spec == nil {
		slog.Warn("switcher: no active backend to set model on", "model", model)
		return
	}
	if spec.agent == nil {
		if _, err := a.ensureBackendLocked(spec.name); err != nil {
			slog.Warn("switcher: failed to create active backend for model switch", "backend", spec.name, "error", err)
			return
		}
	}
	agent := spec.agent
	if agent == nil {
		slog.Warn("switcher: active backend unavailable after creation", "backend", spec.name, "model", model)
		return
	}
	sw, ok := agent.(core.ModelSwitcher)
	if !ok {
		slog.Warn("switcher: active backend does not support model switching", "backend", spec.name, "model", model)
		return
	}
	sw.SetModel(model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	spec := a.activeSpecLocked()
	if spec == nil {
		return ""
	}
	if spec.agent == nil {
		if _, err := a.ensureBackendLocked(spec.name); err != nil {
			return ""
		}
	}
	agent := spec.agent
	if agent == nil {
		return ""
	}
	sw, ok := agent.(core.ModelSwitcher)
	if !ok {
		return ""
	}
	return sw.GetModel()
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	a.mu.Lock()
	defer a.mu.Unlock()
	spec := a.activeSpecLocked()
	if spec == nil {
		return nil
	}
	if spec.agent == nil {
		if _, err := a.ensureBackendLocked(spec.name); err != nil {
			return nil
		}
	}
	agent := spec.agent
	if agent == nil {
		return nil
	}
	sw, ok := agent.(core.ModelSwitcher)
	if !ok {
		return nil
	}
	return sw.AvailableModels(context.Background())
}

func (a *Agent) SetActiveAgent(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.backends[name]; !ok {
		for backendName := range a.backends {
			if strings.EqualFold(backendName, name) {
				name = backendName
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	a.currentAgent = name
	return true
}

func (a *Agent) GetActiveAgent() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.currentAgent != "" {
		return a.currentAgent
	}
	if len(a.order) > 0 {
		return a.order[0]
	}
	return ""
}

func (a *Agent) ListAgents() []core.AgentOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	agents := make([]core.AgentOption, 0, len(a.order))
	for _, name := range a.order {
		spec := a.backends[name]
		if spec == nil {
			continue
		}
		agents = append(agents, core.AgentOption{
			Name: name,
			Desc: a.backendDescLocked(spec),
		})
	}
	return agents
}

func (a *Agent) backendDescLocked(spec *backendSpec) string {
	if spec == nil {
		return ""
	}
	descParts := []string{spec.typ}
	if spec.agent != nil {
		if sw, ok := spec.agent.(core.ModelSwitcher); ok {
			if m := strings.TrimSpace(sw.GetModel()); m != "" {
				descParts = append(descParts, m)
			}
		}
	}
	if len(descParts) == 1 {
		if m, _ := spec.opts["model"].(string); strings.TrimSpace(m) != "" {
			descParts = append(descParts, strings.TrimSpace(m))
		}
	}
	return strings.Join(descParts, " / ")
}

func (a *Agent) activeSpecLocked() *backendSpec {
	if spec := a.backends[a.currentAgent]; spec != nil {
		return spec
	}
	if len(a.order) > 0 {
		return a.backends[a.order[0]]
	}
	return nil
}

func (a *Agent) ensureBackendLocked(name string) (*backendSpec, error) {
	spec, ok := a.backends[name]
	if !ok {
		return nil, fmt.Errorf("switcher: unknown backend %q", name)
	}
	if spec.agent != nil {
		return spec, nil
	}

	opts := cloneOptions(spec.opts)
	if opts == nil {
		opts = make(map[string]any)
	}
	if _, ok := opts["work_dir"]; !ok && a.workDir != "" {
		opts["work_dir"] = a.workDir
	}
	if _, ok := opts["mode"]; !ok && a.mode != "" {
		opts["mode"] = a.mode
	}

	agent, err := core.CreateAgent(spec.typ, opts)
	if err != nil {
		return nil, fmt.Errorf("switcher: create backend %q (%s): %w", name, spec.typ, err)
	}
	spec.agent = agent
	a.applyCommonSettingsLocked(agent)
	return spec, nil
}

func (a *Agent) applyCommonSettingsLocked(agent core.Agent) {
	if sw, ok := agent.(interface{ SetWorkDir(string) }); ok {
		sw.SetWorkDir(a.workDir)
	}
	if ms, ok := agent.(interface{ SetMode(string) }); ok {
		ms.SetMode(a.mode)
	}
	if rs, ok := agent.(interface{ SetReasoningEffort(string) }); ok {
		rs.SetReasoningEffort(a.reasoningEffort)
	}
	if inj, ok := agent.(core.SessionEnvInjector); ok {
		inj.SetSessionEnv(a.sessionEnv)
	}
	if ppi, ok := agent.(core.PlatformPromptInjector); ok {
		ppi.SetPlatformPrompt(a.platformPrompt)
	}
}

func (a *Agent) activeAgent(ctx context.Context) (core.Agent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	spec, err := a.ensureBackendLocked(a.currentAgent)
	if err != nil {
		return nil, err
	}
	if spec.agent == nil {
		return nil, fmt.Errorf("switcher: backend %q is unavailable", a.currentAgent)
	}
	return spec.agent, nil
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	agent, err := a.activeAgent(ctx)
	if err != nil {
		return nil, err
	}
	return agent.StartSession(ctx, sessionID)
}

func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	agent, err := a.activeAgent(ctx)
	if err != nil {
		return nil, err
	}
	return agent.ListSessions(ctx)
}

func (a *Agent) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	agent, err := a.activeAgent(ctx)
	if err != nil {
		return nil, err
	}
	if hp, ok := agent.(interface {
		GetSessionHistory(context.Context, string, int) ([]core.HistoryEntry, error)
	}); ok {
		return hp.GetSessionHistory(ctx, sessionID, limit)
	}
	return nil, fmt.Errorf("switcher: active backend does not support session history")
}

func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	agent, err := a.activeAgent(ctx)
	if err != nil {
		return err
	}
	if deleter, ok := agent.(interface {
		DeleteSession(context.Context, string) error
	}); ok {
		return deleter.DeleteSession(ctx, sessionID)
	}
	return fmt.Errorf("switcher: active backend does not support session deletion")
}

func (a *Agent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var errs []string
	for _, name := range a.order {
		spec := a.backends[name]
		if spec == nil || spec.agent == nil {
			continue
		}
		if err := spec.agent.Stop(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("switcher: stop backend(s): %s", strings.Join(errs, "; "))
	}
	return nil
}

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	for _, spec := range a.backends {
		if spec.agent != nil {
			if ms, ok := spec.agent.(interface{ SetMode(string) }); ok {
				ms.SetMode(a.mode)
			}
		}
	}
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningEffort = strings.TrimSpace(effort)
	for _, spec := range a.backends {
		if spec.agent != nil {
			if rs, ok := spec.agent.(interface{ SetReasoningEffort(string) }); ok {
				rs.SetReasoningEffort(a.reasoningEffort)
			}
		}
	}
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.reasoningEffort
}

func (a *Agent) AvailableReasoningEfforts() []string {
	a.mu.RLock()
	spec := a.activeSpecLocked()
	a.mu.RUnlock()
	if spec == nil {
		return nil
	}
	if agent, err := a.activeAgent(context.Background()); err == nil {
		if rs, ok := agent.(interface{ AvailableReasoningEfforts() []string }); ok {
			return rs.AvailableReasoningEfforts()
		}
	}
	return nil
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	a.mu.RLock()
	spec := a.activeSpecLocked()
	a.mu.RUnlock()
	if spec == nil {
		return nil
	}
	if agent, err := a.activeAgent(context.Background()); err == nil {
		if ms, ok := agent.(interface {
			PermissionModes() []core.PermissionModeInfo
		}); ok {
			return ms.PermissionModes()
		}
	}
	return nil
}

func (a *Agent) SetSessionEnvAndPrompt(env []string, prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
	a.platformPrompt = prompt
}

func (a *Agent) CommandDirs() []string {
	return a.dualDirs("commands")
}

func (a *Agent) SkillDirs() []string {
	return a.dualDirs("skills")
}

func (a *Agent) dualDirs(kind string) []string {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}

	dirs := []string{
		filepath.Join(absDir, ".claude", kind),
		filepath.Join(absDir, ".codex", kind),
	}

	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".claude", kind),
			filepath.Join(home, ".codex", kind),
		)
	}

	return dedupeDirs(dirs)
}

func dedupeDirs(dirs []string) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = filepath.Clean(d)
		if d == "." || d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (a *Agent) CompressCommand() string { return "/compact" }

func (a *Agent) ProjectMemoryFile() string {
	a.mu.RLock()
	spec := a.activeSpecLocked()
	workDir := a.workDir
	a.mu.RUnlock()

	if spec != nil {
		switch spec.typ {
		case "codex":
			absDir, err := filepath.Abs(workDir)
			if err != nil {
				absDir = workDir
			}
			return filepath.Join(absDir, "AGENTS.md")
		default:
			absDir, err := filepath.Abs(workDir)
			if err != nil {
				absDir = workDir
			}
			return filepath.Join(absDir, "CLAUDE.md")
		}
	}

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "CLAUDE.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	a.mu.RLock()
	spec := a.activeSpecLocked()
	a.mu.RUnlock()
	if spec != nil && spec.typ == "codex" {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(homeDir, ".codex")
		}
		return filepath.Join(codexHome, "AGENTS.md")
	}
	return filepath.Join(homeDir, ".claude", "CLAUDE.md")
}

func (a *Agent) HasSystemPromptSupport() bool {
	a.mu.RLock()
	spec := a.activeSpecLocked()
	a.mu.RUnlock()
	if spec == nil {
		return false
	}
	return spec.typ == "claudecode"
}
