// Package reasonix bridges cc-connect to a reasonix serve instance.
// It implements the core.Agent interface by forwarding prompts to reasonix's
// HTTP API (POST /submit) and consuming the SSE event stream (GET /events).
//
// Required agent option: serve_url (e.g. "http://localhost:8080").
package reasonix

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("reasonix", New)
}

// Agent drives a remote reasonix serve instance.
type Agent struct {
	mu       sync.RWMutex
	serveURL string // e.g. "http://localhost:8080"
	workDir  string // local project directory (for /dir and display)
	mode     string // permission mode: "default", "yolo", "plan"
}

// New creates a Reasonix agent.
// Required opts key: "serve_url".
func New(opts map[string]any) (core.Agent, error) {
	serveURL, _ := opts["serve_url"].(string)
	serveURL = strings.TrimRight(serveURL, "/")
	// Strip trailing slashes so path-join never produces "//submit".
	if serveURL == "" {
		return nil, fmt.Errorf("reasonix: serve_url is required")
	}
	if _, err := url.Parse(serveURL); err != nil {
		return nil, fmt.Errorf("reasonix: invalid serve_url %q: %w", serveURL, err)
	}

	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}

	mode := normalizeMode(opts)

	slog.Info("reasonix: agent created", "serve_url", serveURL, "work_dir", workDir, "mode", mode)
	return &Agent{
		serveURL: serveURL,
		workDir:  workDir,
		mode:     mode,
	}, nil
}

func normalizeMode(opts map[string]any) string {
	raw, _ := opts["mode"].(string)
	// "auto" and "force" are legacy aliases from reasonix serve's CLI flags;
	// both map to "yolo" (no interactive approval). All unrecognised values
	// fall back to "default" (interactive approval per tool).
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force":
		return "yolo"
	case "plan":
		return "plan"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "reasonix" }

// StartSession creates a session connected to reasonix serve.
// It establishes an SSE connection to /events and waits for it to be ready.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	slog.Info("reasonix: starting session", "session_id", sessionID)

	s, err := newSession(ctx, a.serveURL, a.workDir, sessionID, a.mode)
	if err != nil {
		return nil, fmt.Errorf("reasonix: start session: %w", err)
	}
	return s, nil
}

// ListSessions returns nil because reasonix doesn't expose session listing.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

// Stop shuts down the agent.
func (a *Agent) Stop() error { return nil }

// ── WorkDirSwitcher ──────────────────────────────────────────────

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("reasonix: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

// ── ModeSwitcher ─────────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(map[string]any{"mode": mode})
	slog.Info("reasonix: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认",
			Desc: "Prompt for approval on each tool use", DescZh: "每次工具调用都需要确认"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动",
			Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式",
			Desc: "Read-only plan mode, no execution", DescZh: "只读规划模式，不做修改"},
	}
}

// ── ContextCompressor ──────────────────────────────────────────

// CompressCommand returns "/compact" which gets sent as a prompt to reasonix.
// The session's Send() method will translate special commands.
func (a *Agent) CompressCommand() string { return "/compact" }

// ── MemoryFileProvider ─────────────────────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "REASONIX.md")
}

func (a *Agent) GlobalMemoryFile() string { return "" }

// Static interface assertions — ensure Agent remains compliant with core.Agent.
var _ core.Agent = (*Agent)(nil)        // *Agent satisfies core.Agent
var _ core.ModeSwitcher = (*Agent)(nil)          // mode switching
var _ core.WorkDirSwitcher = (*Agent)(nil)       // work dir switching
var _ core.ContextCompressor = (*Agent)(nil)     // compact support
var _ core.MemoryFileProvider = (*Agent)(nil)    // memory file support
