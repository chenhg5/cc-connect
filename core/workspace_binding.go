package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

const sharedWorkspaceBindingsKey = "shared"

// WorkspaceBinding maps a channel to a workspace directory.
type WorkspaceBinding struct {
	ChannelName string    `json:"channel_name"`
	Workspace   string    `json:"workspace"`
	BoundAt     time.Time `json:"bound_at"`
}

// WorkspaceBindingManager persists channel->workspace mappings.
// Top-level key is "project:<name>", second-level key is channel ID.
type WorkspaceBindingManager struct {
	mu        sync.RWMutex
	bindings  map[string]map[string]*WorkspaceBinding
	storePath string
}

func NewWorkspaceBindingManager(storePath string) *WorkspaceBindingManager {
	m := &WorkspaceBindingManager{
		bindings:  make(map[string]map[string]*WorkspaceBinding),
		storePath: storePath,
	}
	if storePath != "" {
		m.load()
	}
	return m
}

func (m *WorkspaceBindingManager) Bind(projectKey, channelID, channelName, workspace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[projectKey] == nil {
		m.bindings[projectKey] = make(map[string]*WorkspaceBinding)
	}
	m.bindings[projectKey][channelID] = &WorkspaceBinding{
		ChannelName: channelName,
		Workspace:   workspace,
		BoundAt:     time.Now(),
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Unbind(projectKey, channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proj := m.bindings[projectKey]; proj != nil {
		delete(proj, channelID)
		if len(proj) == 0 {
			delete(m.bindings, projectKey)
		}
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Lookup(projectKey, channelID string) *WorkspaceBinding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if proj := m.bindings[projectKey]; proj != nil {
		return proj[channelID]
	}
	return nil
}

// LookupEffective returns the effective binding for a channel, checking the
// current project first and then the shared routing layer.
func (m *WorkspaceBindingManager) LookupEffective(projectKey, channelID string) (*WorkspaceBinding, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if proj := m.bindings[projectKey]; proj != nil {
		if b := proj[channelID]; b != nil {
			return b, projectKey
		}
	}
	if shared := m.bindings[sharedWorkspaceBindingsKey]; shared != nil {
		if b := shared[channelID]; b != nil {
			return b, sharedWorkspaceBindingsKey
		}
	}
	return nil, ""
}

func (m *WorkspaceBindingManager) ListByProject(projectKey string) map[string]*WorkspaceBinding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*WorkspaceBinding)
	if proj := m.bindings[projectKey]; proj != nil {
		for k, v := range proj {
			result[k] = v
		}
	}
	return result
}

func (m *WorkspaceBindingManager) saveLocked() {
	if m.storePath == "" {
		return
	}
	data, err := json.MarshalIndent(m.bindings, "", "  ")
	if err != nil {
		slog.Error("workspace bindings: marshal error", "err", err)
		return
	}
	if err := AtomicWriteFile(m.storePath, data, 0o644); err != nil {
		slog.Error("workspace bindings: save error", "err", err)
	}
}

func (m *WorkspaceBindingManager) load() {
	data, err := os.ReadFile(m.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("workspace bindings: load error", "err", err)
		}
		return
	}
	if err := json.Unmarshal(data, &m.bindings); err != nil {
		slog.Error("workspace bindings: unmarshal error", "err", err)
	}
}
