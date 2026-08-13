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
	baseURL, workDir, preset, model, timeout := a.baseURL, a.workDir, a.agentPreset, a.model, a.timeout
	env := append([]string(nil), a.sessionEnv...)
	a.mu.RUnlock()

	s, err := newDshSession(ctx, newRPCClient(baseURL), workDir, sessionID, preset, model, timeout, env)
	if err != nil {
		return nil, err
	}
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
	_ core.Agent                 = (*Agent)(nil)
	_ core.WorkDirSwitcher       = (*Agent)(nil)
	_ core.SessionIDValidator    = (*Agent)(nil)
	_ core.SessionEnvInjector    = (*Agent)(nil)
	_ core.MemoryFileProvider    = (*Agent)(nil)
	_ core.ModelSwitcher         = (*Agent)(nil)
	_ core.AgentDoctorInfo       = (*Agent)(nil)
	_ core.DoctorChecker         = (*Agent)(nil)
	_ core.AgentSession          = (*dshSession)(nil)
	_ core.AgentSessionCanceller = (*dshSession)(nil)
)
