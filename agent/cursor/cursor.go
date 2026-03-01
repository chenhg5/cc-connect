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
	workDir   string
	model     string
	mode      string
	cmd       string // CLI binary name, default "agent"
	providers []core.ProviderConfig
	activeIdx int
	mu        sync.Mutex
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

func (a *Agent) Name() string { return "cursor" }

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	cmd := a.cmd
	extraEnv := a.providerEnvLocked()
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

func (a *Agent) Stop() error { return nil }

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
		summary := meta.Name
		if summary == "" || summary == "New Agent" {
			summary = sessionID[:12] + "..."
		}
		if utf8.RuneCountInString(summary) > 60 {
			summary = string([]rune(summary)[:60]) + "..."
		}

		sessions = append(sessions, core.AgentSessionInfo{
			ID:         sessionID,
			Summary:    summary,
			ModifiedAt: info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// sessionMeta holds metadata extracted from a Cursor chat store.db.
type sessionMeta struct {
	AgentID string
	Name    string
	Mode    string
}

// readSessionMeta reads the meta table from store.db without importing database/sql.
// The meta value at key "0" is a hex-encoded JSON string.
func readSessionMeta(dbPath string) sessionMeta {
	sqliteBin, err := exec.LookPath("sqlite3")
	if err != nil {
		return sessionMeta{}
	}

	out, err := exec.Command(sqliteBin, dbPath,
		"SELECT hex(value) FROM meta WHERE key='0' LIMIT 1;",
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
		return sessionMeta{}
	}

	var m struct {
		AgentID string `json:"agentId"`
		Name    string `json:"name"`
		Mode    string `json:"mode"`
	}
	if json.Unmarshal(decoded, &m) != nil {
		return sessionMeta{}
	}

	return sessionMeta{AgentID: m.AgentID, Name: m.Name, Mode: m.Mode}
}
