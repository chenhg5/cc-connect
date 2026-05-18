package tmux

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("tmux", New)
}

// Agent drives a persistent tmux pane as an interactive shell agent.
// User messages are sent as keystrokes; output is captured by polling capture-pane.
type Agent struct {
	sessionName     string
	pane            string
	workDir         string
	autoCreate      bool
	shell           string
	initCmd         string // command to run once after a new session is created (e.g. "claude")
	startupWaitMs   int    // milliseconds to wait after init_command before accepting messages
	promptPat       string
	pollMs          int
	stripInputBlock bool     // strip the ───/❯/─── input area block from output
	stripPatterns   []string // per-line regex patterns to strip from output
	mu              sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	sessionName, _ := opts["session"].(string)
	if sessionName == "" {
		return nil, fmt.Errorf("tmux: 'session' option is required (name of the tmux session to attach)")
	}

	pane, _ := opts["pane"].(string)
	if pane == "" {
		pane = "0"
	}

	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}

	autoCreate := true
	if v, ok := opts["auto_create"].(bool); ok {
		autoCreate = v
	}

	shell, _ := opts["shell"].(string)
	initCmd, _ := opts["init_command"].(string)

	startupWaitMs := 0
	switch v := opts["startup_wait_ms"].(type) {
	case int64:
		startupWaitMs = int(v)
	case int:
		startupWaitMs = v
	case float64:
		startupWaitMs = int(v)
	}
	if startupWaitMs == 0 && initCmd != "" {
		startupWaitMs = 2000 // default: give the init process 2s to start
	}

	promptPat, _ := opts["prompt_pattern"].(string)
	if promptPat == "" {
		// Matches common shell prompts and Claude Code's ❯ prompt.
		promptPat = `[❯\$#>%]\s*$`
	}

	pollMs := 200
	switch v := opts["poll_interval_ms"].(type) {
	case int64:
		if v > 0 {
			pollMs = int(v)
		}
	case int:
		if v > 0 {
			pollMs = v
		}
	case float64:
		if v > 0 {
			pollMs = int(v)
		}
	}

	stripInputBlock := true
	if v, ok := opts["strip_input_block"].(bool); ok {
		stripInputBlock = v
	}

	var stripPatterns []string
	switch v := opts["strip_patterns"].(type) {
	case []string:
		stripPatterns = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				stripPatterns = append(stripPatterns, s)
			}
		}
	case nil:
		// default: strip Claude Code's mode status line
		stripPatterns = []string{`⏵⏵.*\(shift\+tab to cycle\)`}
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux: 'tmux' not found in PATH")
	}

	return &Agent{
		sessionName:     sessionName,
		pane:            pane,
		workDir:         workDir,
		autoCreate:      autoCreate,
		shell:           shell,
		initCmd:         initCmd,
		startupWaitMs:   startupWaitMs,
		promptPat:       promptPat,
		pollMs:          pollMs,
		stripInputBlock: stripInputBlock,
		stripPatterns:   stripPatterns,
	}, nil
}

func (a *Agent) Name() string { return "tmux" }

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	sessionName := a.sessionName
	pane := a.pane
	workDir := a.workDir
	autoCreate := a.autoCreate
	shell := a.shell
	initCmd := a.initCmd
	startupWaitMs := a.startupWaitMs
	promptPat := a.promptPat
	pollMs := a.pollMs
	stripInputBlock := a.stripInputBlock
	stripPatterns := a.stripPatterns
	a.mu.RUnlock()

	// When workDir is a real path (set via SetWorkDir in multi-workspace mode, or
	// configured directly), use a dedicated tmux window named after the directory so
	// each workspace gets its own Claude Code instance.  Otherwise fall back to the
	// legacy session:pane target.
	target, windowName := a.resolveTarget(sessionName, pane, workDir)

	sessionExists := tmuxSessionExists(sessionName)
	windowExists := sessionExists && tmuxWindowExists(target)

	if !sessionExists {
		if !autoCreate {
			return nil, fmt.Errorf("tmux: session %q does not exist and auto_create is disabled", sessionName)
		}
		if err := createTmuxSession(sessionName, windowName, workDir, shell); err != nil {
			return nil, fmt.Errorf("tmux: create session %q: %w", sessionName, err)
		}
		slog.Info("tmux: created session", "session", sessionName, "window", windowName, "work_dir", workDir)
	} else if !windowExists && windowName != pane {
		// Session exists but this workspace window does not yet — create it.
		if err := createTmuxWindow(sessionName, windowName, workDir); err != nil {
			return nil, fmt.Errorf("tmux: create window %q in session %q: %w", windowName, sessionName, err)
		}
		slog.Info("tmux: created window", "session", sessionName, "window", windowName, "work_dir", workDir)
	}

	newPane := !sessionExists || (!windowExists && windowName != pane)
	if newPane {
		// Always cd to the workspace directory so the shell (and any init_command)
		// starts in the right place, regardless of tmux's -c flag behaviour.
		if workDir != "" && workDir != "." {
			if err := sendKeys(target, "cd "+shellQuote(workDir)); err != nil {
				slog.Warn("tmux: cd failed", "work_dir", workDir, "err", err)
			}
		}
		if initCmd != "" {
			if err := sendKeys(target, initCmd); err != nil {
				slog.Warn("tmux: init_command failed", "cmd", initCmd, "err", err)
			} else {
				slog.Info("tmux: init_command sent", "cmd", initCmd, "target", target)
			}
			if startupWaitMs > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(startupWaitMs) * time.Millisecond):
				}
			}
		}
	}

	return newTmuxSession(ctx, target, sessionID, promptPat, time.Duration(pollMs)*time.Millisecond, stripInputBlock, stripPatterns, workDir)
}

// resolveTarget returns the tmux target string and the window name for the given workDir.
// When workDir is a real path the window is named with a hash suffix derived from the
// full path so that two workDirs sharing the same basename (e.g. /a/app and /b/app)
// never collide into the same tmux window.  When workDir is "." or empty the legacy
// session:pane target is returned and windowName == pane.
func (a *Agent) resolveTarget(sessionName, pane, workDir string) (target, windowName string) {
	if workDir != "" && workDir != "." {
		windowName = uniqueWindowName(workDir)
		return sessionName + ":" + windowName, windowName
	}
	return sessionName + ":" + pane, pane
}

// uniqueWindowName builds a tmux window name that is unique per workDir path.
// It appends a 4-hex-char FNV hash of the full path so that directories with
// the same basename do not collide (e.g. /repo/a/app → "app-1a2b",
// /repo/b/app → "app-3c4d").
func uniqueWindowName(workDir string) string {
	base := sanitizeWindowName(filepath.Base(workDir))
	h := fnv.New32a()
	_, _ = h.Write([]byte(workDir))
	return fmt.Sprintf("%s-%04x", base, h.Sum32()&0xffff)
}

// sanitizeWindowName makes a string safe to use as a tmux window name.
func sanitizeWindowName(name string) string {
	name = strings.NewReplacer(":", "-", ".", "-", " ", "-").Replace(name)
	if name == "" {
		return "default"
	}
	return name
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *Agent) Stop() error { return nil }

// WorkspaceAgentOptions implements core.WorkspaceAgentOptionSnapshotter so the
// engine can seed per-workspace agent instances with tmux-specific options
// (session name, strip patterns, etc.) in multi-workspace mode.
// work_dir is intentionally omitted; the engine sets it.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return map[string]any{
		"session":           a.sessionName,
		"pane":              a.pane,
		"auto_create":       a.autoCreate,
		"shell":             a.shell,
		"init_command":      a.initCmd,
		"startup_wait_ms":   a.startupWaitMs,
		"prompt_pattern":    a.promptPat,
		"poll_interval_ms":  a.pollMs,
		"strip_input_block": a.stripInputBlock,
		"strip_patterns":    a.stripPatterns,
	}
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("tmux: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}
