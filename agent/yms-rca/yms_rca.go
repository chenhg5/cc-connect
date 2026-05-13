// Package ymsagent implements the cc-connect adapter for the yms-rca CLI.
//
// yms-rca is launched as `yms-rca rpc --no-color`, talking the Pi RPC JSONL
// protocol over stdin/stdout. The adapter never passes --no-confirm: all
// yolo / dontAsk modes are realised by auto-answering extension_ui_response
// frames inside cc-connect, preserving audit trails.
package ymsagent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("yms-rca", New)
}

// Agent is the cc-connect agent adapter for yms-rca rpc.
type Agent struct {
	cmd            string
	workDir        string
	provider       string
	model          string
	thinking       string
	mode           string
	sessionDir     string
	sessionFile    string
	offline        bool
	confirmTimeout time.Duration

	mu         sync.Mutex
	sessionEnv []string
}

func New(opts map[string]any) (core.Agent, error) {
	a := &Agent{
		cmd:            getString(opts, "cmd", "yms-rca"),
		workDir:        getString(opts, "work_dir", "."),
		provider:       getString(opts, "provider", ""),
		model:          getString(opts, "model", ""),
		thinking:       getString(opts, "thinking", ""),
		mode:           normalizeMode(getString(opts, "mode", "default")),
		sessionDir:     getString(opts, "session_dir", ""),
		sessionFile:    getString(opts, "session_file", ""),
		offline:        getBool(opts, "offline", false),
		confirmTimeout: time.Duration(getInt(opts, "confirm_timeout_secs", 300)) * time.Second,
	}
	if a.provider != "" && a.model == "" {
		return nil, fmt.Errorf("yms-rca: provider %q requires model to be set", a.provider)
	}
	if _, err := exec.LookPath(a.cmd); err != nil {
		return nil, fmt.Errorf("yms-rca: %q not found in PATH", a.cmd)
	}
	return a, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto-approve":
		return "yolo"
	case "bypass", "bypasspermissions":
		return "bypassPermissions"
	case "dontask", "dont-ask":
		return "dontAsk"
	case "":
		return "default"
	default:
		return "default"
	}
}

// ── core.Agent ─────────────────────────────────────────────

func (a *Agent) Name() string { return "yms-rca" }
func (a *Agent) Stop() error  { return nil }

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	snapshot := &Agent{
		cmd:            a.cmd,
		workDir:        a.workDir,
		provider:       a.provider,
		model:          a.model,
		thinking:       a.thinking,
		mode:           a.mode,
		sessionDir:     a.sessionDir,
		sessionFile:    a.sessionFile,
		offline:        a.offline,
		confirmTimeout: a.confirmTimeout,
	}
	extraEnv := append([]string{}, a.sessionEnv...)
	a.mu.Unlock()

	// Resolve sessionID → session file (see implementation doc §B6).
	resumeFile, err := a.resolveResumeFile(sessionID)
	if err != nil {
		return nil, err
	}

	return newSession(ctx, snapshot, resumeFile, extraEnv)
}

// resolveResumeFile follows §B6 strictly — no fallback between branches.
func (a *Agent) resolveResumeFile(sessionID string) (string, error) {
	if sessionID != "" && sessionID != core.ContinueSession {
		sessDir := a.effectiveSessionDir()
		path := findSessionFile(sessDir, sessionID)
		if path == "" {
			return "", fmt.Errorf("yms-rca: session %q not found in %s", sessionID, sessDir)
		}
		return path, nil
	}
	if a.sessionFile != "" {
		if _, err := os.Stat(a.sessionFile); err != nil {
			return "", fmt.Errorf("yms-rca: configured session_file %q does not exist: %w", a.sessionFile, err)
		}
		return a.sessionFile, nil
	}
	return "", nil
}

// effectiveSessionDir returns the directory to scan for session files.
func (a *Agent) effectiveSessionDir() string {
	if a.sessionDir != "" {
		return a.sessionDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// yms-rca default — under ~/.yms-rca/sessions (best-effort; if upstream
	// uses a different layout, the user can set session_dir explicitly).
	return filepath.Join(home, ".yms-rca", "sessions")
}

// ── core.AgentDoctorInfo ───────────────────────────────────

func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "yms-rca" }

// ── core.ModeSwitcher ──────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("yms-rca: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask for high-risk operations", DescZh: "高危操作需人工确认"},
		{Key: "dontAsk", Name: "Don't Ask", NameZh: "全部拒绝", Desc: "Auto-deny high-risk operations", DescZh: "高危操作自动拒绝"},
		{Key: "bypassPermissions", Name: "Bypass", NameZh: "全部允许", Desc: "Auto-approve high-risk operations", DescZh: "高危操作自动批准"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
	}
}

// ── core.ModelSwitcher ─────────────────────────────────────

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("yms-rca: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	return nil // yms-rca config drives the model registry; no static list.
}

// ── core.ReasoningEffortSwitcher ───────────────────────────

func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.thinking = effort
	slog.Info("yms-rca: thinking changed", "thinking", effort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thinking
}

func (a *Agent) AvailableReasoningEfforts() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh"}
}

// ── core.WorkDirSwitcher ───────────────────────────────────

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("yms-rca: work_dir changed", "work_dir", dir)
}

// ── core.WorkspaceAgentOptionSnapshotter ───────────────────

// WorkspaceAgentOptions exports the constructor options needed to recreate
// this agent for a different workspace. work_dir is intentionally omitted
// per the interface contract — the engine sets it explicitly for each
// bound workspace. All other constructor-only fields (cmd, provider, model,
// thinking, mode, session_dir, session_file, offline, confirm_timeout_secs)
// are included so duplicate agents don't silently fall back to defaults.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"cmd":                  a.cmd,
		"provider":             a.provider,
		"model":                a.model,
		"thinking":             a.thinking,
		"mode":                 a.mode,
		"session_dir":          a.sessionDir,
		"session_file":         a.sessionFile,
		"offline":              a.offline,
		"confirm_timeout_secs": int(a.confirmTimeout / time.Second),
	}
}

// ── core.MemoryFileProvider ────────────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	a.mu.Lock()
	dir := a.workDir
	a.mu.Unlock()
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return filepath.Join(abs, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Pi SDK loads the global context file under agentDir = ~/.pi/agent.
	return filepath.Join(home, ".pi", "agent", "AGENTS.md")
}

// ── helpers ────────────────────────────────────────────────

func getString(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func getBool(m map[string]any, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func getInt(m map[string]any, key string, def int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}
