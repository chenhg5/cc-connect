package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	core.RegisterAgent("opencode", New)
}

// Agent drives the OpenCode CLI in headless mode using `opencode run --format json`.
//
// Modes:
//   - "default": standard mode
//   - "yolo":    auto mode (opencode run is auto by default in non-interactive mode)
type Agent struct {
	workDir              string
	model                string
	mode                 string
	cmd                  string // CLI binary name, default "opencode"
	providers            []core.ProviderConfig
	activeIdx            int
	sessionEnv           []string
	modelCachePath       string
	persistentModelCache *opencodePersistentModelCache
	refreshingModelCache bool
	mu                   sync.RWMutex
}

type opencodePersistentModelCache struct {
	Models      []core.ModelOption `json:"models"`
	UpdatedAt   time.Time          `json:"updated_at"`
	ProviderKey string             `json:"provider_key,omitempty"`
	ContextKey  string             `json:"context_key,omitempty"`
}

type opencodeModelDiscoverySnapshot struct {
	cmd         string
	workDir     string
	providerEnv []string
	providerKey string
	cachePath   string
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	cmd, _ := opts["cmd"].(string)
	if cmd == "" {
		cmd = "opencode"
	}
	ccDataDir, _ := opts["cc_data_dir"].(string)
	ccProject, _ := opts["cc_project"].(string)
	modelCachePath := opencodeProjectModelCachePath(ccDataDir, ccProject)
	persistentModelCache, err := loadOpencodePersistentModelCache(modelCachePath)
	if err != nil {
		slog.Warn("opencode: load persistent model cache failed", "path", modelCachePath, "err", err)
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("opencode: %q CLI not found in PATH, install from: https://github.com/opencode-ai/opencode", cmd)
	}

	return &Agent{
		workDir:              workDir,
		model:                model,
		mode:                 mode,
		cmd:                  cmd,
		activeIdx:            -1,
		modelCachePath:       modelCachePath,
		persistentModelCache: persistentModelCache,
	}, nil
}

func opencodeProjectModelCachePath(dataDir, project string) string {
	if dataDir == "" || project == "" {
		return ""
	}
	prefix := sanitizeProjectCacheComponent(project)
	if prefix == "" {
		prefix = "project"
	}
	hash := sha256.Sum256([]byte(project))
	fileName := fmt.Sprintf("%s-%s.opencode-models.json", prefix, hex.EncodeToString(hash[:8]))
	if fileName == "" {
		return ""
	}
	return filepath.Join(dataDir, "projects", fileName)
}

func sanitizeProjectCacheComponent(project string) string {
	project = strings.TrimSpace(strings.ToLower(project))
	if project == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(project))
	lastDash := false
	for _, r := range project {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func loadOpencodePersistentModelCache(path string) (*opencodePersistentModelCache, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cache opencodePersistentModelCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	cache.Models = normalizeModelOptions(cache.Models)
	if len(cache.Models) == 0 {
		return nil, nil
	}
	return &cache, nil
}

func normalizeModelOptions(models []core.ModelOption) []core.ModelOption {
	if len(models) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	normalized := make([]core.ModelOption, 0, len(models))
	for _, model := range models {
		model.Name = strings.TrimSpace(model.Name)
		if model.Name == "" {
			continue
		}
		if _, dup := seen[model.Name]; dup {
			continue
		}
		seen[model.Name] = struct{}{}
		normalized = append(normalized, model)
	}
	if len(normalized) == 0 {
		return nil
	}

	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Name < normalized[j].Name
	})
	return normalized
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force", "bypasspermissions":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "opencode" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("opencode: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("opencode: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) configuredModelsForSnapshot(snapshot opencodeModelDiscoverySnapshot) []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.providers) == 0 {
		return nil
	}
	for i := range a.providers {
		if providerCacheKey(a.providers[i]) != snapshot.providerKey {
			continue
		}
		return core.GetProviderModels(a.providers, i)
	}
	return nil
}

func (a *Agent) activeProviderKeyLocked() string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return ""
	}
	return providerCacheKey(a.providers[a.activeIdx])
}

func providerCacheKey(p core.ProviderConfig) string {
	h := sha256.New()
	writeProviderSignaturePart(h, "name", p.Name)
	writeProviderSignaturePart(h, "base_url", p.BaseURL)
	writeProviderSignaturePart(h, "model", p.Model)
	writeProviderSignaturePart(h, "api_key", p.APIKey)
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeProviderSignaturePart(h, "env:"+k, p.Env[k])
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func writeProviderSignaturePart(w io.Writer, key, value string) {
	io.WriteString(w, key)
	io.WriteString(w, "\x00")
	io.WriteString(w, value)
	io.WriteString(w, "\x00")
}

func (a *Agent) activeProviderKey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeProviderKeyLocked()
}

func (a *Agent) modelDiscoverySnapshot() opencodeModelDiscoverySnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return opencodeModelDiscoverySnapshot{
		cmd:         a.cmd,
		workDir:     a.workDir,
		providerEnv: append([]string(nil), a.providerEnvLocked()...),
		providerKey: a.activeProviderKeyLocked(),
		cachePath:   a.modelCachePath,
	}
}

func modelDiscoveryContextKey(snapshot opencodeModelDiscoverySnapshot) string {
	h := sha256.New()
	writeProviderSignaturePart(h, "provider_key", snapshot.providerKey)
	writeProviderSignaturePart(h, "work_dir", snapshot.workDir)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (a *Agent) persistentModelsForSnapshot(snapshot opencodeModelDiscoverySnapshot) []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.persistentModelCache == nil || len(a.persistentModelCache.Models) == 0 {
		return nil
	}
	if a.persistentModelCache.ProviderKey != snapshot.providerKey {
		return nil
	}
	if a.persistentModelCache.ContextKey != "" && a.persistentModelCache.ContextKey != modelDiscoveryContextKey(snapshot) {
		return nil
	}
	if a.persistentModelCache.ContextKey == "" && snapshot.workDir != "" {
		return nil
	}
	models := make([]core.ModelOption, len(a.persistentModelCache.Models))
	copy(models, a.persistentModelCache.Models)
	return models
}

func (a *Agent) persistentModels() []core.ModelOption {
	return a.persistentModelsForSnapshot(a.modelDiscoverySnapshot())
}

func (a *Agent) startPersistentModelRefresh(snapshot opencodeModelDiscoverySnapshot, allowColdStart bool) {
	a.mu.Lock()
	hasPersistentModels := a.persistentModelCache != nil && len(a.persistentModelCache.Models) > 0
	if (!allowColdStart && !hasPersistentModels) || a.refreshingModelCache {
		a.mu.Unlock()
		return
	}
	a.refreshingModelCache = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.refreshingModelCache = false
			a.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		models := a.discoverModelsWithSnapshot(ctx, snapshot)
		if len(models) == 0 {
			return
		}
		if err := a.storePersistentModelCache(snapshot, models); err != nil {
			slog.Warn("opencode: update persistent model cache failed", "path", snapshot.cachePath, "err", err)
		}
	}()
}

func (a *Agent) StartInitialModelRefresh() {
	a.startPersistentModelRefresh(a.modelDiscoverySnapshot(), true)
}

func (a *Agent) storePersistentModelCache(snapshot opencodeModelDiscoverySnapshot, models []core.ModelOption) error {
	models = normalizeModelOptions(models)
	if len(models) == 0 {
		return nil
	}

	cache := &opencodePersistentModelCache{
		Models:      models,
		UpdatedAt:   time.Now(),
		ProviderKey: snapshot.providerKey,
		ContextKey:  modelDiscoveryContextKey(snapshot),
	}

	if snapshot.cachePath != "" {
		if err := os.MkdirAll(filepath.Dir(snapshot.cachePath), 0o755); err != nil {
			return err
		}
		data, err := json.Marshal(cache)
		if err != nil {
			return err
		}
		if err := core.AtomicWriteFile(snapshot.cachePath, data, 0o644); err != nil {
			return err
		}
	}

	a.mu.Lock()
	a.persistentModelCache = cache
	a.mu.Unlock()
	return nil
}

// discoverModels runs `<cmd> models` and parses stdout line-by-line into a
// normalized slice of ModelOption.
// Returns nil if the command fails, times out, or produces no usable output.
func (a *Agent) discoverModels(ctx context.Context) []core.ModelOption {
	return a.discoverModelsWithSnapshot(ctx, a.modelDiscoverySnapshot())
}

func (a *Agent) discoverModelsWithSnapshot(ctx context.Context, snapshot opencodeModelDiscoverySnapshot) []core.ModelOption {
	c := exec.CommandContext(ctx, snapshot.cmd, "models")
	c.Dir = snapshot.workDir
	if len(snapshot.providerEnv) > 0 {
		c.Env = append(os.Environ(), snapshot.providerEnv...)
	}
	out, err := c.Output()
	if err != nil {
		slog.Debug("opencode: discoverModels failed", "err", err)
		return nil
	}

	var models []core.ModelOption
	for _, line := range strings.Split(string(out), "\n") {
		models = append(models, core.ModelOption{Name: line})
	}

	models = normalizeModelOptions(models)
	if len(models) == 0 {
		slog.Debug("opencode: discoverModels: no models in output")
		return nil
	}
	return models
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	snapshot := a.modelDiscoverySnapshot()
	if models := a.persistentModelsForSnapshot(snapshot); len(models) > 0 {
		a.startPersistentModelRefresh(snapshot, false)
		return models
	}
	if models := a.discoverModelsWithSnapshot(ctx, snapshot); len(models) > 0 {
		if err := a.storePersistentModelCache(snapshot, models); err != nil {
			slog.Warn("opencode: persist discovered model cache failed", "path", a.modelCachePath, "err", err)
		}
		return models
	}
	if models := a.configuredModelsForSnapshot(snapshot); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "anthropic/claude-sonnet-4-20250514", Desc: "Claude Sonnet 4 (default)"},
		{Name: "anthropic/claude-opus-4-20250514", Desc: "Claude Opus 4"},
		{Name: "openai/gpt-4o", Desc: "GPT-4o"},
		{Name: "openai/o3", Desc: "OpenAI o3"},
	}
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	cmd := a.cmd
	workDir := a.workDir
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newOpencodeSession(ctx, cmd, workDir, model, mode, sessionID, extraEnv)
}

// ListSessions runs `opencode session list` and parses the JSON output.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listOpencodeSessions(a.cmd, a.workDir)
}

func (a *Agent) Stop() error { return nil }

// DeleteSession implements core.SessionDeleter via `opencode session delete <id>`.
func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	a.mu.RLock()
	cmd := a.cmd
	workDir := a.workDir
	a.mu.RUnlock()

	c := exec.Command(cmd, "session", "delete", sessionID)
	c.Dir = workDir
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("opencode: delete session %s: %w: %s", sessionID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// -- ModeSwitcher --

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("opencode: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard mode", DescZh: "标准模式"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
	}
}

// -- ContextCompressor --

func (a *Agent) CompressCommand() string { return "/compact" }

// -- MemoryFileProvider --

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "OPENCODE.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".opencode", "OPENCODE.md")
}

// -- ProviderSwitcher --

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
		slog.Info("opencode: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("opencode: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]core.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string
	if p.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// -- Session listing --

// opencodeSessionEntry represents a session from `opencode session list` output.
type opencodeSessionEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated int64  `json:"updated"` // Unix timestamp in milliseconds
	Created int64  `json:"created"`
}

func listOpencodeSessions(cmd, workDir string) ([]core.AgentSessionInfo, error) {
	c := exec.Command(cmd, "session", "list", "--format", "json")
	c.Dir = workDir

	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode: session list: %w", err)
	}

	var entries []opencodeSessionEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("opencode: parse session list: %w", err)
	}

	msgCounts := querySessionMessageCounts()

	var sessions []core.AgentSessionInfo
	for _, e := range entries {
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           e.ID,
			Summary:      e.Title,
			MessageCount: msgCounts[e.ID],
			ModifiedAt:   time.UnixMilli(e.Updated),
		})
	}

	return sessions, nil
}

// querySessionMessageCounts uses the sqlite3 CLI to read message counts from
// OpenCode's local database. Returns an empty map on any failure.
func querySessionMessageCounts() map[string]int {
	dbPath := opencodeDBPath()
	if dbPath == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		slog.Warn("opencode: sqlite3 CLI not found, message counts unavailable", "err", err)
		return nil
	}

	out, err := exec.Command(sqlite3, dbPath,
		"SELECT session_id, COUNT(*) FROM message GROUP BY session_id").Output()
	if err != nil {
		slog.Warn("opencode: sqlite3 query failed", "db_path", dbPath, "err", err)
		return nil
	}

	counts := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(parts[1], "%d", &n); err == nil {
			counts[parts[0]] = n
		}
	}
	return counts
}

func opencodeDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}
