package gemini

import (
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
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("gemini", New)
}

// Agent drives the Gemini CLI in headless mode using -p --output-format stream-json.
//
// Modes (maps to Gemini CLI approval flags):
//   - "default":   standard approval mode (prompt for each tool use)
//   - "auto_edit": auto-approve edit tools, ask for others
//   - "yolo":      auto-approve all tools (-y / --approval-mode yolo)
//   - "plan":      read-only plan mode (--approval-mode plan)
type Agent struct {
	workDir    string
	model      string
	mode       string
	cmd        string // CLI binary name, default "gemini"
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
		cmd = "gemini"
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("gemini: %q CLI not found in PATH, install with: npm i -g @google/gemini-cli", cmd)
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
	case "yolo", "auto", "force", "bypasspermissions":
		return "yolo"
	case "auto_edit", "autoedit", "edit", "acceptedits":
		return "auto_edit"
	case "plan":
		return "plan"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "gemini" }

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

	return newGeminiSession(ctx, cmd, workDir, model, mode, sessionID, extraEnv)
}

// ListSessions reads sessions from ~/.gemini/tmp/<project_hash>/chats/.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listGeminiSessions(a.workDir)
}

func (a *Agent) Stop() error { return nil }

// ── ModeSwitcher ────────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("gemini: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Prompt for approval on each tool use", DescZh: "每次工具调用都需要确认"},
		{Key: "auto_edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Auto-approve edit tools, ask for others", DescZh: "编辑工具自动通过，其他仍需确认"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式", Desc: "Read-only plan mode, no execution", DescZh: "只读规划模式，不做修改"},
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
			slog.Info("gemini: provider switched", "provider", name)
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
		env = append(env, "GEMINI_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// ── Session listing ─────────────────────────────────────────────

// geminiProjectHash computes the directory name Gemini CLI uses under ~/.gemini/tmp/.
// Gemini uses a hash of the absolute project path.
func geminiProjectHash(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	return filepath.Base(abs)
}

// sessionFile represents the JSON structure of a Gemini CLI session file.
type sessionFile struct {
	SessionID   string    `json:"sessionId"`
	ProjectHash string    `json:"projectHash"`
	StartTime   time.Time `json:"startTime"`
	LastUpdated time.Time `json:"lastUpdated"`
	Messages    []struct {
		Type    string `json:"type"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
	Kind string `json:"kind"`
}

func listGeminiSessions(workDir string) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("gemini: cannot determine home dir: %w", err)
	}

	projName := geminiProjectHash(workDir)
	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", projName, "chats")

	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gemini: read chats dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(chatsDir, entry.Name()))
		if err != nil {
			continue
		}

		var sf sessionFile
		if json.Unmarshal(data, &sf) != nil || sf.SessionID == "" {
			continue
		}

		summary := extractSessionSummary(&sf)
		if utf8.RuneCountInString(summary) > 60 {
			summary = string([]rune(summary)[:60]) + "..."
		}

		msgCount := len(sf.Messages)
		modTime := sf.LastUpdated
		if modTime.IsZero() {
			modTime = sf.StartTime
		}

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sf.SessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   modTime,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// extractSessionSummary picks the first user message as the session summary.
func extractSessionSummary(sf *sessionFile) string {
	for _, msg := range sf.Messages {
		if msg.Type != "user" {
			continue
		}
		for _, c := range msg.Content {
			text := strings.TrimSpace(c.Text)
			if text != "" {
				lines := strings.SplitN(text, "\n", 2)
				return strings.TrimSpace(lines[0])
			}
		}
	}
	if len(sf.SessionID) > 12 {
		return sf.SessionID[:12] + "..."
	}
	return sf.SessionID
}
