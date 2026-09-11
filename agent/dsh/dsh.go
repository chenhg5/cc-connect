// Package dsh bridges cc-connect to a running DeepSeek Harness (dsh) web
// server. It implements the core.Agent interface by forwarding prompts to
// the server's typed RPC API (POST /api/session.prompt) and consuming the
// aggregated event stream (GET /api/events.mux) for streaming output,
// tool calls, permission approvals, and AskUserQuestion interactions.
//
// The dsh server must be running on the same machine (default
// http://127.0.0.1:3080, i.e. `dsh web`). Sessions are created with the
// project's work_dir as cwd, so each cc-connect project maps to its own
// dsh session; resuming reuses the persisted session on the server side.
package dsh

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("dsh", New)
}

// Agent drives a remote dsh web server instance.
type Agent struct {
	mu          sync.RWMutex
	baseURL     string // dsh web server, e.g. "http://127.0.0.1:3080"
	workDir     string // project directory used as the session cwd
	agentPreset string // optional dsh agent preset for new sessions
	model       string // optional "provider/model" override applied at session start
	timeout     time.Duration
	sessionEnv  []string

	// /mode + /reasoning runtime switches (applied at the next session start;
	// mode is also pushed to the live session immediately via /permission).
	mode        string
	effort      string
	effortCache []string   // efforts of the current model, refreshed per StartSession
	lastSession string     // most recent session id (for immediate mode application)
	client      *rpcClient // shared client for immediate commands
}

// normalizeMode maps cc-connect permission modes onto dsh permission presets.
//
//	"default" → dsh preset "workspace-write" (sandbox workspace-write, approval ask)
//	"yolo"    → dsh preset "danger-full-access" (sandbox danger-full-access, approval never)
//	"plan"    → dsh preset "read-only" (when the deployment defines it)
//
// Unknown values fall back to "default".
func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force", "bypasspermissions", "bypass":
		return "yolo"
	case "plan", "read-only", "readonly":
		return "plan"
	default:
		return "default"
	}
}

// modePreset maps a normalized mode to the dsh /permission preset name.
func modePreset(mode string) string {
	switch mode {
	case "yolo":
		return "danger-full-access"
	case "plan":
		return "read-only"
	default:
		return "workspace-write"
	}
}

// New creates the dsh agent.
//
// Options:
//
//	base_url      dsh web server URL (default "http://127.0.0.1:3080")
//	work_dir      project directory used as session cwd (default ".")
//	agent_preset  optional dsh agent preset name for new sessions
//	model         optional "provider/model" override applied at session start
//	timeout_mins  optional per-turn timeout in minutes (0 = unlimited)
//
// The server is NOT required to be reachable at construction time — startup
// stays non-blocking. Sessions fail with a clear message until `dsh web` is
// running.
func New(opts map[string]any) (core.Agent, error) {
	baseURL, err := parseBaseURL(optsString(opts, "base_url"))
	if err != nil {
		return nil, err
	}

	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	// dsh session headers require an absolute cwd — resolve before use.
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	} else {
		slog.Warn("dsh: cannot absolutize work_dir", "work_dir", workDir, "error", err)
	}

	agentPreset, _ := opts["agent_preset"].(string)
	model, _ := opts["model"].(string)

	var timeout time.Duration
	switch v := opts["timeout_mins"].(type) {
	case int64:
		timeout = time.Duration(v) * time.Minute
	case int:
		timeout = time.Duration(v) * time.Minute
	case float64:
		timeout = time.Duration(int64(v)) * time.Minute
	}

	slog.Info("dsh: agent created", "base_url", baseURL, "work_dir", workDir,
		"agent_preset", agentPreset, "model", model, "timeout", timeout)

	// Soft reachability probe: warn early about a missing server without
	// blocking project startup (the server may still be booting).
	client := newRPCClient(baseURL)
	probeCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := client.call(probeCtx, "session.list", map[string]any{}, nil); err != nil {
		slog.Warn("dsh: web server not reachable yet — sessions will fail until it is up",
			"base_url", baseURL, "error", err)
	} else {
		slog.Info("dsh: web server reachable", "base_url", baseURL)
	}

	return &Agent{
		baseURL:     baseURL,
		workDir:     workDir,
		agentPreset: agentPreset,
		model:       model,
		timeout:     timeout,
		client:      client,
	}, nil
}

func optsString(opts map[string]any, key string) string {
	if v, ok := opts[key].(string); ok {
		return v
	}
	return ""
}

func (a *Agent) Name() string { return "dsh" }

// StartSession creates or resumes a dsh session for this project and
// attaches the aggregated event stream.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	workDir, preset, model, timeout := a.workDir, a.agentPreset, a.model, a.timeout
	effort := a.effort
	env := append([]string(nil), a.sessionEnv...)
	client := a.client
	a.mu.RUnlock()

	s, err := newDshSession(ctx, client, workDir, sessionID, preset, model, effort, timeout, env)
	if err != nil {
		return nil, err
	}

	// Track the live session for immediate mode application and refresh the
	// reasoning-effort catalog for /reasoning.
	a.mu.Lock()
	a.lastSession = s.CurrentSessionID()
	if efforts := fetchEffortOptions(ctx, client, s.CurrentSessionID()); efforts != nil {
		a.effortCache = efforts
	}
	a.mu.Unlock()
	return s, nil
}

// ListSessions returns the dsh sessions persisted for this project's cwd.
func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	client := newRPCClient(a.baseURL)
	workDir := a.workDir
	a.mu.RUnlock()

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	absDir = filepath.Clean(absDir)

	var out struct {
		Items []sessionSummary `json:"items"`
	}
	if err := client.call(ctx, "session.list", map[string]any{}, &out); err != nil {
		return nil, fmt.Errorf("dsh: list sessions: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, s := range out.Items {
		if filepath.Clean(s.Cwd) != absDir {
			continue
		}
		info := core.AgentSessionInfo{
			ID:         s.SessionID,
			ModifiedAt: time.UnixMilli(int64(s.UpdatedAt)),
		}
		if s.Projections != nil && s.Projections.Values.Title != nil {
			info.Summary = truncate(*s.Projections.Values.Title, 60)
		}
		sessions = append(sessions, info)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})
	return sessions, nil
}

// Stop is a no-op: the dsh server is shared and keeps running.
func (a *Agent) Stop() error { return nil }

// ── WorkDirSwitcher ─────────────────────────────────────────────

func (a *Agent) SetWorkDir(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("dsh: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

// ── SessionIDValidator ──────────────────────────────────────────

// ValidateSessionID verifies the stored id belongs to a dsh session whose
// cwd is this project (issue #599 cross-project leakage guard).
func (a *Agent) ValidateSessionID(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	a.mu.RLock()
	client := newRPCClient(a.baseURL)
	workDir := a.workDir
	a.mu.RUnlock()

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	absDir = filepath.Clean(absDir)

	var out struct {
		Items []sessionSummary `json:"items"`
	}
	if err := client.call(ctx, "session.list", map[string]any{}, &out); err != nil {
		slog.Warn("dsh: validate session id: list failed", "error", err)
		return false
	}
	for _, s := range out.Items {
		if s.SessionID == sessionID && filepath.Clean(s.Cwd) == absDir {
			return true
		}
	}
	return false
}

// ── SessionEnvInjector ──────────────────────────────────────────

// SetSessionEnv stores the per-session cc-connect environment (CC_PROJECT,
// CC_SESSION_KEY, CC_DATA_DIR). Since dsh sessions run in a remote server
// process, the values are injected into the first prompt of each fresh
// session instead of the process environment.
func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
}

// ── MemoryFileProvider ──────────────────────────────────────────

// ProjectMemoryFile returns AGENTS.md: dsh natively loads AGENTS.md (and
// CLAUDE.md) from the session cwd, so cc-connect's instruction block
// (relay/cron guidance) lands where the harness already reads it.
func (a *Agent) ProjectMemoryFile() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string { return "" }

// ── ModelSwitcher ───────────────────────────────────────────────

// SetModel stores a "provider/model" (or bare model) override applied to the
// next session via session.selectModel.
func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("dsh: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// AvailableModels queries the catalog of the most recently started session.
// Returns nil when no session exists yet (the engine then shows the default
// model only).
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.RLock()
	client := newRPCClient(a.baseURL)
	a.mu.RUnlock()

	// Find any session of this project to read the catalog from.
	var out struct {
		Items []sessionSummary `json:"items"`
	}
	if err := client.call(ctx, "session.list", map[string]any{}, &out); err != nil {
		return nil
	}
	absDir := a.absWorkDir()
	var sessionID string
	for _, s := range out.Items {
		if filepath.Clean(s.Cwd) == absDir && s.SessionID != "" {
			sessionID = s.SessionID
			break
		}
	}
	if sessionID == "" {
		return nil
	}

	var models sessionModels
	if err := client.call(ctx, "session.models", map[string]any{"sessionId": sessionID}, &models); err != nil {
		slog.Debug("dsh: fetch models failed", "error", err)
		return nil
	}
	var opts []core.ModelOption
	for _, g := range models.Groups {
		for _, m := range g.Models {
			desc := m.Name
			if m.Description != "" {
				desc = m.Description
			}
			opts = append(opts, core.ModelOption{
				Name:  g.ID + "/" + m.ID,
				Desc:  desc,
				Alias: m.ID,
			})
		}
	}
	return opts
}

func (a *Agent) absWorkDir() string {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return filepath.Clean(workDir)
	}
	return filepath.Clean(abs)
}

// ── ModeSwitcher ────────────────────────────────────────────────

// SetMode stores the permission mode and, when a live session exists, applies
// it immediately by switching the dsh session's permission preset via the
// /permission command:
//
//	default → workspace-write  (approval prompts per tool)
//	yolo    → danger-full-access (no approval prompts)
//	plan    → read-only (when the deployment defines that preset)
//
// The engine restarts the session after a mode change, so the stored value
// also re-applies on the next StartSession.
func (a *Agent) SetMode(mode string) {
	mode = normalizeMode(mode)

	a.mu.Lock()
	a.mode = mode
	client := a.client
	sessionID := a.lastSession
	a.mu.Unlock()

	slog.Info("dsh: mode changed", "mode", mode)
	if sessionID != "" {
		go a.applyPermissionPreset(client, sessionID, mode)
	}
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认",
			Desc:   "Follow the dsh session's workspace-write preset (per-tool approval)",
			DescZh: "使用 dsh 的 workspace-write 预设（工具级审批）"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动",
			Desc:   "Switch the dsh session to danger-full-access (no approval prompts)",
			DescZh: "切换 dsh 会话到 danger-full-access（不再弹出审批）"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式",
			Desc:   "Switch the dsh session to read-only (requires a read-only preset)",
			DescZh: "切换 dsh 会话到只读（需要部署配置了 read-only 预设）"},
	}
}

// applyPermissionPreset runs /permission <preset> against one session via the
// command registry (best effort; failures are logged, never fatal).
func (a *Agent) applyPermissionPreset(client *rpcClient, sessionID, mode string) {
	if client == nil {
		return
	}
	preset := modePreset(mode)

	// Path 1 — session-scoped: run the `/permission <preset>` command through
	// the command registry (dsh builds that mount @deepseek-ai/dsh-commands).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var out struct {
		Matched bool `json:"matched"`
	}
	err := client.call(ctx, "command.execute", map[string]any{
		"sessionId": sessionID,
		"line":      "/permission " + preset,
	}, &out)
	cancel()
	if err == nil && out.Matched {
		slog.Info("dsh: permission preset applied (command)", "session_id", sessionID, "preset", preset)
		return
	}
	if err != nil {
		slog.Debug("dsh: /permission command unavailable, falling back to settings", "preset", preset, "error", err)
	} else {
		slog.Debug("dsh: /permission command not matched, falling back to settings", "preset", preset)
	}

	// Path 2 — fallback: write the `permission` settings namespace's
	// defaultPreset via settings.update (dsh npm builds without the command
	// registry, e.g. rc.6). This changes the default for FUTURE sessions,
	// which matches cc-connect's /mode flow: the engine restarts the session
	// after a mode change, so the next message creates a fresh session on the
	// new preset.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := client.call(ctx2, "settings.update", map[string]any{
		"ns":    "permission",
		"patch": map[string]any{"defaultPreset": preset},
	}, nil); err != nil {
		slog.Warn("dsh: mode switch failed (no /permission command and settings.update rejected)", "preset", preset, "error", err)
		return
	}
	slog.Info("dsh: permission preset applied (settings, applies to new sessions)", "preset", preset)
}

// ── ReasoningEffortSwitcher ─────────────────────────────────────

// SetReasoningEffort stores the effort override; it is applied at the next
// session start via session.selectModel(reasoningEffort).
func (a *Agent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effort = strings.TrimSpace(effort)
	slog.Info("dsh: reasoning effort changed", "effort", a.effort)
}

func (a *Agent) GetReasoningEffort() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.effort
}

// AvailableReasoningEfforts returns the effort options of the current model,
// refreshed whenever a session starts. Empty when no session has been queried
// yet (the engine then shows only the default).
func (a *Agent) AvailableReasoningEfforts() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.effortCache...)
}

// fetchEffortOptions reads the reasoning-effort catalog of one session's
// current model. Returns nil on any failure (callers keep the old cache).
func fetchEffortOptions(ctx context.Context, client *rpcClient, sessionID string) []string {
	if client == nil {
		return nil
	}
	var models sessionModels
	if err := client.call(ctx, "session.models", map[string]any{"sessionId": sessionID}, &models); err != nil {
		slog.Debug("dsh: fetch effort options failed", "error", err)
		return nil
	}
	for _, g := range models.Groups {
		for _, m := range g.Models {
			if m.ID != models.Current.Model {
				continue
			}
			var efforts []string
			for _, e := range m.Reasoning.Efforts {
				efforts = append(efforts, e.ID)
			}
			return efforts
		}
	}
	return nil
}

// ── AgentDoctorInfo + DoctorChecker ─────────────────────────────

func (a *Agent) CLIBinaryName() string  { return "dsh" }
func (a *Agent) CLIDisplayName() string { return "DeepSeek Harness (dsh)" }

// DoctorChecks reports the dsh web server's reachability.
func (a *Agent) DoctorChecks(ctx context.Context) []core.DoctorCheckResult {
	a.mu.RLock()
	client := newRPCClient(a.baseURL)
	baseURL := a.baseURL
	a.mu.RUnlock()

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := client.call(probeCtx, "session.list", map[string]any{}, nil)
	latency := time.Since(start)

	if err != nil {
		return []core.DoctorCheckResult{{
			Name:   "dsh web server",
			Status: core.DoctorFail,
			Detail: fmt.Sprintf("%s not reachable: %v (start it with `dsh web`)", baseURL, err),
		}}
	}
	return []core.DoctorCheckResult{{
		Name:    "dsh web server",
		Status:  core.DoctorPass,
		Detail:  fmt.Sprintf("%s reachable", baseURL),
		Latency: latency,
	}}
}

// Interface assertions.
var (
	_ core.Agent                   = (*Agent)(nil)
	_ core.WorkDirSwitcher         = (*Agent)(nil)
	_ core.SessionIDValidator      = (*Agent)(nil)
	_ core.SessionEnvInjector      = (*Agent)(nil)
	_ core.MemoryFileProvider      = (*Agent)(nil)
	_ core.ModelSwitcher           = (*Agent)(nil)
	_ core.ModeSwitcher            = (*Agent)(nil)
	_ core.ReasoningEffortSwitcher = (*Agent)(nil)
	_ core.AgentDoctorInfo         = (*Agent)(nil)
	_ core.DoctorChecker           = (*Agent)(nil)
	_ core.AgentSession            = (*dshSession)(nil)
	_ core.AgentSessionCanceller   = (*dshSession)(nil)
)
