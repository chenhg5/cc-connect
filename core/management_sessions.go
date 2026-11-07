package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// managementSessionManager pairs a SessionManager with the workspace it serves.
type managementSessionManager struct {
	manager   *SessionManager
	workspace string // workspace path when known (live or persisted with workspace), else ""
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

// managementColdCacheTTL bounds how long persisted workspace stores are reused
// across management-API requests. Without it every list/count/detail call
// globs the sessions directory and re-parses every workspace store, which is
// per-project disk I/O on a hot path (the web console refreshes the project
// list on each load). Live managers are never cached — only the disk-only
// entries — and mutations via the management API invalidate the cache.
const managementColdCacheTTL = 5 * time.Second

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

// invalidateManagementColdCache drops the cached disk-only workspace entries.
// Call it after any management-API mutation that may change the set of
// persisted stores (session create, delete, switch).
func (e *Engine) invalidateManagementColdCache() {
	e.mgmtColdMu.Lock()
	defer e.mgmtColdMu.Unlock()
	e.mgmtColdExpires = time.Time{}
	e.mgmtColdManagers = nil
}

// managementColdManagers returns the disk-only workspace entries, reusing a
// short-TTL cache. The returned entries cover every non-empty persisted
// workspace store; callers dedupe against the current live managers by store
// path so a workspace that warmed up after the scan is not double-counted.
func (e *Engine) managementColdManagers() []managementSessionManager {
	e.mgmtColdMu.Lock()
	defer e.mgmtColdMu.Unlock()
	if time.Now().Before(e.mgmtColdExpires) && e.mgmtColdManagers != nil {
		return e.mgmtColdManagers
	}
	fresh := e.scanManagementColdStores()
	e.mgmtColdManagers = fresh
	e.mgmtColdExpires = time.Now().Add(managementColdCacheTTL)
	return fresh
}

// scanManagementColdStores lists persisted workspace stores without glob
// patterns (a project name containing `[`, `*` or `?` would otherwise make
// filepath.Glob return ErrBadPattern and silently hide every workspace).
func (e *Engine) scanManagementColdStores() []managementSessionManager {
	storePath := ""
	if e.sessions != nil {
		storePath = e.sessions.StorePath()
	}
	dir := filepath.Dir(storePath)
	if dir == "" || dir == "." {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("management: list workspace stores failed", "dir", dir, "error", err)
		return nil
	}
	prefix := e.name + "_ws_"
	var out []managementSessionManager
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		sm := NewSessionManager(path)
		if len(sm.AllSessions()) == 0 {
			continue
		}
		out = append(out, managementSessionManager{
			manager:   sm,
			workspace: sm.Workspace(),
			qualifier: managementQualifierForStore(path),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].manager.StorePath() < out[j].manager.StorePath()
	})
	return out
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

	managers := []managementSessionManager{base}
	for _, key := range sortedManagementKeys(live) {
		managers = append(managers, live[key])
	}
	for _, d := range e.managementColdManagers() {
		if _, ok := live[d.manager.StorePath()]; ok {
			continue
		}
		managers = append(managers, d)
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
// first, then the bare key.
//
// The trailing suffix scan exists for cold disk entries whose workspace path
// is unknown in this process (mirrors workspaceFromLiveState). It runs only
// for workspace sessions (qualifier != ""): a base session must never match a
// workspace-prefixed live key that merely ends with the same session key,
// otherwise a base session would flap to live:true depending on map iteration
// order.
func managementLiveStatus(sessionKey, workspace, qualifier string, activeKeys map[string]string) (live bool, platform string) {
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
	if qualifier == "" {
		return false, ""
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
