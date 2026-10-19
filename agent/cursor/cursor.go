package cursor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("cursor", New)
}

// Agent drives the Cursor Agent CLI (`agent`) using --print --output-format stream-json.
//
// Modes (maps to Cursor agent CLI flags):
//   - "default":  default permissions (ask permission for tools)
//   - "force":    --force (auto-approve tools unless explicitly denied)
//   - "plan":     --mode plan (read-only analysis)
//   - "ask":      --mode ask (Q&A style, read-only)
type Agent struct {
	workDir         string
	model           string
	modelPinned     bool // SetModel overrides the active provider default
	reasoningEffort string
	mode            string
	cmd             string   // CLI binary name, default "agent"
	cliExtraArgs    []string // extra args from cmd after the binary name
	configEnv       []string // env vars from [projects.agent.options.env]
	providers       []core.ProviderConfig
	activeIdx       int
	sessionEnv      []string
	modelsCache     []core.ModelOption
	modelsGen       uint64 // bumped when CLI identity/config changes
	mu              sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	effort, _ := opts["reasoning_effort"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	cmd, extraArgs := core.ParseCmdOpts(opts, "agent")
	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("cursor: %q CLI not found in PATH, install from https://cursor.com/docs/cli/installation", cmd)
	}

	return &Agent{
		workDir:         workDir,
		model:           model,
		reasoningEffort: normalizeCursorEffort(effort),
		mode:            mode,
		cmd:             cmd,
		cliExtraArgs:    extraArgs,
		configEnv:       core.ParseConfigEnv(opts),
		activeIdx:       -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "force", "yolo", "auto":
		return "force"
	case "plan":
		return "plan"
	case "ask":
		return "ask"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "cursor" }
func (a *Agent) CLIBinaryName() string  { return "agent" }
func (a *Agent) CLIDisplayName() string { return "Cursor Agent" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	a.bumpModelsGenLocked()
	slog.Info("cursor: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	a.modelPinned = true
	id := parseCursorModel(model)
	if strings.EqualFold(strings.TrimSpace(model), "auto") {
		a.reasoningEffort = ""
	} else if id.effort != "" {
		a.reasoningEffort = id.effort
	}
	slog.Info("cursor: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	return a.launchModel(context.Background())
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
	a.bumpModelsGenLocked()
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	mode := a.mode
	cmd := a.cmd
	extraArgs := append([]string{}, a.cliExtraArgs...)
	workDir := a.workDir
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	a.mu.RUnlock()

	return newCursorSession(ctx, cmd, extraArgs, workDir, a.launchModel(ctx), mode, sessionID, extraEnv)
}

// ListSessions reads sessions from Cursor Agent CLI chat storage.
// The CLI may store chats under ~/.cursor/chats or ~/.config/Cursor/chats
// depending on XDG_CONFIG_HOME and platform; all known locations are scanned.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	workDir := a.GetWorkDir()
	return listCursorSessions(workDir)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	dir, err := a.safeSessionDir(sessionID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (a *Agent) Stop() error { return nil }

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	dirs := []string{
		filepath.Join(absDir, ".cursor", "skills"),
		filepath.Join(absDir, ".claude", "skills"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".cursor", "skills"),
			filepath.Join(home, ".claude", "skills"),
		)
	}
	return dirs
}

// ── ContextCompressor implementation ──────────────────────────

func (a *Agent) CompressCommand() string { return "" }

// ── ModeSwitcher ────────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("cursor: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Trust workspace, ask before tool use", DescZh: "信任工作区，工具调用前询问"},
		{Key: "force", Name: "Force (YOLO)", NameZh: "强制执行", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式", Desc: "Read-only analysis, no edits", DescZh: "只读分析，不做修改"},
		{Key: "ask", Name: "Ask", NameZh: "问答模式", Desc: "Q&A style, read-only", DescZh: "问答风格，只读"},
	}
}

// ── ProviderSwitcher ────────────────────────────────────────────

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
	a.bumpModelsGenLocked()
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		a.modelPinned = false
		a.bumpModelsGenLocked()
		slog.Info("cursor: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			a.modelPinned = false
			a.bumpModelsGenLocked()
			slog.Info("cursor: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
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
		env = append(env, "CURSOR_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// ── Session listing ─────────────────────────────────────────────

// workspaceHash returns the MD5 hash that Cursor uses to organize chats by workspace.
func workspaceHash(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	h := md5.Sum([]byte(abs))
	return hex.EncodeToString(h[:])
}

// cursorChatsBaseDirs returns candidate Cursor Agent CLI chat roots, in priority order.
// When XDG_CONFIG_HOME is set the CLI stores under $XDG_CONFIG_HOME/Cursor/chats;
// otherwise it may use ~/.config/Cursor/chats or the legacy ~/.cursor/chats.
func cursorChatsBaseDirs(homeDir string) []string {
	var dirs []string
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		dirs = append(dirs, path)
	}

	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		add(filepath.Join(xdg, "Cursor", "chats"))
	}
	add(filepath.Join(homeDir, ".config", "Cursor", "chats"))
	add(filepath.Join(homeDir, ".cursor", "chats"))
	return dirs
}

func workspaceChatsDirs(homeDir, workDir string) []string {
	return cursorCandidateChatDirs(homeDir, workDir)
}

func findCursorSessionDir(homeDir, workDir, sessionID string) (string, error) {
	if err := rejectUnsafeSessionID(sessionID); err != nil {
		return "", err
	}
	var last error
	for _, chatsDir := range workspaceChatsDirs(homeDir, workDir) {
		dir := filepath.Join(chatsDir, sessionID)
		if err := sessionDirWithinWorkspace(chatsDir, dir); err != nil {
			if !os.IsNotExist(err) {
				last = err
			}
			continue
		}
		return dir, nil
	}
	if last != nil {
		return "", last
	}
	return "", fmt.Errorf("cursor: session not found: %s", sessionID)
}

func listCursorSessions(workDir string) ([]core.AgentSessionInfo, error) {
	homeDir, err := cursorUserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cursor: cannot determine home dir: %w", err)
	}

	chatsDirs := workspaceChatsDirs(homeDir, workDir)
	if len(chatsDirs) == 0 {
		return nil, nil
	}

	byID := make(map[string]core.AgentSessionInfo)
	for _, chatsDir := range chatsDirs {
		entries, err := os.ReadDir(chatsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("cursor: read chats dir %s: %w", chatsDir, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionID := entry.Name()
			sessionDir := filepath.Join(chatsDir, sessionID)
			if err := sessionDirWithinWorkspace(chatsDir, sessionDir); err != nil {
				continue
			}
			dbPath := filepath.Join(sessionDir, "store.db")

			var meta cursorMeta
			if decoded, err := readCursorMeta(context.Background(), dbPath); err == nil {
				meta = decoded
			}
			msgCount, firstUserMsg := countSessionMessages(dbPath, meta.RootBlobID)

			summary := cursorSessionSummary(meta.Name, firstUserMsg, sessionID)

			candidate := core.AgentSessionInfo{
				ID:           sessionID,
				Summary:      summary,
				MessageCount: msgCount,
				ModifiedAt:   cursorSessionActivityTime(dbPath),
			}
			if existing, ok := byID[sessionID]; !ok || candidate.ModifiedAt.After(existing.ModifiedAt) {
				byID[sessionID] = candidate
			}
		}
	}

	sessions := make([]core.AgentSessionInfo, 0, len(byID))
	for _, s := range byID {
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// cursorSessionActivityTime is the store.db mtime, or a newer nonempty
// store.db-wal mtime. Directory mtime is ignored: a read that creates an
// empty WAL sidecar updates the directory but not the conversation. Symlink
// sidecars and zero-length WAL files are ignored.
func cursorSessionActivityTime(dbPath string) time.Time {
	latest, ok := regularFileMtime(dbPath)
	if !ok {
		return time.Time{}
	}
	if wal, ok := nonemptyRegularMtime(dbPath + "-wal"); ok && wal.After(latest) {
		return wal
	}
	return latest
}

func regularFileMtime(path string) (time.Time, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func nonemptyRegularMtime(path string) (time.Time, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

var cursorUserQueryRE = regexp.MustCompile(`(?is)<user_query>\s*(.*?)\s*</user_query>`)

func cursorSessionSummary(metaName, firstUserMsg, sessionID string) string {
	summary := strings.TrimSpace(metaName)
	if summary == "" || strings.EqualFold(summary, "New Agent") {
		summary = strings.TrimSpace(firstUserMsg)
	}
	if summary == "" {
		if len(sessionID) > 12 {
			summary = sessionID[:12] + "..."
		} else {
			summary = sessionID
		}
	}
	if utf8.RuneCountInString(summary) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}
	return summary
}

func cursorMessageContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := m["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func extractCursorUserSummary(content any) string {
	text := strings.TrimSpace(cursorMessageContentText(content))
	if text == "" {
		return ""
	}

	if m := cursorUserQueryRE.FindStringSubmatch(text); len(m) == 2 {
		if query := strings.TrimSpace(m[1]); query != "" {
			return query
		}
	}

	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<user_info>") ||
		strings.HasPrefix(lower, "<open_and_recently_viewed_files>") ||
		strings.HasPrefix(lower, "<attached_files>") ||
		strings.HasPrefix(lower, "<agent_transcripts>") {
		return ""
	}

	if strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}

// countSessionMessages counts visible user and assistant turns from the full
// root blob. It uses the same child-ref walk and history filter as
// GetSessionHistory, with no 8KB prefix and no 80-message sample.
func countSessionMessages(dbPath, rootBlobID string) (int, string) {
	if !blobIDHex(rootBlobID) {
		return 0, ""
	}
	rootHex, err := runSQLite(context.Background(), dbPath, fmt.Sprintf(
		"SELECT hex(data) FROM blobs WHERE id='%s' LIMIT 1;", rootBlobID,
	))
	if err != nil {
		return 0, ""
	}
	rootBytes, err := hex.DecodeString(strings.TrimSpace(string(rootHex)))
	if err != nil || len(rootBytes) == 0 {
		return 0, ""
	}
	childIDs, err := cursorRootChildIDs(rootBytes)
	if err != nil || len(childIDs) == 0 {
		return 0, ""
	}
	blobs, err := readBlobHexBatched(context.Background(), dbPath, childIDs)
	if err != nil {
		return 0, ""
	}
	count := 0
	firstUser := ""
	for _, id := range childIDs {
		rawHex, ok := blobs[id]
		if !ok || rawHex == "" {
			continue
		}
		raw, err := hex.DecodeString(rawHex)
		if err != nil || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		entry, ok := historyEntryFromBlob(raw)
		if !ok {
			continue
		}
		count++
		if entry.Role == "user" && firstUser == "" {
			firstUser = entry.Content
		}
	}
	return count, firstUser
}
