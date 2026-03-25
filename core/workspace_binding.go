package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
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
// Top-level key is "project:<name>", second-level key is a workspace channel key.
type WorkspaceBindingManager struct {
	mu                sync.RWMutex
	bindings          map[string]map[string]*WorkspaceBinding
	storePath         string
	lastLoadedModTime time.Time
	lastLoadedSize    int64
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

func legacyWorkspaceChannelKey(channelKey string) string {
	if i := strings.IndexByte(channelKey, ':'); i >= 0 {
		return channelKey[i+1:]
	}
	return channelKey
}

func workspaceChannelKeyCandidates(channelKey string) []string {
	if channelKey == "" {
		return nil
	}
	legacyKey := legacyWorkspaceChannelKey(channelKey)
	if legacyKey == channelKey {
		return []string{channelKey}
	}
	return []string{channelKey, legacyKey}
}

func (m *WorkspaceBindingManager) lookupLocked(projectKey, channelKey string) *WorkspaceBinding {
	proj := m.bindings[projectKey]
	if proj == nil {
		return nil
	}
	for _, candidate := range workspaceChannelKeyCandidates(channelKey) {
		if b := proj[candidate]; b != nil {
			return b
		}
	}
	return nil
}

func (m *WorkspaceBindingManager) Bind(projectKey, channelKey, channelName, workspace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	if m.bindings[projectKey] == nil {
		m.bindings[projectKey] = make(map[string]*WorkspaceBinding)
	}
	m.bindings[projectKey][channelKey] = &WorkspaceBinding{
		ChannelName: channelName,
		Workspace:   workspace,
		BoundAt:     time.Now(),
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Unbind(projectKey, channelKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	if proj := m.bindings[projectKey]; proj != nil {
		for _, candidate := range workspaceChannelKeyCandidates(channelKey) {
			delete(proj, candidate)
		}
		if len(proj) == 0 {
			delete(m.bindings, projectKey)
		}
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Lookup(projectKey, channelKey string) *WorkspaceBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	return m.lookupLocked(projectKey, channelKey)
}

// LookupEffective returns the effective binding for a channel, checking the
// current project first and then the shared routing layer.
func (m *WorkspaceBindingManager) LookupEffective(projectKey, channelKey string) (*WorkspaceBinding, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
	if b := m.lookupLocked(projectKey, channelKey); b != nil {
		return b, projectKey
	}
	if b := m.lookupLocked(sharedWorkspaceBindingsKey, channelKey); b != nil {
		return b, sharedWorkspaceBindingsKey
	}
	return nil, ""
}

func (m *WorkspaceBindingManager) ListByProject(projectKey string) map[string]*WorkspaceBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
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
		return
	}
	if info, err := os.Stat(m.storePath); err == nil {
		m.lastLoadedModTime = info.ModTime()
		m.lastLoadedSize = info.Size()
	}
}

func (m *WorkspaceBindingManager) load() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshLocked()
}

func (m *WorkspaceBindingManager) refreshLocked() {
	if m.storePath == "" {
		return
	}
	info, err := os.Stat(m.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			m.bindings = make(map[string]map[string]*WorkspaceBinding)
			m.lastLoadedModTime = time.Time{}
			m.lastLoadedSize = 0
			return
		}
		slog.Error("workspace bindings: stat error", "err", err)
		return
	}
	if !m.lastLoadedModTime.IsZero() && info.ModTime().Equal(m.lastLoadedModTime) && info.Size() == m.lastLoadedSize {
		return
	}

	data, err := os.ReadFile(m.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("workspace bindings: load error", "err", err)
		}
		return
	}
	loaded := make(map[string]map[string]*WorkspaceBinding)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &loaded); err != nil {
			slog.Error("workspace bindings: unmarshal error", "err", err)
			return
		}
	}
	m.bindings = loaded
	m.lastLoadedModTime = info.ModTime()
	m.lastLoadedSize = info.Size()
}
