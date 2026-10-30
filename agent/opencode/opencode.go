package opencode

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	_ "modernc.org/sqlite"
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
	cmd                  string   // CLI binary name, default "opencode"
	cliExtraArgs         []string // extra args from cmd after the binary name
	configEnv            []string // env vars from [projects.agent.options.env]
	agentName            string   // passed as --agent to opencode (for plugin-defined agents)
	providers            []core.ProviderConfig
	activeIdx            int
	sessionEnv           []string
	modelCachePath       string
	persistentModelCache *opencodePersistentModelCache
	refreshingModelCache bool
	refreshWg            sync.WaitGroup // tracks in-flight background model-cache refresh goroutines
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
	cmd, extraArgs := core.ParseCmdOpts(opts, "opencode")
	agentName, _ := opts["agent"].(string) // --agent flag for plugin-defined agents (#1210)
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
		cliExtraArgs:         extraArgs,
		configEnv:            core.ParseConfigEnv(opts),
		agentName:            agentName,
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

func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	opts := map[string]any{
		"mode": a.mode,
	}
	if a.model != "" {
		opts["model"] = a.model
	}
	if a.agentName != "" {
		opts["agent"] = a.agentName
	}
	if len(a.configEnv) > 0 {
		env := make(map[string]string, len(a.configEnv))
		for _, kv := range a.configEnv {
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = v
			}
		}
		if len(env) > 0 {
			opts["env"] = env
		}
	}
	return opts
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
	mustWriteProviderSignaturePart(h, "name", p.Name)
	mustWriteProviderSignaturePart(h, "base_url", p.BaseURL)
	mustWriteProviderSignaturePart(h, "model", p.Model)
	mustWriteProviderSignaturePart(h, "api_key", p.APIKey)
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		mustWriteProviderSignaturePart(h, "env:"+k, p.Env[k])
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func mustWriteProviderSignaturePart(w io.Writer, key, value string) {
	if err := writeProviderSignaturePart(w, key, value); err != nil {
		panic(fmt.Sprintf("write provider signature: %v", err))
	}
}

func writeProviderSignaturePart(w io.Writer, key, value string) error {
	if _, err := io.WriteString(w, key); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\x00"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, value); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\x00"); err != nil {
		return err
	}
	return nil
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
	mustWriteProviderSignaturePart(h, "provider_key", snapshot.providerKey)
	mustWriteProviderSignaturePart(h, "work_dir", snapshot.workDir)
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
	a.refreshWg.Add(1)
	a.mu.Unlock()

	go func() {
		defer a.refreshWg.Done()
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
	extraArgs := append([]string{}, a.cliExtraArgs...)
	workDir := a.workDir
	agentName := a.agentName
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newOpencodeSession(ctx, cmd, extraArgs, workDir, model, mode, agentName, sessionID, extraEnv)
}

// ListSessions runs `opencode session list` and parses the JSON output.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	cmd := a.cmd
	workDir := a.workDir
	a.mu.RUnlock()
	return listOpencodeSessions(cmd, workDir)
}

func (a *Agent) Stop() error { return nil }

// DeleteSession implements core.SessionDeleter via `opencode session delete <id>`.
func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	a.mu.RLock()
	cmd := a.cmd
	workDir := a.workDir
	a.mu.RUnlock()

	// Never delete a session that lives in another project directory: with
	// the global /list, an index can point at a foreign session, and
	// `opencode session delete` resolves its target by ID with no directory
	// scoping (verified against Session.remove: store.get by ID only).
	// The database is opened once up front so every outcome below is
	// decided from the same readable snapshot: an unreadable database
	// refuses instead of falling through to the unscoped CLI.
	dbPath := legacyOpencodeDBPath()
	db, err := openOpencodeDB(dbPath)
	if err != nil {
		return fmt.Errorf("opencode: refuse to delete session %s: cannot open local database (%v)", sessionID, err)
	}
	defer closeQuiet(db, "db", dbPath)

	if dir, known := querySessionDirectoryDB(db, sessionID); known {
		if !core.SameAgentDir(dir, workDir) {
			return fmt.Errorf("opencode: refuse to delete session %s from another directory (%s); switch to that project first", sessionID, dir)
		}
	} else if tableExists(db, "session_v2") {
		// The session is not in a modern database that should know about
		// every session: refuse rather than letting the unscoped CLI
		// delete resolve it.
		return fmt.Errorf("opencode: refuse to delete unknown session %s (not found in the local database)", sessionID)
	}
	// Legacy database without a v2 table: fall through to the CLI. Its
	// `session list` is scoped to the current project's directory, so an
	// index picked from /list can only point at a same-project session.

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
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
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
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Updated   int64  `json:"updated"` // Unix timestamp in milliseconds
	Created   int64  `json:"created"`
}

func listOpencodeSessions(cmd, workDir string) ([]core.AgentSessionInfo, error) {
	// Global list first: every project, newest first, mirroring the native
	// OpenCode session picker. Falls back to the server's per-project list
	// when the global list is unavailable or empty (older databases, or a
	// v2 table that exists but has not been populated yet).
	if sessions, err := listOpencodeSessionsGlobal(legacyOpencodeDBPath()); err == nil && len(sessions) > 0 {
		return sessions, nil
	} else {
		slog.Debug("opencode: global session list unavailable or empty, falling back to server list", "err", err)
	}

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

	msgCounts := querySessionMessageCounts(legacyOpencodeDBPath())

	var sessions []core.AgentSessionInfo
	for _, e := range entries {
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           e.ID,
			Directory:    e.Directory,
			Summary:      e.Title,
			MessageCount: msgCounts[e.ID],
			ModifiedAt:   time.UnixMilli(e.Updated),
		})
	}

	return sessions, nil
}

// listOpencodeSessionsGlobal returns sessions from every project, newest
// first, mirroring the native OpenCode session picker. It reads the v2
// tables directly with the in-process driver, so it works the same in server
// and standalone mode and is not limited to the current work_dir project.
// Child sessions (parent_id set) are excluded, matching `session list`.
func listOpencodeSessionsGlobal(dbPath string) ([]core.AgentSessionInfo, error) {
	db, err := openOpencodeDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer closeQuiet(db, "db", dbPath)
	if !tableExists(db, "session_v2") {
		return nil, fmt.Errorf("opencode: session_v2 table not found")
	}

	rows, err := db.Query(`SELECT s.id, s.title, s.directory, COALESCE(s.time_updated, s.time_created, 0) FROM session_v2 s WHERE s.parent_id IS NULL ORDER BY 4 DESC, s.id DESC`)
	if err != nil {
		return nil, err
	}
	defer closeQuiet(rows, "rows", dbPath)

	var sessions []core.AgentSessionInfo
	for rows.Next() {
		var id string
		var title, directory sql.NullString
		var updated int64
		if err := rows.Scan(&id, &title, &directory, &updated); err != nil {
			continue
		}
		sessions = append(sessions, core.AgentSessionInfo{
			ID:         id,
			Directory:  directory.String,
			Summary:    title.String,
			ModifiedAt: time.UnixMilli(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	byID := make(map[string]int, len(sessions))
	for i := range sessions {
		byID[sessions[i].ID] = i
	}
	for id, n := range querySessionMessageCounts(dbPath) {
		if i, ok := byID[id]; ok {
			sessions[i].MessageCount = n
		}
	}
	return sessions, nil
}

// querySessionMessageCounts reads per-session message counts from the given
// OpenCode database using the in-process sqlite driver (no sqlite3 CLI
// needed). OpenCode v2 records turns in session_message/session_v2 while the
// v1 message/session tables stop receiving writes, so the v2 table is read
// with the v1 table as a base: sessions the migration has not copied yet
// keep their v1 counts instead of regressing to 0. Returns an empty map on
// any failure.
func querySessionMessageCounts(dbPath string) map[string]int {
	db, err := openOpencodeDB(dbPath)
	if err != nil {
		return nil
	}
	defer closeQuiet(db, "db", dbPath)

	counts := countBySessionTable(db, dbPath, "message")
	for id, n := range countBySessionTable(db, dbPath, "session_message") {
		if counts == nil {
			counts = make(map[string]int)
		}
		counts[id] = n
	}
	return counts
}

// countBySessionTable returns per-session row counts for one message table,
// or nil when the table does not exist or cannot be read.
func countBySessionTable(db *sql.DB, dbPath, table string) map[string]int {
	if !tableExists(db, table) {
		slog.Debug("opencode: session message table not found, skipping", "db_path", dbPath, "table", table)
		return nil
	}

	rows, err := db.Query("SELECT session_id, COUNT(*) FROM \"" + table + "\" GROUP BY session_id")
	if err != nil {
		slog.Warn("opencode: session count query failed", "db_path", dbPath, "table", table, "err", err)
		return nil
	}
	defer closeQuiet(rows, "rows", dbPath)

	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			continue
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		slog.Warn("opencode: session count scan failed", "db_path", dbPath, "table", table, "err", err)
		return nil
	}
	return counts
}

// opencodeDataDir is the directory holding the OpenCode sqlite database.
func opencodeDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// openOpencodeDB opens the OpenCode sqlite database read-only. The server may
// hold the database open for writing; read-only mode plus a short busy
// timeout keeps lookups from blocking on it.
//
// The DSN uses the file: URI form so the driver's URI parser applies
// SQLITE_OPEN_READONLY; without it modernc.org/sqlite opens read-write and
// cc-connect could create -wal/-shm sidecars next to the user's database.
func openOpencodeDB(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("opencode: empty database path")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Force one read: sql.Open is lazy, so without this a corrupt file
	// only fails later at query time (or never, if the caller only checks
	// table existence loosely). Fail closed here instead.
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// closeQuiet closes c, logging unexpected errors at debug level.
// Read-only lookups must never fail the caller on close.
func closeQuiet(c io.Closer, what string, dbPath string) {
	if err := c.Close(); err != nil {
		slog.Debug("opencode: close failed", "what", what, "db_path", dbPath, "err", err)
	}
}

// tableExists reports whether the named table exists in the opened database.
func tableExists(db *sql.DB, name string) bool {
	var found string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&found); err != nil {
		return false
	}
	return found == name
}

// legacyOpencodeDBPath is the default OpenCode database location: the
// OPENCODE_DB override when set, otherwise opencode.db under the data dir
// (honoring XDG_DATA_HOME). Counts/titles must be read from the same file
// the CLI uses; a mismatch surfaces as ghost sessions with 0 msgs in /list.
func legacyOpencodeDBPath() string {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_DB")); v != "" {
		if filepath.IsAbs(v) {
			return v
		}
		if dir := opencodeDataDir(); dir != "" {
			return filepath.Join(dir, v)
		}
		return v
	}
	if dir := opencodeDataDir(); dir != "" {
		return filepath.Join(dir, "opencode.db")
	}
	return ""
}

func (a *Agent) GetSessionTitle(sessionID string) string {
	return querySessionTitle(legacyOpencodeDBPath(), sessionID)
}

func querySessionTitle(dbPath, sessionID string) string {
	db, err := openOpencodeDB(dbPath)
	if err != nil {
		return ""
	}
	defer closeQuiet(db, "db", dbPath)

	// v2 stores the live title; fall back to the v1 table on older databases.
	for _, table := range []string{"session_v2", "session"} {
		if !tableExists(db, table) {
			continue
		}
		var title string
		if err := db.QueryRow("SELECT title FROM \""+table+"\" WHERE id = ? LIMIT 1", sessionID).Scan(&title); err == nil {
			return title
		}
	}
	return ""
}

// querySessionDirectoryDB returns the project directory recorded for a
// session on an already-open database, or false when the session is
// unknown. v2 first, v1 as fallback.
func querySessionDirectoryDB(db *sql.DB, sessionID string) (string, bool) {
	for _, table := range []string{"session_v2", "session"} {
		if !tableExists(db, table) {
			continue
		}
		var dir sql.NullString
		if err := db.QueryRow("SELECT directory FROM \""+table+"\" WHERE id = ? LIMIT 1", sessionID).Scan(&dir); err == nil && dir.String != "" {
			return dir.String, true
		}
	}
	return "", false
}
