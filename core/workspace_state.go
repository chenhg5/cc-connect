package core

import (
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// normalizeWorkspacePath cleans and resolves a workspace path to prevent
// mismatches caused by trailing slashes, symlinks, or relative segments.
// If the path cannot be resolved (e.g. doesn't exist yet), falls back to
// filepath.Clean only.
func normalizeWorkspacePath(path string) string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return cleaned
	}
	if resolved != path {
		slog.Debug("workspace path normalized", "original", path, "normalized", resolved)
	}
	return resolved
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

type reapedWorkspace struct {
	Path  string
	Agent Agent
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

// AcquireTurn atomically verifies pool membership/agent identity and marks the
// workspace active. The returned lease must be released with EndTurn.
func (p *workspacePool) AcquireTurn(workspace string, expected Agent) *workspaceState {
	normalized := normalizeWorkspacePath(workspace)
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.states[workspace]
	if state == nil && normalized != workspace {
		state = p.states[normalized]
	}
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if expected != nil && !sameAgentInstance(state.agent, expected) {
		return nil
	}
	state.activeTurns++
	state.lastActivity = time.Now()
	return state
}

// ReapIdle removes and returns workspace paths that have been idle longer than idleTimeout.
// A zero idleTimeout disables reaping entirely.
func (p *workspacePool) ReapIdle() []string {
	reapedStates := p.ReapIdleStates()
	reaped := make([]string, 0, len(reapedStates))
	for _, state := range reapedStates {
		reaped = append(reaped, state.Path)
	}
	return reaped
}

// ReapIdleStates is the engine-facing form of ReapIdle. It captures the exact
// agent instance that owned each removed pool entry so later capability
// cleanup cannot delete state published by a same-path replacement.
func (p *workspacePool) ReapIdleStates() []reapedWorkspace {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTimeout <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-p.idleTimeout)
	var reaped []reapedWorkspace
	for path, state := range p.states {
		if state.HasActiveTurn() {
			continue
		}
		if state.LastActivity().Before(cutoff) {
			state.mu.Lock()
			agent := state.agent
			state.mu.Unlock()
			reaped = append(reaped, reapedWorkspace{Path: path, Agent: agent})
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
