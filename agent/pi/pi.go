package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("pi", New)
}

// Agent drives the pi coding agent CLI.
type Agent struct {
	cmd          string   // path to pi binary
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	workDir      string
	model        string
	mode         string // "default" | "yolo"
	thinking     string // reasoning effort: off, minimal, low, medium, high, xhigh
	rpc          bool   // true = --mode rpc (persistent, extension_ui); false = --mode json (one-shot, default)
	sessionEnv   []string
	mu           sync.Mutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	thinking, _ := opts["thinking"].(string)
	rpc, _ := opts["rpc"].(bool)

	cmd, extraArgs := core.ParseCmdOpts(opts, "pi")

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("pi: '%s' not found in PATH, install with: npm install -g @mariozechner/pi-coding-agent", cmd)
	}

	// If model not specified in opts, try defaultModel from settings.json
	if model == "" {
		if def, err := readDefaultModel(); err == nil && def != "" {
			model = def
		}
	}
	// Canonicalise to "provider/id" so the /model card can mark the current
	// entry, and so pi's --model flag gets an unambiguous reference instead of
	// relying on its own fuzzy fallback (which silently invents a custom model
	// ID when the reference does not resolve).
	model = resolveModelRef(model)

	return &Agent{
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		workDir:      workDir,
		model:        model,
		mode:         mode,
		thinking:     thinking,
		rpc:          rpc,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "bypass", "auto-approve":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "pi" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "Pi" }

// WorkspaceAgentOptions implements core.WorkspaceAgentOptionSnapshotter.
// It returns the user-configured options that must propagate to per-workspace
// agents reconstructed by the engine in multi-workspace mode. work_dir is
// intentionally omitted — the engine sets the target workspace. sessionEnv is
// also omitted (runtime-only). model and mode are copied by the engine via
// GetModel/GetMode, so we don't repeat them here.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	opts := map[string]any{}
	if a.cmd != "" && a.cmd != "pi" {
		opts["cmd"] = a.cmd
	}
	if a.rpc {
		opts["rpc"] = true
	}
	if a.thinking != "" {
		opts["thinking"] = a.thinking
	}
	return opts
}

func (a *Agent) SetModel(model string) {
	model = resolveModelRef(model)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("pi: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	models, err := readSettingsModels()
	if err != nil {
		slog.Debug("pi: AvailableModels: read settings", "error", err)
	}
	if len(models) > 0 {
		return models
	}
	// enabledModels 未配置时，列出 pi 的完整模型目录（models.json 自定义端点 +
	// models-store.json 内置目录），否则 /model 卡片只会显示当前模型、无法列出可
	// 切换的模型列表。此前这里只读 models-store.json，会把用户在 models.json 里
	// 配置的自定义 provider（如 workbuddy/chunfeng）全部漏掉。
	if catalog := readModelCatalog(); len(catalog) > 0 {
		return catalogOptions(catalog)
	}
	return nil
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	mode := a.mode
	model := a.model
	thinking := a.thinking
	extraArgs := append([]string{}, a.cliExtraArgs...)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	// 注入权限模式环境变量，供 permission-gate 扩展读取：yolo（全自动）时扩展
	// 自动放行所有工具，不再弹出权限确认卡片。模式切换会触发会话重建（pi 未实现
	// LiveModeSwitcher），新进程拿到新值。core.InjectedAgentEnv 追加在
	// configEnv/sessionEnv 之后，因此用户显式设置的 CC_PERMISSION_MODE 排在前、
	// 优先生效（getenv 返回第一个匹配项）。
	extraEnv = append(extraEnv, core.InjectedAgentEnv(mode)...)
	rpc := a.rpc
	a.mu.Unlock()
	return newPiSession(ctx, a.cmd, extraArgs, a.workDir, model, mode, thinking, rpc, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("pi: read session dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		sessionID, summary, msgCount := scanPiSession(filepath.Join(sessDir, name))
		if sessionID == "" {
			continue
		}

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return fmt.Errorf("pi: cannot determine session directory")
	}

	path := findSessionFile(sessDir, sessionID)
	if path == "" {
		return fmt.Errorf("pi: session %q not found", sessionID)
	}
	return os.Remove(path)
}

func (a *Agent) Stop() error { return nil }

// ── ModeSwitcher ─────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("pi: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard permissions", DescZh: "标准权限模式"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
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
	// Use PI_CODING_AGENT_DIR if set, otherwise default to ~/.pi/agent/.
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		agentDir = filepath.Join(homeDir, ".pi", "agent")
	}
	return filepath.Join(agentDir, "AGENTS.md")
}

// ── ReasoningEffortSwitcher ──────────────────────────────────

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.thinking = effort
	slog.Info("pi: thinking level changed", "level", effort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thinking
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh"}
}

// ── WorkDirSwitcher ───────────────────────────────────────────

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("pi: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string { return a.workDir }

// ── HistoryProvider ──────────────────────────────────────────

func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	sessDir := piSessionDir(a.workDir)
	if sessDir == "" {
		return nil, nil
	}

	sessFile := findSessionFile(sessDir, sessionID)
	if sessFile == "" {
		return nil, nil
	}

	return readPiHistory(sessFile, limit)
}

// ── SkillProvider ────────────────────────────────────────────

func (a *Agent) SkillDirs() []string {
	a.mu.Lock()
	workDir := a.workDir
	a.mu.Unlock()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	// Pi uses .pi/skills at project scope; .pi/agent/skills is its global
	// layout. Keep the legacy project path for existing bridge installations.
	// Project skills precede global skills so local instructions win by name.
	dirs := []string{
		filepath.Join(absDir, ".pi", "skills"),
		filepath.Join(absDir, ".agents", "skills"),
		filepath.Join(absDir, ".pi", "agent", "skills"),
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		// Default pi agent skill directory.
		dirs = append(dirs, filepath.Join(homeDir, ".pi", "agent", "skills"))
		// Common shared skill directory used by lark-cli and other agent tools.
		dirs = append(dirs, filepath.Join(homeDir, ".agents", "skills"))

		// If PI_CODING_AGENT_DIR is set, also scan skills under that directory.
		if agentDir := os.Getenv("PI_CODING_AGENT_DIR"); agentDir != "" {
			if filepath.IsAbs(agentDir) {
				dirs = append(dirs, filepath.Join(agentDir, "skills"))
			} else {
				dirs = append(dirs, filepath.Join(homeDir, agentDir, "skills"))
			}
		}
	}
	return dirs
}

// ── Models JSON helpers ─────────────────────────────────────

// modelsJSON represents the structure of ~/.pi/agent/models.json.
type modelsJSON struct {
	Providers map[string]struct {
		Models []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextWindow int    `json:"contextWindow"`
		} `json:"models"`
	} `json:"providers"`
}

// loadModelsContextWindows reads ~/.pi/agent/models.json and
// models-store.json and returns a map of model ID → contextWindow.
// Keys include both the short ID (e.g. "deepseek-v4-pro") and the
// fully-qualified provider/ID (e.g. "workbuddy/cn:auto").
// Returns nil on any error (caller falls back to 200K).
//
// Note: models.json is distinct from models-store.json — models.json holds
// user-defined endpoints, while models-store.json is the bundled provider
// catalog. Both carry per-model context-window sizes, so both are read:
// previously only models.json was consulted, which left every catalog model
// (e.g. deepseek-flash at 1M) on the 200K fallback.
func loadModelsContextWindows() map[string]int {
	m := make(map[string]int)
	for _, c := range readModelCatalog() {
		m[c.ID] = c.ContextWindow
		m[c.Provider+"/"+c.ID] = c.ContextWindow
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// ── Settings helpers ─────────────────────────────────────────

// piSettingsDir returns the pi agent config directory.
// Respects PI_CODING_AGENT_DIR env var; defaults to ~/.pi/agent.
func piSettingsDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// settingsPath returns the path to settings.json.
func settingsPath() string {
	dir := piSettingsDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// piSettings represents the structure of pi's settings.json relevant fields.
type piSettings struct {
	EnabledModels   []string `json:"enabledModels"`
	DefaultModel    string   `json:"defaultModel"`
	DefaultProvider string   `json:"defaultProvider"`
}

// readSettings reads and parses pi's settings.json.
func readSettings() (*piSettings, error) {
	path := settingsPath()
	if path == "" {
		return nil, fmt.Errorf("pi: cannot determine settings path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pi: read settings: %w", err)
	}
	var s piSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("pi: parse settings: %w", err)
	}
	return &s, nil
}

// readSettingsModels returns the models selected by settings.json's
// enabledModels as ModelOptions.
//
// enabledModels holds *scope patterns* in the same format as pi's --models
// flag (exact IDs, fuzzy matches, case-insensitive globs, optional
// ":<thinking>" suffix) — not a list of model IDs. They are resolved against
// the merged model catalog so that e.g. ["workbuddy/*", "chunfeng/*"] expands
// to the concrete provider/id pairs pi would offer in /model.
//
// A pattern that matches nothing is kept verbatim so the user still sees what
// they configured (and so a typo'd custom model remains selectable), which also
// keeps the previous behaviour of returning len(enabledModels) entries.
func readSettingsModels() ([]core.ModelOption, error) {
	s, err := readSettings()
	if err != nil {
		return nil, err
	}
	if len(s.EnabledModels) == 0 {
		return nil, nil
	}
	catalog := readModelCatalog()
	models := make([]core.ModelOption, 0, len(s.EnabledModels))
	seen := make(map[string]bool, len(s.EnabledModels))
	add := func(opt core.ModelOption) {
		if seen[opt.Name] {
			return
		}
		seen[opt.Name] = true
		models = append(models, opt)
	}
	for _, pattern := range s.EnabledModels {
		if pattern == "" {
			continue
		}
		matched := matchModelPattern(pattern, catalog)
		if len(matched) == 0 {
			// Unresolvable pattern: surface it as-is (previous behaviour).
			opt := core.ModelOption{Name: pattern}
			if idx := strings.LastIndex(pattern, "/"); idx >= 0 && idx+1 < len(pattern) {
				opt.Alias = pattern[idx+1:]
			}
			add(opt)
			continue
		}
		for _, c := range catalogOptions(matched) {
			add(c)
		}
	}
	return aliasUniqueOptions(models), nil
}

// aliasUniqueOptions replaces any Alias that repeats (case-insensitively) with
// the provider-qualified ID, so a model selected by alias always resolves to
// exactly one entry.
func aliasUniqueOptions(models []core.ModelOption) []core.ModelOption {
	counts := make(map[string]int, len(models))
	for _, m := range models {
		if m.Alias != "" {
			counts[strings.ToLower(m.Alias)]++
		}
	}
	for i := range models {
		if models[i].Alias != "" && counts[strings.ToLower(models[i].Alias)] > 1 {
			models[i].Alias = models[i].Name
		}
	}
	return models
}

// modelsStoreJSON represents the structure of ~/.pi/agent/models-store.json,
// the provider catalog pi itself maintains (hydrated from the published
// pi-ai package and refreshed as providers are added). Top-level keys are
// provider names, each with a Models list.
//
//	{
//	  "deepseek": { "models": [ { "id": "...", "name": "...", ... } ] }
//	}
type modelsStoreJSON map[string]struct {
	Models []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextWindow int    `json:"contextWindow"`
	} `json:"models"`
}

// ── Model catalog ───────────────────────────────────────────

// piModel is one model in pi's merged catalog.
type piModel struct {
	Provider      string
	ID            string
	Name          string // display name, may be empty
	ContextWindow int
}

// readModelCatalog merges models.json (user-defined endpoints) with
// models-store.json (bundled provider catalog) into a single list.
// models.json wins on a provider/id collision. The result is sorted by
// provider then ID so the /model card is stable across calls.
func readModelCatalog() []piModel {
	byKey := make(map[string]piModel)
	add := func(provider string, id, name string, cw int) {
		if provider == "" || id == "" {
			return
		}
		byKey[provider+"/"+id] = piModel{Provider: provider, ID: id, Name: name, ContextWindow: cw}
	}

	// Bundled catalog first so models.json can override it.
	if dir := piSettingsDir(); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "models-store.json")); err == nil {
			var store modelsStoreJSON
			if err := json.Unmarshal(data, &store); err != nil {
				slog.Warn("pi: parse models-store", "error", err)
			} else {
				for provider, p := range store {
					for _, m := range p.Models {
						add(provider, m.ID, m.Name, m.ContextWindow)
					}
				}
			}
		} else {
			slog.Debug("pi: read models-store", "error", err)
		}
	}

	if dir := piSettingsDir(); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "models.json")); err == nil {
			var cfg modelsJSON
			if err := json.Unmarshal(data, &cfg); err != nil {
				slog.Warn("pi: parse models.json", "error", err)
			} else {
				for provider, p := range cfg.Providers {
					for _, m := range p.Models {
						// A models.json entry with no name keeps the store's display name.
						name := m.Name
						if name == "" {
							name = byKey[provider+"/"+m.ID].Name
						}
						cw := m.ContextWindow
						if cw == 0 {
							cw = byKey[provider+"/"+m.ID].ContextWindow
						}
						add(provider, m.ID, name, cw)
					}
				}
			}
		} else if !os.IsNotExist(err) {
			slog.Warn("pi: read models.json", "error", err)
		}
	}

	catalog := make([]piModel, 0, len(byKey))
	for _, m := range byKey {
		catalog = append(catalog, m)
	}
	sort.Slice(catalog, func(i, j int) bool {
		if catalog[i].Provider != catalog[j].Provider {
			return catalog[i].Provider < catalog[j].Provider
		}
		return catalog[i].ID < catalog[j].ID
	})
	return catalog
}

// catalogOptions renders catalog entries as core.ModelOptions.
//
// Name carries the provider-qualified ID because that is the only reference
// pi's --model flag resolves unambiguously. Alias is the display name, which
// is much friendlier on the /model card than "cn:auto". A display name that
// is not unique across the list falls back to the bare model ID, so aliases
// stay unambiguous for `resolveModelAlias`.
func catalogOptions(catalog []piModel) []core.ModelOption {
	counts := make(map[string]int, len(catalog))
	for _, m := range catalog {
		if m.Name != "" {
			counts[strings.ToLower(m.Name)]++
		}
	}
	opts := make([]core.ModelOption, 0, len(catalog))
	for _, m := range catalog {
		alias := m.ID
		if m.Name != "" && counts[strings.ToLower(m.Name)] == 1 {
			alias = m.Name
		}
		opts = append(opts, core.ModelOption{
			Name:  m.Provider + "/" + m.ID,
			Alias: alias,
			Desc:  m.ID,
		})
	}
	return opts
}

// thinkingLevels are the values pi accepts as a ":<level>" model-pattern suffix.
var thinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// splitThinkingSuffix splits an optional trailing ":<level>" off a pattern,
// mirroring pi's resolver. Only an exact, known level counts, so IDs that
// themselves contain colons (e.g. "workbuddy/cn:auto") are left intact.
func splitThinkingSuffix(pattern string) (string, string) {
	i := strings.LastIndex(pattern, ":")
	if i < 0 || i == len(pattern)-1 {
		return pattern, ""
	}
	if lvl := pattern[i+1:]; thinkingLevels[strings.ToLower(lvl)] {
		return pattern[:i], lvl
	}
	return pattern, ""
}

// globToRegexp compiles a minimatch-style glob into a case-insensitive,
// fully-anchored regexp. "?" matches one character, "*" matches any run of
// characters *including* "/" (pi relies on that for patterns such as
// "workbuddy/*"), and "[...]" is a character class with optional "!" negation.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			// Copy the class through to its closing ']' so ranges and
			// negation keep working. An unclosed '[' is a literal.
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				b.WriteString(regexp.QuoteMeta("["))
				continue
			}
			class := pattern[i+1 : i+1+end]
			i += end + 1
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// globMatch reports whether pattern matches s. An invalid pattern matches
// nothing.
func globMatch(pattern, s string) bool {
	re, err := globToRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// hasGlobMeta reports whether pattern contains glob metacharacters.
func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// sortedModels orders matches deterministically (provider, then ID).
func sortedModels(models []piModel) []piModel {
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].ID < models[j].ID
	})
	return models
}

// exactModelMatch resolves an exact model reference: a canonical
// "provider/id" always wins; a bare ID only matches when it is unambiguous.
func exactModelMatch(ref string, catalog []piModel) (piModel, bool) {
	lower := strings.ToLower(strings.TrimSpace(ref))
	if lower == "" {
		return piModel{}, false
	}
	var bare []piModel
	for _, m := range catalog {
		if strings.ToLower(m.Provider+"/"+m.ID) == lower {
			return m, true
		}
		if strings.ToLower(m.ID) == lower {
			bare = append(bare, m)
		}
	}
	if len(bare) == 1 {
		return bare[0], true
	}
	return piModel{}, false
}

// globModels returns every catalog entry matching a glob pattern, testing the
// pattern against both "provider/id" and the bare ID (pi does the same).
func globModels(pattern string, catalog []piModel) []piModel {
	re, err := globToRegexp(pattern)
	if err != nil {
		slog.Debug("pi: invalid model glob", "pattern", pattern, "error", err)
		return nil
	}
	var hits []piModel
	for _, m := range catalog {
		if re.MatchString(m.Provider+"/"+m.ID) || re.MatchString(m.ID) {
			hits = append(hits, m)
		}
	}
	return hits
}

// fuzzyModels picks one model by case-insensitive substring match on the ID or
// display name. A "provider/<subpattern>" input is matched within that provider
// first, mirroring how pi infers the provider from a slash before matching
// (so "workbuddy/auto" finds "workbuddy/cn:auto").
func fuzzyModels(pattern string, catalog []piModel) []piModel {
	if i := strings.Index(pattern, "/"); i > 0 && i < len(pattern)-1 {
		provider := strings.ToLower(pattern[:i])
		sub := strings.ToLower(pattern[i+1:])
		var scoped []piModel
		for _, m := range catalog {
			if strings.ToLower(m.Provider) == provider {
				scoped = append(scoped, m)
			}
		}
		if len(scoped) > 0 {
			if hits := substringModels(sub, scoped); len(hits) > 0 {
				return hits[:1]
			}
		}
	}
	hits := substringModels(strings.ToLower(pattern), catalog)
	if len(hits) == 0 {
		return nil
	}
	return hits[:1]
}

// substringModels returns the sorted models whose ID or display name contains
// needle (already lower-cased).
func substringModels(needle string, catalog []piModel) []piModel {
	var hits []piModel
	for _, m := range catalog {
		if strings.Contains(strings.ToLower(m.ID), needle) ||
			(m.Name != "" && strings.Contains(strings.ToLower(m.Name), needle)) {
			hits = append(hits, m)
		}
	}
	return sortedModels(hits)
}

// matchModelPattern resolves one model pattern against the catalog, following
// pi's resolveModelScope ordering: exact match first (pi tries the raw pattern
// before splitting off a ":<thinking>" suffix, so IDs ending in a level name
// still resolve), then glob, then fuzzy substring.
// Returns nil when nothing matches.
func matchModelPattern(pattern string, catalog []piModel) []piModel {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if m, ok := exactModelMatch(pattern, catalog); ok {
		return []piModel{m}
	}
	base, _ := splitThinkingSuffix(pattern)
	if base == "" {
		return nil
	}
	if hasGlobMeta(base) {
		return globModels(base, catalog)
	}
	return fuzzyModels(base, catalog)
}

// resolveModelRef canonicalises a user-supplied model reference to the
// catalog's "provider/id" spelling (e.g. "workbuddy/auto" →
// "workbuddy/cn:auto"). An unresolvable reference is returned unchanged so that
// pi's own custom-model-id fallback keeps working.
func resolveModelRef(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || hasGlobMeta(model) {
		return model
	}
	matched := matchModelPattern(model, readModelCatalog())
	if len(matched) != 1 {
		return model
	}
	return matched[0].Provider + "/" + matched[0].ID
}

// readDefaultModel returns the defaultModel from settings.json.
func readDefaultModel() (string, error) {
	s, err := readSettings()
	if err != nil {
		return "", err
	}
	// If defaultProvider is set, qualify the defaultModel with it.
	if s.DefaultProvider != "" && s.DefaultModel != "" && !strings.Contains(s.DefaultModel, "/") {
		return s.DefaultProvider + "/" + s.DefaultModel, nil
	}
	return s.DefaultModel, nil
}

// ── Session helpers ──────────────────────────────────────────

// findSessionFile locates the .jsonl file for a given session UUID in sessDir.
// Session files are named: <timestamp>_<uuid>.jsonl — this function extracts
// the UUID portion and matches exactly to avoid partial-match vulnerabilities.
func findSessionFile(sessDir, sessionID string) string {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Extract UUID: strip .jsonl, then take everything after the last "_".
		base := strings.TrimSuffix(name, ".jsonl")
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if base[idx+1:] == sessionID {
				return filepath.Join(sessDir, name)
			}
		}
	}
	return ""
}

// piSessionDir returns the pi session directory for the given workDir.
// Pi encodes the absolute path as: replace "/", "\" and ":" with "-", wrap with "--".
// e.g. /home/user/project → --home-user-project--
// e.g. D:\project         → --D-project--
func piSessionDir(workDir string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	// Replace /, \ and : with - to produce a valid single-path-component directory name.
	// Pi's TypeScript uses /[/\\:]/g to do the same (see getDefaultSessionDirPath in session-manager.ts).
	safe := strings.ReplaceAll(absDir, "/", "-")
	safe = strings.ReplaceAll(safe, "\\", "-")
	safe = strings.ReplaceAll(safe, ":", "-")
	encoded := "--" + strings.TrimPrefix(safe, "-") + "--"
	return filepath.Join(homeDir, ".pi", "agent", "sessions", encoded)
}

// scanPiSession reads a pi session .jsonl file and extracts the session ID,
// a summary (first user message), and a message count.
func scanPiSession(path string) (sessionID, summary string, msgCount int) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry["type"] {
		case "session":
			if id, ok := entry["id"].(string); ok {
				sessionID = id
			}
		case "message":
			msg, _ := entry["message"].(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			if role == "user" || role == "assistant" {
				msgCount++
			}
			// Use first user message as summary.
			if role == "user" && summary == "" {
				content, _ := msg["content"].([]any)
				for _, c := range content {
					item, _ := c.(map[string]any)
					if item != nil {
						if text, ok := item["text"].(string); ok && text != "" {
							summary = text
							runes := []rune(summary)
							if len(runes) > 80 {
								summary = string(runes[:80]) + "..."
							}
							break
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("pi: scan session error", "path", path, "error", err)
	}
	return
}

// readPiHistory reads user/assistant messages from a pi session file.
func readPiHistory(path string, limit int) ([]core.HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	var all []core.HistoryEntry
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] != "message" {
			continue
		}
		msg, _ := entry["message"].(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}

		var text string
		content, _ := msg["content"].([]any)
		for _, c := range content {
			item, _ := c.(map[string]any)
			if item != nil {
				if t, ok := item["text"].(string); ok && t != "" {
					text = t
					break
				}
			}
		}
		if text == "" {
			continue
		}
		all = append(all, core.HistoryEntry{Role: role, Content: text})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("pi: read history: %w", err)
	}

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}
