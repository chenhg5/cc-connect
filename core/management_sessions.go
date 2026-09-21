package core

import (
	"path/filepath"
	"sort"
	"strings"
)

// managementSessionManager pairs a SessionManager with the workspace it serves.
type managementSessionManager struct {
	manager   *SessionManager
	workspace string // workspace path when known (live manager), else ""
	qualifier string // "" for the base manager; a unique key for workspace managers
}

// managementSession pairs a session with the manager and workspace that own it.
type managementSession struct {
	session   *Session
	manager   *SessionManager
	workspace string
	qualifier string
}

// managementSessionWorkspaceSep separates a workspace qualifier from a session
// id in the ids exposed to the management API.
//
// Session ids are assigned per SessionManager and therefore collide across
// workspaces (each manager starts at "s1"). In multi-workspace mode the id is
// qualified so it stays unique and can be resolved back to the owning manager
// via findManagementSession.
const managementSessionWorkspaceSep = "::"

func managementSessionID(session *Session, qualifier string) string {
	if qualifier == "" {
		return session.ID
	}
	return qualifier + managementSessionWorkspaceSep + session.ID
}

func sortedManagementKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// managementSessionManagers returns the engine's base session manager plus one
// entry per workspace manager. It includes both live workspace managers and
// persisted workspace stores that are not live in this process — the latter
// matters because the workspace pool is only populated once a workspace sees
// traffic, so without them the count would read 0 after every restart.
func (e *Engine) managementSessionManagers() []managementSessionManager {
	base := managementSessionManager{manager: e.sessions}
	if !e.multiWorkspace {
		return []managementSessionManager{base}
	}

	live := map[string]managementSessionManager{} // store path -> entry
	if e.workspacePool != nil {
		all := e.workspacePool.All()
		for _, path := range sortedManagementKeys(all) {
			ws := all[path]
			if ws == nil || ws.sessions == nil || ws.sessions == e.sessions {
				continue
			}
			live[ws.sessions.StorePath()] = managementSessionManager{
				manager:   ws.sessions,
				workspace: path,
				qualifier: path,
			}
		}
	}

	disk := map[string]managementSessionManager{}
	if dir := filepath.Dir(e.sessions.StorePath()); dir != "" && dir != "." {
		if matches, err := filepath.Glob(filepath.Join(dir, e.name+"_ws_*.json")); err == nil {
			for _, path := range matches {
				if _, ok := live[path]; ok {
					continue
				}
				sm := NewSessionManager(path)
				if len(sm.AllSessions()) == 0 {
					continue
				}
				disk[path] = managementSessionManager{
					manager:   sm,
					qualifier: filepath.Base(path),
				}
			}
		}
	}

	managers := []managementSessionManager{base}
	for _, key := range sortedManagementKeys(live) {
		managers = append(managers, live[key])
	}
	for _, key := range sortedManagementKeys(disk) {
		managers = append(managers, disk[key])
	}
	return managers
}

// managementSessionCount returns the number of sessions across the base manager
// and all workspace managers. In single-workspace mode this equals
// len(e.sessions.AllSessions()).
func (e *Engine) managementSessionCount() int {
	count := 0
	for _, mm := range e.managementSessionManagers() {
		count += len(mm.manager.AllSessions())
	}
	return count
}

// findManagementSession resolves a management session id to the session and the
// manager that owns it. It accepts both a bare id (single-workspace, or a first
// match while searching all managers) and the qualified id returned by
// managementSessionID.
func (e *Engine) findManagementSession(id string) (managementSession, bool) {
	if id == "" {
		return managementSession{}, false
	}

	managers := e.managementSessionManagers()

	if idx := strings.LastIndex(id, managementSessionWorkspaceSep); idx >= 0 {
		qualifier := id[:idx]
		raw := id[idx+len(managementSessionWorkspaceSep):]
		for _, mm := range managers {
			if mm.qualifier != qualifier {
				continue
			}
			if s := mm.manager.FindByID(raw); s != nil {
				return managementSession{session: s, manager: mm.manager, workspace: mm.workspace, qualifier: mm.qualifier}, true
			}
			return managementSession{}, false
		}
		// Unknown qualifier: fall back to searching the bare id below.
		id = raw
	}

	for _, mm := range managers {
		if s := mm.manager.FindByID(id); s != nil {
			return managementSession{session: s, manager: mm.manager, workspace: mm.workspace, qualifier: mm.qualifier}, true
		}
	}
	return managementSession{}, false
}
