package codex

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("codex", New)
}

// Agent drives OpenAI Codex CLI using `codex exec --json`.
//
// Modes (maps to codex exec flags):
//   - "suggest":   default, no special flags (safe commands only)
//   - "auto-edit": --full-auto (sandbox-protected auto execution)
//   - "full-auto": --full-auto (sandbox-protected auto execution)
//   - "yolo":      --dangerously-bypass-approvals-and-sandbox
type Agent struct {
	workDir    string
	model      string
	mode       string // "suggest" | "auto-edit" | "full-auto" | "yolo"
	providers  []core.ProviderConfig
	activeIdx  int // -1 = no provider set
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

	if _, err := exec.LookPath("codex"); err != nil {
		return nil, fmt.Errorf("codex: 'codex' CLI not found in PATH, install with: npm install -g @openai/codex")
	}

	return &Agent{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		activeIdx: -1,
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

func (a *Agent) Name() string { return "codex" }

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	mode := a.mode
	model := a.model
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newCodexSession(ctx, a.workDir, model, mode, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listCodexSessions(a.workDir)
}

func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	return getSessionHistory(sessionID, limit)
}

func (a *Agent) Stop() error { return nil }

// SetMode changes the approval mode for future sessions.
func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("codex: approval mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// ── ProviderSwitcher implementation ──────────────────────────

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
			slog.Info("codex: provider switched", "provider", name)
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
		env = append(env, "OPENAI_API_KEY="+p.APIKey)
	}
	if p.BaseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+p.BaseURL)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "suggest", Name: "Suggest", NameZh: "建议", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "auto-edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Auto-approve file edits, ask for shell commands", DescZh: "自动允许文件编辑，Shell 命令需确认"},
		{Key: "full-auto", Name: "Full Auto", NameZh: "全自动", Desc: "Auto-approve with workspace sandbox", DescZh: "自动通过（工作区沙箱）"},
		{Key: "yolo", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Bypass all approvals and sandbox", DescZh: "跳过所有审批和沙箱"},
	}
}
