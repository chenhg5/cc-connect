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
	"sort"
	"strings"
	"sync"
	"encoding/json"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("cursor", New)
}

// Agent drives the Cursor Agent CLI (`agent`) using --print --output-format stream-json.
//
// Modes (maps to Cursor agent CLI flags):
//   - "default":  --trust only (ask permission for tools)
//   - "force":    --trust --force (auto-approve tools unless explicitly denied)
//   - "plan":     --trust --mode plan (read-only analysis)
//   - "ask":      --trust --mode ask (Q&A style, read-only)
type Agent struct {
	workDir    string
	model      string
	mode       string
	cmd        string // CLI binary name, default "agent"
	providers  []core.ProviderConfig
	activeIdx  int
	sessionEnv []string
	mu         sync.Mutex
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
		cmd = "agent"
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("cursor: %q CLI not found in PATH, install with: npm i -g @anthropic-ai/cursor-agent (or from Cursor IDE settings)", cmd)
	}

	return &Agent{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		cmd:       cmd,
		activeIdx: -1,
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
	slog.Info("cursor: work_dir changed", "work_dir", dir)
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
	slog.Info("cursor: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.Lock()
	cmd := a.cmd
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	a.mu.Unlock()

	if models := fetchModelsFromAgentCLI(ctx, cmd, extraEnv); len(models) > 0 {
		return models
	}
	return cursorFallbackModels()
}

// fetchModelsFromAgentCLI runs `agent models` and parses the output.
// Output format: "model-id - Display Name  (current)" or "model-id - Display Name"
func fetchModelsFromAgentCLI(ctx context.Context, cmd string, extraEnv []string) []core.ModelOption {
	c := exec.CommandContext(ctx, cmd, "models")
	c.Env = append(os.Environ(), extraEnv...)
	out, err := c.Output()
	if err != nil {
		slog.Debug("cursor: agent models failed", "error", err)
		return nil
	}

	var models []core.ModelOption
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "Available models" || strings.HasPrefix(line, "Tip:") {
			continue
		}
		idx := strings.Index(line, " - ")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		desc := strings.TrimSpace(line[idx+3:])
		if name == "" {
			continue
		}
		// Remove trailing markers like "(current)", "(default)"
		desc = strings.TrimSuffix(desc, " (current)")
		desc = strings.TrimSuffix(desc, " (default)")
		desc = strings.TrimSpace(desc)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, core.ModelOption{Name: name, Desc: desc})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

func cursorFallbackModels() []core.ModelOption {
	return []core.ModelOption{
		{Name: "claude-sonnet-4-20250514", Desc: "Claude Sonnet 4"},
		{Name: "claude-opus-4-20250514", Desc: "Claude Opus 4"},
		{Name: "gpt-4o", Desc: "GPT-4o"},
		{Name: "gemini-2.5-pro", Desc: "Gemini 2.5 Pro"},
		{Name: "cursor-small", Desc: "Cursor Small (fast)"},
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
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newCursorSession(ctx, cmd, a.workDir, model, mode, sessionID, extraEnv)
}

// ListSessions reads sessions from ~/.cursor/chats/<workspace_hash>/.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listCursorSessions(a.workDir)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cursor: cannot determine home dir: %w", err)
	}
	hash := workspaceHash(a.workDir)
	dir := filepath.Join(homeDir, ".cursor", "chats", hash, sessionID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return os.RemoveAll(dir)
}

func (a *Agent) Stop() error { return nil }

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".claude", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
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
	a.mu.Lock()
	defer a.mu.Unlock()
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
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		slog.Info("cursor: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("cursor: provider switched", "provider", name)
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

func listCursorSessions(workDir string) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cursor: cannot determine home dir: %w", err)
	}

	hash := workspaceHash(workDir)
	chatsDir := filepath.Join(homeDir, ".cursor", "chats", hash)

	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cursor: read chats dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		dbPath := filepath.Join(chatsDir, sessionID, "store.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		meta := readSessionMeta(dbPath)
		msgCount, firstUserMsg := countSessionMessages(dbPath, meta.RootBlobID)

		summary := meta.Name
		if summary == "" || summary == "New Agent" {
			if firstUserMsg != "" {
				summary = firstUserMsg
			} else {
				summary = sessionID[:12] + "..."
			}
		}
		if utf8.RuneCountInString(summary) > 60 {
			summary = string([]rune(summary)[:60]) + "..."
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

// sessionMeta holds metadata extracted from a Cursor chat store.db.
type sessionMeta struct {
	AgentID    string
	Name       string
	Mode       string
	RootBlobID string
}

// readSessionMeta reads the meta table from store.db without importing database/sql.
// The meta value at key "0" is already a hex-encoded JSON string in the TEXT column,
// so we read it directly (no extra hex() wrapping) and decode once.
func readSessionMeta(dbPath string) sessionMeta {
	sqliteBin, err := exec.LookPath("sqlite3")
	if err != nil {
		return sessionMeta{}
	}

	out, err := exec.Command(sqliteBin, dbPath,
		"SELECT value FROM meta WHERE key='0' LIMIT 1;",
	).Output()
	if err != nil {
		return sessionMeta{}
	}

	hexStr := strings.TrimSpace(string(out))
	if hexStr == "" {
		return sessionMeta{}
	}

	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		// Fallback: value might be raw JSON (not hex-encoded) in some versions
		decoded = []byte(hexStr)
	}

	var m struct {
		AgentID    string `json:"agentId"`
		Name       string `json:"name"`
		Mode       string `json:"mode"`
		RootBlobID string `json:"latestRootBlobId"`
	}
	if json.Unmarshal(decoded, &m) != nil {
		return sessionMeta{}
	}

	return sessionMeta{AgentID: m.AgentID, Name: m.Name, Mode: m.Mode, RootBlobID: m.RootBlobID}
}

// countSessionMessages reads the root blob from store.db and counts conversation
// messages. It also returns the first user message text as a summary fallback.
// The root blob uses a protobuf-like encoding where field 1 (tag 0x0a, length 0x20)
// entries are 32-byte SHA-256 references to child message blobs.
func countSessionMessages(dbPath, rootBlobID string) (int, string) {
	if rootBlobID == "" {
		return 0, ""
	}
	sqliteBin, err := exec.LookPath("sqlite3")
	if err != nil {
		return 0, ""
	}

	// Read root blob header (first ~8KB is enough for counting refs)
	out, err := exec.Command(sqliteBin, dbPath,
		fmt.Sprintf("SELECT hex(substr(data,1,8192)) FROM blobs WHERE id='%s' LIMIT 1;", rootBlobID),
	).Output()
	if err != nil {
		return 0, ""
	}
	rootHex := strings.TrimSpace(string(out))
	rootBytes, err := hex.DecodeString(rootHex)
	if err != nil || len(rootBytes) == 0 {
		return 0, ""
	}

	// Count field-1 entries (0x0a 0x20 + 32-byte hash)
	var childIDs []string
	i := 0
	for i+33 < len(rootBytes) && rootBytes[i] == 0x0a && rootBytes[i+1] == 0x20 {
		childIDs = append(childIDs, hex.EncodeToString(rootBytes[i+2:i+34]))
		i += 34
	}
	if len(childIDs) == 0 {
		return 0, ""
	}

	// Read the first few children to find the first real user message for summary,
	// and count roles to determine message count (excluding system).
	msgCount := 0
	var firstUserMsg string
	limit := len(childIDs)
	if limit > 80 {
		limit = 80
	}

	// Build a single query to read multiple children
	var ids []string
	for _, cid := range childIDs[:limit] {
		ids = append(ids, "'"+cid+"'")
	}
	query := fmt.Sprintf(
		"SELECT id, data FROM blobs WHERE id IN (%s);",
		strings.Join(ids, ","),
	)
	blobOut, err := exec.Command(sqliteBin, "-separator", "|", dbPath, query).Output()
	if err != nil {
		// Fallback: estimate from child count minus 1 (system message)
		if len(childIDs) > 1 {
			return len(childIDs) - 1, ""
		}
		return 0, ""
	}

	roleCount := make(map[string]int)
	blobMap := make(map[string][]byte)
	for _, line := range strings.Split(string(blobOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		blobMap[parts[0]] = []byte(parts[1])
	}

	for _, cid := range childIDs[:limit] {
		raw, ok := blobMap[cid]
		if !ok || len(raw) == 0 {
			continue
		}
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		roleCount[msg.Role]++
		if msg.Role == "user" && firstUserMsg == "" {
			if s, ok := msg.Content.(string); ok {
				s = strings.TrimSpace(s)
				// Skip injected context (XML tags, conversation summaries, etc.)
				if len(s) > 0 && !strings.HasPrefix(s, "<") && !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "{") {
					if utf8.RuneCountInString(s) > 50 {
						s = string([]rune(s)[:50]) + "..."
					}
					firstUserMsg = s
				}
			}
		}
	}

	msgCount = roleCount["user"] + roleCount["assistant"]
	if limit < len(childIDs) {
		// Extrapolate for remaining children
		total := len(childIDs)
		msgCount = msgCount * total / limit
	}

	return msgCount, firstUserMsg
}
