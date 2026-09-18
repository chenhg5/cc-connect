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
	thinking     string // reasoning effort: off, minimal, low, medium, high, xhigh, max
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
	// enabledModels 未配置时，回退到 pi 的模型目录：models-store.json（内置
	// 目录）合并 models.json（用户自定义 provider），与 pi 自身模型选择器一致；
	// 否则 /model 卡片只会显示当前模型，且自定义 provider（如 zai-coding-team）
	// 会整体缺失。
	if opts := catalogModelOptions(); len(opts) > 0 {
		return opts
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

// allThinkingLevels mirrors pi-ai's EXTENDED_THINKING_LEVELS: the canonical
// pi thinking levels in ascending order.
var allThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// AvailableReasoningEfforts returns the thinking levels supported by the
// current model, derived from its thinkingLevelMap (models.json /
// models-store.json) exactly like pi-ai's getSupportedThinkingLevels:
//   - non-reasoning models only support off;
//   - a level explicitly mapped to null is unsupported;
//   - xhigh/max are only offered when explicitly mapped to a string;
//   - the remaining levels default to supported when absent from the map.
//
// Models unknown to the catalog fall back to the full level list.
func (a *Agent) AvailableReasoningEfforts() []string {
	a.mu.Lock()
	model := a.model
	a.mu.Unlock()
	if levels, ok := supportedThinkingLevels(model); ok {
		return levels
	}
	return append([]string(nil), allThinkingLevels...)
}

// supportedThinkingLevels resolves model → supported thinking levels.
// ok is false when the model cannot be resolved from the catalog.
func supportedThinkingLevels(model string) ([]string, bool) {
	_, m, ok := lookupModelDef(model)
	if !ok {
		return nil, false
	}
	return thinkingLevelsForDef(m), true
}

// thinkingLevelsForDef applies pi-ai's getSupportedThinkingLevels rules to a
// resolved model definition.
func thinkingLevelsForDef(m *piModelDef) []string {
	if !m.Reasoning {
		return []string{"off"}
	}
	var out []string
	for _, level := range allThinkingLevels {
		mapped, present := m.ThinkingLevelMap[level]
		switch {
		case present && mapped == nil:
			// Explicitly disabled for this model.
		case !present && (level == "xhigh" || level == "max"):
			// Only offered when the model explicitly maps them.
		default:
			out = append(out, level)
		}
	}
	if len(out) == 0 {
		return []string{"off"}
	}
	return out
}

// lookupModelDef resolves a model reference ("provider/id", "id", or
// "provider/id:thinking") against the merged model catalog. A bare id is
// matched against every provider, preferring settings.json defaultProvider.
func lookupModelDef(model string) (string, *piModelDef, bool) {
	ref := strings.TrimSpace(model)
	// Strip an optional ":thinking" suffix (pi's --model pattern syntax).
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		ref = ref[:idx]
	}
	if ref == "" {
		return "", nil, false
	}
	catalog := readModelCatalog()
	if idx := strings.Index(ref, "/"); idx >= 0 {
		provider, id := ref[:idx], ref[idx+1:]
		entry, ok := catalog[provider]
		if !ok {
			return "", nil, false
		}
		for i := range entry.Models {
			if entry.Models[i].ID == id {
				return provider, &entry.Models[i], true
			}
		}
		return "", nil, false
	}
	// Bare id: prefer the default provider, then any provider (sorted for
	// deterministic resolution when several providers share an id).
	providers := make([]string, 0, len(catalog))
	for name := range catalog {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	if s, err := readSettings(); err == nil && s.DefaultProvider != "" {
		providers = append([]string{s.DefaultProvider}, providers...)
	}
	for _, provider := range providers {
		entry := catalog[provider]
		for i := range entry.Models {
			if entry.Models[i].ID == ref {
				return provider, &entry.Models[i], true
			}
		}
	}
	return "", nil, false
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
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".pi", "agent", "skills")}

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

// ── Model catalog (models.json + models-store.json) ─────────

// piModelDef is the per-model metadata cc-connect reads from pi's model
// files. Both models.json (user-defined custom providers) and
// models-store.json (the provider catalog pi maintains) share this shape.
type piModelDef struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Reasoning        bool           `json:"reasoning"`
	ContextWindow    int            `json:"contextWindow"`
	ThinkingLevelMap map[string]any `json:"thinkingLevelMap"`
}

// piProviderEntry is one provider entry in a pi model file.
type piProviderEntry struct {
	Name   string       `json:"name"`
	Models []piModelDef `json:"models"`
}

// customModelsJSON represents the structure of ~/.pi/agent/models.json:
// user-defined custom providers (baseUrl/api/apiKey + models), wrapped in a
// top-level "providers" key.
type customModelsJSON struct {
	Providers map[string]piProviderEntry `json:"providers"`
}

// storeModelsJSON represents the structure of ~/.pi/agent/models-store.json:
// the provider catalog pi itself maintains (hydrated from the published
// pi-ai package and refreshed as providers are added). Top-level keys are
// provider names, each with a Models list.
type storeModelsJSON map[string]piProviderEntry

// readStoreProviders reads models-store.json. ok is false when the file is
// missing or malformed; callers fall back.
func readStoreProviders() (map[string]piProviderEntry, bool) {
	dir := piSettingsDir()
	if dir == "" {
		slog.Debug("pi: cannot determine settings dir for models-store.json")
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "models-store.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("pi: read models-store", "error", err)
		}
		return nil, false
	}
	var store storeModelsJSON
	if err := json.Unmarshal(data, &store); err != nil {
		slog.Warn("pi: parse models-store", "error", err)
		return nil, false
	}
	return store, true
}

// readCustomProviders reads the user-defined providers from models.json.
// The returned bool is false when the file exists but is malformed (callers
// with an all-or-nothing contract return nil); a missing file returns
// (nil, true).
func readCustomProviders() (map[string]piProviderEntry, bool) {
	dir := piSettingsDir()
	if dir == "" {
		return nil, true
	}
	data, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("pi: read models.json", "error", err)
			return nil, false
		}
		return nil, true
	}
	var custom customModelsJSON
	if err := json.Unmarshal(data, &custom); err != nil {
		slog.Warn("pi: parse models.json", "error", err)
		return nil, false
	}
	return custom.Providers, true
}

// readModelCatalog merges the two model sources pi exposes: custom providers
// from models.json on top of the models-store.json catalog. Within a
// provider, custom model defs replace same-id catalog entries and append new
// ones, so user overrides win. This mirrors what pi's own model picker shows
// (custom providers like "zai-coding-team" alongside catalog providers).
func readModelCatalog() map[string]piProviderEntry {
	catalog := map[string]piProviderEntry{}
	if store, ok := readStoreProviders(); ok {
		for name, entry := range store {
			catalog[name] = entry
		}
	}
	custom, _ := readCustomProviders()
	for name, entry := range custom {
		base, exists := catalog[name]
		if !exists {
			catalog[name] = entry
			continue
		}
		merged := append([]piModelDef(nil), base.Models...)
		for _, m := range entry.Models {
			replaced := false
			for i := range merged {
				if merged[i].ID == m.ID {
					merged[i] = m
					replaced = true
					break
				}
			}
			if !replaced {
				merged = append(merged, m)
			}
		}
		entry.Models = merged
		if entry.Name == "" {
			entry.Name = base.Name
		}
		catalog[name] = entry
	}
	return catalog
}

// providerOptions flattens per-provider entries into sorted,
// provider-qualified ModelOptions (Name = "provider/id", Alias = short id,
// Desc = display name).
func providerOptions(catalog map[string]piProviderEntry) []core.ModelOption {
	var models []core.ModelOption
	for provider, entry := range catalog {
		for _, m := range entry.Models {
			if m.ID == "" {
				continue
			}
			models = append(models, core.ModelOption{
				Name:  provider + "/" + m.ID,
				Alias: m.ID,
				Desc:  m.Name,
			})
		}
	}
	// Map iteration order is random — sort for deterministic card display.
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

// catalogModelOptions returns all models visible to pi's model picker:
// the models-store.json catalog merged with models.json custom providers.
func catalogModelOptions() []core.ModelOption {
	return providerOptions(readModelCatalog())
}

// loadModelsContextWindows reads per-model contextWindow sizes from
// models.json (custom providers, authoritative) and models-store.json
// (catalog fallback) and returns a map of model ID → contextWindow. Keys
// include both the short ID (e.g. "deepseek/deepseek-v4-pro") and the
// fully-qualified provider/ID (e.g. "my-provider/my-model").
// Returns nil on any error (caller falls back to 200K).
func loadModelsContextWindows() map[string]int {
	if piSettingsDir() == "" {
		slog.Warn("pi: cannot determine pi settings dir for models.json")
		return nil
	}
	custom, ok := readCustomProviders()
	if !ok {
		// models.json exists but is malformed — keep the all-or-nothing
		// contract (nil → caller falls back to 200K).
		return nil
	}
	add := func(m map[string]int, provider string, entry piProviderEntry) {
		for _, mdl := range entry.Models {
			if mdl.ContextWindow <= 0 {
				continue
			}
			m[mdl.ID] = mdl.ContextWindow
			m[provider+"/"+mdl.ID] = mdl.ContextWindow
		}
	}
	m := make(map[string]int)
	// Custom models.json wins over the catalog, so fill the catalog first.
	if store, ok := readStoreProviders(); ok {
		for provider, entry := range store {
			add(m, provider, entry)
		}
	}
	for provider, entry := range custom {
		add(m, provider, entry)
	}
	if len(m) == 0 {
		// No usable data — keep the all-or-nothing contract (nil → caller
		// falls back to 200K).
		slog.Info("pi: no context windows found in models files, using 200K fallback")
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

// readSettingsModels returns the enabledModels from settings.json as ModelOptions.
func readSettingsModels() ([]core.ModelOption, error) {
	s, err := readSettings()
	if err != nil {
		return nil, err
	}
	if len(s.EnabledModels) == 0 {
		return nil, nil
	}
	models := make([]core.ModelOption, 0, len(s.EnabledModels))
	for _, m := range s.EnabledModels {
		option := core.ModelOption{Name: m}
		// Derive a short alias from the last segment after the final "/".
		if idx := strings.LastIndex(m, "/"); idx >= 0 && idx+1 < len(m) {
			option.Alias = m[idx+1:]
		}
		models = append(models, option)
	}
	return models, nil
}

// readModelsStore reads all models from models-store.json as
// provider-qualified ModelOptions (Name = "provider/id", Alias = short id,
// Desc = display name). Returns nil when the file is missing or unreadable;
// callers fall back to an empty list. Unlike catalogModelOptions it does not
// overlay models.json custom providers.
func readModelsStore() []core.ModelOption {
	store, ok := readStoreProviders()
	if !ok {
		return nil
	}
	return providerOptions(store)
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
// Pi encodes the absolute path as: replace "/" with "-", wrap with "--".
// e.g. /home/user/project → --home-user-project--
func piSessionDir(workDir string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	encoded := "--" + strings.ReplaceAll(strings.TrimPrefix(absDir, "/"), "/", "-") + "--"
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
