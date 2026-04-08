package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type projectStateData struct {
	WorkDirOverride string `json:"work_dir_override,omitempty"`
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
