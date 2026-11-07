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
//
// The qualifier is opaque and URL-safe (never a filesystem path): it is
// derived from the workspace session store file, so the same workspace gets
// the same qualifier whether its manager is live in this process or loaded
// cold from disk after a restart. A raw workspace path must never be used as
// a qualifier — it contains "/" which breaks URL routing and changes with
// path normalization.
const managementSessionWorkspaceSep = "::"

func managementSessionID(session *Session, qualifier string) string {
	if qualifier == "" {
		return session.ID
	}
	return qualifier + managementSessionWorkspaceSep + session.ID
}

// managementQualifierForStore derives a stable, URL-safe, opaque qualifier
// from a workspace session store path.
//
// Live managers and cold disk stores for the same workspace share the same
// store file (<project>_ws_<8hex>.json where 8hex = hex(sha256(workspace)[:4]),
// see getOrCreateWorkspaceAgent), so deriving from the store path keeps ids
// stable across restarts and across the live/cold transition.
func managementQualifierForStore(storePath string) string {
	base := filepath.Base(storePath)
	if idx := strings.LastIndex(base, "_ws_"); idx >= 0 {
		rest := base[idx+len("_ws_"):]
		hexPart := strings.TrimSuffix(rest, ".json")
		if hexPart != "" {
			isHex := true
			for _, c := range hexPart {
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
					isHex = false
					break
				}
			}
			if isHex {
				return strings.ToLower(hexPart)
			}
		}
		return strings.TrimSuffix(base, ".json")
	}
	return strings.TrimSuffix(base, ".json")
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
			if ws == nil {
				continue
			}
			// Snapshot under ws.mu — the pool copy does not protect
			// per-workspace fields and unlocked reads trip the race detector.
			sm := ws.getSessions()
			if sm == nil || sm == e.sessions {
				continue
			}
			live[sm.StorePath()] = managementSessionManager{
				manager:   sm,
				workspace: path,
				qualifier: managementQualifierForStore(sm.StorePath()),
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
					qualifier: managementQualifierForStore(path),
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

// managementLiveStatus reports whether sessionKey is live. In multi-workspace
// mode the interactive state key is "<workspace>:<sessionKey>" (see
// interactiveKeyForSessionKey), so a bare-key lookup alone always reports
// live:false for workspace sessions. The workspace-aware candidate is checked
// first, then the bare key, then a suffix scan for cold disk entries whose
// workspace path is unknown in this process (mirrors workspaceFromLiveState).
func managementLiveStatus(sessionKey, workspace string, activeKeys map[string]string) (live bool, platform string) {
	if sessionKey == "" {
		return false, ""
	}
	if workspace != "" {
		if p, ok := activeKeys[workspace+":"+sessionKey]; ok {
			return true, p
		}
	}
	if p, ok := activeKeys[sessionKey]; ok {
		return true, p
	}
	suffix := ":" + sessionKey
	for k, p := range activeKeys {
		if strings.HasSuffix(k, suffix) {
			return true, p
		}
	}
	return false, ""
}

// findManagementSession resolves a management session id to the session and the
// manager that owns it.
//
//   - A qualified id (<qualifier>::<sessionID>) resolves only against the
//     manager carrying that qualifier. An unknown qualifier or an unknown id
//     under a known qualifier returns not-found — it must never fall back to
//     searching the bare id, otherwise a workspace request can silently
//     resolve to a same-named base session and leak its history.
//   - A bare id resolves only against the base manager. In multi-workspace
//     mode workspace sessions require their qualified id; in single-workspace
//     mode the base manager is the only manager anyway.
func (e *Engine) findManagementSession(id string) (managementSession, bool) {
	if id == "" {
		return managementSession{}, false
	}

	managers := e.managementSessionManagers()
	if len(managers) == 0 {
		return managementSession{}, false
	}

	if idx := strings.LastIndex(id, managementSessionWorkspaceSep); idx >= 0 {
		qualifier := id[:idx]
		raw := id[idx+len(managementSessionWorkspaceSep):]
		if qualifier == "" || raw == "" {
			return managementSession{}, false
		}
		for _, mm := range managers {
			if mm.qualifier != qualifier {
				continue
			}
			if s := mm.manager.FindByID(raw); s != nil {
				return managementSession{session: s, manager: mm.manager, workspace: mm.workspace, qualifier: mm.qualifier}, true
			}
			return managementSession{}, false
		}
		return managementSession{}, false
	}

	if s := managers[0].manager.FindByID(id); s != nil {
		mm := managers[0]
		return managementSession{session: s, manager: mm.manager, workspace: mm.workspace, qualifier: mm.qualifier}, true
	}
	return managementSession{}, false
}
