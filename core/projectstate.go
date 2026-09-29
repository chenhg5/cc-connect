package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type projectStateData struct {
	WorkDirOverride         string            `json:"work_dir_override,omitempty"`
	WorkspaceDirOverrides   map[string]string `json:"workspace_dir_overrides,omitempty"`
	WorkspaceModelOverrides map[string]string `json:"workspace_model_overrides,omitempty"`
}

// ProjectStateStore persists lightweight runtime state for one project.
type ProjectStateStore struct {
	mu        sync.RWMutex
	storePath string
	state     projectStateData
}

func NewProjectStateStore(path string) *ProjectStateStore {
	ps := &ProjectStateStore{storePath: path}
	if path != "" {
		ps.load()
	}
	return ps
}

func (ps *ProjectStateStore) WorkDirOverride() string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.state.WorkDirOverride
}

func (ps *ProjectStateStore) SetWorkDirOverride(dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.state.WorkDirOverride = dir
}

func (ps *ProjectStateStore) WorkspaceDirOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	m := ps.state.WorkspaceDirOverrides
	if m == nil {
		return ""
	}
	if v, ok := m[workspace]; ok {
		return v
	}
	// Fall back to the pre-normalization (backslash) key for overrides
	// persisted by a build from before normalizeWorkspacePath emitted forward
	// slashes. The derived key is never written on a non-Windows host, so there
	// this lookup only ever misses.
	if legacy := legacyWorkspaceKey(workspace); legacy != workspace {
		return m[legacy]
	}
	return ""
}

func (ps *ProjectStateStore) SetWorkspaceDirOverride(workspace, dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		ps.state.WorkspaceDirOverrides = make(map[string]string)
	}
	ps.state.WorkspaceDirOverrides[workspace] = dir
}

func (ps *ProjectStateStore) ClearWorkspaceDirOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceDirOverrides, workspace)
	// Drop the legacy key too: reads fall back to it, so leaving it behind
	// would resurrect an override the caller just cleared.
	if legacy := legacyWorkspaceKey(workspace); legacy != workspace {
		delete(ps.state.WorkspaceDirOverrides, legacy)
	}
	if len(ps.state.WorkspaceDirOverrides) == 0 {
		ps.state.WorkspaceDirOverrides = nil
	}
}

func (ps *ProjectStateStore) WorkspaceModelOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	m := ps.state.WorkspaceModelOverrides
	if m == nil {
		return ""
	}
	if v, ok := m[workspace]; ok {
		return v
	}
	// See WorkspaceDirOverride for why the legacy key is consulted.
	if legacy := legacyWorkspaceKey(workspace); legacy != workspace {
		return m[legacy]
	}
	return ""
}

func (ps *ProjectStateStore) SetWorkspaceModelOverride(workspace, model string) {
	if model == "" {
		ps.ClearWorkspaceModelOverride(workspace)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		ps.state.WorkspaceModelOverrides = make(map[string]string)
	}
	ps.state.WorkspaceModelOverrides[workspace] = model
}

func (ps *ProjectStateStore) ClearWorkspaceModelOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceModelOverrides, workspace)
	// See ClearWorkspaceDirOverride: the legacy key must go as well.
	if legacy := legacyWorkspaceKey(workspace); legacy != workspace {
		delete(ps.state.WorkspaceModelOverrides, legacy)
	}
	if len(ps.state.WorkspaceModelOverrides) == 0 {
		ps.state.WorkspaceModelOverrides = nil
	}
}

func (ps *ProjectStateStore) ClearWorkDirOverride() {
	ps.SetWorkDirOverride("")
}

func (ps *ProjectStateStore) Save() {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	ps.saveLocked()
}

func (ps *ProjectStateStore) saveLocked() {
	if ps.storePath == "" {
		return
	}

	data, err := json.MarshalIndent(ps.state, "", "  ")
	if err != nil {
		slog.Error("project_state: failed to marshal", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(ps.storePath), 0o755); err != nil {
		slog.Error("project_state: failed to create dir", "path", ps.storePath, "error", err)
		return
	}
	if err := AtomicWriteFile(ps.storePath, data, 0o644); err != nil {
		slog.Error("project_state: failed to write", "path", ps.storePath, "error", err)
	}
}

func (ps *ProjectStateStore) load() {
	data, err := os.ReadFile(ps.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("project_state: failed to read", "path", ps.storePath, "error", err)
		}
		return
	}

	var state projectStateData
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("project_state: failed to unmarshal", "path", ps.storePath, "error", err)
		return
	}
	ps.state = state
}
