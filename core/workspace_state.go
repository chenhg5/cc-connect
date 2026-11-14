package core

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// workspaceKeyMode holds the persisted workspace key form: WorkspaceKeyModeLegacy
// (default) or WorkspaceKeyModeSlash. Process-global because every engine has to
// agree — if two callers disagreed, the same workspace would hash to two session
// files. Set once at startup from config via SetWorkspaceKeyMode; it stays on
// "legacy" when nothing is configured, which is the historical behaviour.
var workspaceKeyMode atomic.Value

// SetWorkspaceKeyMode selects the persisted workspace key form. Anything other
// than "slash" falls back to "legacy", so an unset or unrecognised value can
// never change what an existing workspace resolves to.
func SetWorkspaceKeyMode(mode string) {
	if strings.EqualFold(strings.TrimSpace(mode), WorkspaceKeyModeSlash) {
		workspaceKeyMode.Store(WorkspaceKeyModeSlash)
		return
	}
	workspaceKeyMode.Store(WorkspaceKeyModeLegacy)
}

// WorkspaceKeyModeSlash is the opt-in key form; keep the literal in sync with
// config.WorkspaceKeyModeSlash (core does not import config).
const WorkspaceKeyModeSlash = "slash"

// WorkspaceKeyModeLegacy is the default key form.
const WorkspaceKeyModeLegacy = "legacy"

func workspaceKeyModeIsSlash() bool {
	if v, ok := workspaceKeyMode.Load().(string); ok {
		return v == WorkspaceKeyModeSlash
	}
	return false
}

// workspaceKeyForm applies the configured key form to a resolved path. "legacy"
// (default) keeps the native separators an older build would have produced, so
// upgrading alone changes nothing; "slash" normalizes to forward slashes.
func workspaceKeyForm(path string) string {
	if workspaceKeyModeIsSlash() {
		return filepath.ToSlash(path)
	}
	return path
}

// normalizeWorkspacePath cleans and resolves a workspace path to prevent
// mismatches caused by trailing slashes, symlinks, or relative segments.
// If the path cannot be resolved (e.g. doesn't exist yet), falls back to
// filepath.Clean only.
//
// The separator form of the result follows the configured workspace key mode
// (config "workspace_key_mode"): with the default "legacy" this is exactly the
// historical behaviour, and only the opt-in "slash" mode makes the key
// separator-consistent across platforms.
func normalizeWorkspacePath(path string) string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return workspaceKeyForm(cleaned)
	}
	if resolved != path {
		slog.Debug("workspace path normalized", "original", path, "normalized", resolved)
	}
	return workspaceKeyForm(resolved)
}

// legacyWorkspaceKey returns the key an older build persisted for the same
// workspace, or "" when no such key can exist. Returning "" is what keeps the
// fallback safe: the derived value is only ever used to *look up* state, and a
// key that cannot have been written by an older build must not name one.
//
// It returns "" in two cases:
//
//  1. The key form is "legacy" (the default). Nothing was normalized, so there is
//     no older form to fall back to, and deriving one could only point at
//     unrelated state.
//  2. The key does not look like a Windows path (see
//     looksLikeWindowsWorkspaceKey). Only a Windows build ever wrote
//     native-separator keys, so only those shapes can have a pre-upgrade form.
//     This is also what stops the fallback from inventing one: where "\" is a
//     legal file-name character, an unconditional swap turns A's canonical key
//     into B's canonical key — for A="/tmp/weird\name" it yields the perfectly
//     valid workspace "\tmp\weird\name", so A would read B's override and resume
//     B's session file, and ClearWorkspace*Override would delete B's entry.
//
// The shape test is applied on every platform, including Windows, so the rule is
// one rule everywhere and is covered by tests that run on Linux CI. The cost is
// that a *relative* Windows workspace ("repo/sub") gets no fallback: it has no
// drive letter or UNC prefix. That is deliberate — a relative work_dir is
// resolved against a working directory that changes between releases, so the old
// key would rarely be reproducible anyway.
//
// Callers pass two shapes: a bare workspace (the model-override and session-file
// paths) and the composite "<workspace>:<sessionKey>" used for directory
// overrides. Both work, because the shape test only inspects the prefix; session
// keys carry no path separators, so the swap leaves them alone.
func legacyWorkspaceKey(key string) string {
	if !workspaceKeyModeIsSlash() {
		return ""
	}
	if !looksLikeWindowsWorkspaceKey(key) {
		return ""
	}
	return strings.ReplaceAll(key, "/", `\`)
}

// legacyWorkspaceLookup returns the key an older build may have written for the
// same workspace, or "" when there is none. It collapses the derived key onto ""
// when it would be identical to the canonical one, so callers can treat a
// non-empty result as "a different, older key may exist".
//
// Callers must not use the result as a write target: it exists only to *read*
// state that predates the upgrade.
func legacyWorkspaceLookup(workspace string) string {
	legacy := legacyWorkspaceKey(workspace)
	if legacy == workspace {
		return ""
	}
	return legacy
}

// looksLikeWindowsWorkspaceKey reports whether key starts with a Windows drive
// letter ("C:/", "C:\") or a UNC prefix ("//", "\\"). Those are the only shapes a
// Windows build can have written in native-separator form, so they are the only
// ones worth deriving.
func looksLikeWindowsWorkspaceKey(key string) bool {
	if len(key) >= 3 && key[1] == ':' && (key[2] == '/' || key[2] == '\\') {
		c := key[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return strings.HasPrefix(key, "//") || strings.HasPrefix(key, `\\`)
}

// workspaceState holds the runtime state for a single workspace.
type workspaceState struct {
	mu           sync.Mutex
	workspace    string
	sessions     *SessionManager
	agent        Agent
	lastActivity time.Time
	activeTurns  int
}

func newWorkspaceState(workspace string) *workspaceState {
	return &workspaceState{
		workspace:    workspace,
		lastActivity: time.Now(),
	}
}

func (ws *workspaceState) Touch() {
	ws.mu.Lock()
	ws.lastActivity = time.Now()
	ws.mu.Unlock()
}

func (ws *workspaceState) BeginTurn() {
	ws.mu.Lock()
	ws.activeTurns++
	ws.lastActivity = time.Now()
	ws.mu.Unlock()
}

func (ws *workspaceState) EndTurn() {
	ws.mu.Lock()
	if ws.activeTurns > 0 {
		ws.activeTurns--
	}
	ws.lastActivity = time.Now()
	ws.mu.Unlock()
}

func (ws *workspaceState) HasActiveTurn() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.activeTurns > 0
}

func (ws *workspaceState) LastActivity() time.Time {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.lastActivity
}

// workspacePool manages a set of workspace states with idle reaping.
type workspacePool struct {
	mu          sync.RWMutex
	states      map[string]*workspaceState // workspace path -> state
	idleTimeout time.Duration
}

func newWorkspacePool(idleTimeout time.Duration) *workspacePool {
	return &workspacePool{
		states:      make(map[string]*workspaceState),
		idleTimeout: idleTimeout,
	}
}

// Get returns the state for a workspace.
func (p *workspacePool) Get(workspace string) *workspaceState {
	p.mu.RLock()
	state := p.states[workspace]
	p.mu.RUnlock()
	if state != nil {
		return state
	}

	normalized := normalizeWorkspacePath(workspace)
	if normalized == workspace {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.states[normalized]
}

// GetOrCreate returns or creates state for a workspace.
func (p *workspacePool) GetOrCreate(workspace string) *workspaceState {
	p.mu.Lock()
	if s, ok := p.states[workspace]; ok {
		p.mu.Unlock()
		return s
	}
	p.mu.Unlock()

	normalized := normalizeWorkspacePath(workspace)

	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.states[workspace]; ok {
		return s
	}
	if normalized != workspace {
		if s, ok := p.states[normalized]; ok {
			return s
		}
		workspace = normalized
	}

	s := newWorkspaceState(workspace)
	p.states[workspace] = s
	return s
}

// ReapIdle removes and returns workspace paths that have been idle longer than idleTimeout.
// A zero idleTimeout disables reaping entirely.
func (p *workspacePool) ReapIdle() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTimeout <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-p.idleTimeout)
	var reaped []string
	for path, state := range p.states {
		if state.HasActiveTurn() {
			continue
		}
		if state.LastActivity().Before(cutoff) {
			reaped = append(reaped, path)
			delete(p.states, path)
		}
	}
	return reaped
}

func (p *workspacePool) All() map[string]*workspaceState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]*workspaceState, len(p.states))
	for k, v := range p.states {
		result[k] = v
	}
	return result
}
