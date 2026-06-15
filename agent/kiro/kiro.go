package kiro

import (
	"context"
	"encoding/json"
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
	core.RegisterAgent("kiro", New)
}

// Agent drives Kiro CLI using `kiro-cli chat <prompt> --no-interactive`.
type Agent struct {
	workDir      string
	model        string
	mode         string // "default" | "yolo"
	cmd          string
	agentProfile string
	agentEngine  string
	kasMode      string
	trustTools   string
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
	cmd, _ := opts["cmd"].(string)
	agentProfile, _ := opts["agent"].(string)
	agentEngine, _ := opts["agent_engine"].(string)
	kasMode, _ := opts["kas_mode"].(string)
	trustTools, _ := opts["trust_tools"].(string)

	resolvedCmd, err := resolveKiroCmd(cmd)
	if err != nil {
		return nil, err
	}

	return &Agent{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		cmd:          resolvedCmd,
		agentProfile: agentProfile,
		agentEngine:  agentEngine,
		kasMode:      kasMode,
		trustTools:   trustTools,
	}, nil
}

func resolveKiroCmd(cmd string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		cmd = "kiro-cli"
	}
	if path, err := exec.LookPath(cmd); err == nil {
		return path, nil
	}
	if cmd != "kiro-cli" {
		return "", fmt.Errorf("kiro: %q CLI not found in PATH, install Kiro CLI and ensure it is executable", cmd)
	}

	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "kiro-cli"))
	}
	candidates = append(candidates, "/opt/homebrew/bin/kiro-cli", "/usr/local/bin/kiro-cli")
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("kiro: %q CLI not found in PATH or common install locations, install Kiro CLI and ensure it is executable", cmd)
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "bypass", "trust", "trust-all", "trust_all", "trustall":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "kiro" }
func (a *Agent) CLIBinaryName() string  { return a.cmdName() }
func (a *Agent) CLIDisplayName() string { return "Kiro CLI" }

func (a *Agent) cmdName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == "" {
		return "kiro-cli"
	}
	return a.cmd
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("kiro: work_dir changed", "work_dir", dir)
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
	slog.Info("kiro: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	models, err := a.listModels(ctx)
	if err == nil && len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "auto", Desc: "Kiro CLI default model"},
	}
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	cmd := a.cmd
	if cmd == "" {
		cmd = "kiro-cli"
	}
	workDir := a.workDir
	model := a.model
	mode := a.mode
	agentProfile := a.agentProfile
	agentEngine := a.agentEngine
	kasMode := a.kasMode
	trustTools := a.trustTools
	extraEnv := append([]string{}, a.sessionEnv...)
	a.mu.Unlock()

	return newKiroSession(ctx, cmd, workDir, model, mode, agentProfile, agentEngine, kasMode, trustTools, sessionID, extraEnv)
}

func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.Lock()
	cmdName := a.cmd
	if cmdName == "" {
		cmdName = "kiro-cli"
	}
	workDir := a.workDir
	a.mu.Unlock()

	cmd := exec.CommandContext(ctx, cmdName, "chat", "--list-sessions", "--format", "json")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kiro: list sessions: %w", err)
	}
	return parseSessionList(out), nil
}

func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	a.mu.Lock()
	cmdName := a.cmd
	if cmdName == "" {
		cmdName = "kiro-cli"
	}
	workDir := a.workDir
	a.mu.Unlock()

	cmd := exec.CommandContext(ctx, cmdName, "chat", "--delete-session", sessionID)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kiro: delete session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("kiro: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Kiro CLI default tool trust behavior", DescZh: "Kiro CLI 默认工具信任行为"},
		{Key: "yolo", Name: "Trust All", NameZh: "信任全部工具", Desc: "Pass --trust-all-tools to Kiro CLI", DescZh: "向 Kiro CLI 传递 --trust-all-tools"},
	}
}

func (a *Agent) SkillDirs() []string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	dirs := []string{filepath.Join(absDir, ".kiro", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kiro", "skills"))
	}
	return dirs
}

func (a *Agent) ProjectMemoryFile() string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".kiro", "AGENTS.md")
}

func (a *Agent) CompressCommand() string { return "/compact" }

func (a *Agent) BaseOpts() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]any{
		"cmd":          a.cmd,
		"model":        a.model,
		"mode":         a.mode,
		"agent":        a.agentProfile,
		"agent_engine": a.agentEngine,
		"kas_mode":     a.kasMode,
		"trust_tools":  a.trustTools,
	}
	for k, v := range out {
		if s, ok := v.(string); ok && s == "" {
			delete(out, k)
		}
	}
	return out
}

func (a *Agent) listModels(ctx context.Context) ([]core.ModelOption, error) {
	a.mu.Lock()
	cmdName := a.cmd
	if cmdName == "" {
		cmdName = "kiro-cli"
	}
	workDir := a.workDir
	a.mu.Unlock()

	cmd := exec.CommandContext(ctx, cmdName, "chat", "--list-models", "--format", "json")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseModelList(out), nil
}

func parseModelList(data []byte) []core.ModelOption {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var models []core.ModelOption
	walkJSON(raw, func(m map[string]any) {
		name := firstString(m, "id", "name", "model", "model_id")
		if name == "" {
			return
		}
		desc := firstString(m, "display_name", "displayName", "description", "label")
		models = append(models, core.ModelOption{Name: name, Desc: desc})
	})
	return dedupeModels(models)
}

func parseSessionList(data []byte) []core.AgentSessionInfo {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var sessions []core.AgentSessionInfo
	walkJSON(raw, func(m map[string]any) {
		id := firstString(m, "id", "session_id", "sessionId", "sessionID")
		if id == "" {
			return
		}
		summary := firstString(m, "title", "name", "summary", "description")
		updated := firstString(m, "updated_at", "updatedAt", "updated", "last_active_at", "lastActiveAt", "modified_at", "modifiedAt")
		sessions = append(sessions, core.AgentSessionInfo{ID: id, Summary: summary, ModifiedAt: parseKiroTime(updated)})
	})
	return sessions
}

func parseKiroTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func walkJSON(v any, visit func(map[string]any)) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			walkJSON(item, visit)
		}
	case map[string]any:
		visit(x)
		for _, item := range x {
			walkJSON(item, visit)
		}
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			case float64:
				return fmt.Sprintf("%.0f", t)
			}
		}
	}
	return ""
}

func dedupeModels(in []core.ModelOption) []core.ModelOption {
	seen := make(map[string]bool)
	var out []core.ModelOption
	for _, model := range in {
		if model.Name == "" || seen[model.Name] {
			continue
		}
		seen[model.Name] = true
		out = append(out, model)
	}
	return out
}

var (
	_ core.Agent              = (*Agent)(nil)
	_ core.SessionDeleter     = (*Agent)(nil)
	_ core.ModelSwitcher      = (*Agent)(nil)
	_ core.ModeSwitcher       = (*Agent)(nil)
	_ core.WorkDirSwitcher    = (*Agent)(nil)
	_ core.MemoryFileProvider = (*Agent)(nil)
	_ core.SkillProvider      = (*Agent)(nil)
	_ core.ContextCompressor  = (*Agent)(nil)
	_ core.AgentOptsProvider  = (*Agent)(nil)
)
