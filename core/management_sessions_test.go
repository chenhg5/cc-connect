package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementSessions_SingleWorkspaceUnchanged(t *testing.T) {
	base := NewSessionManager(filepath.Join(t.TempDir(), "base.json"))
	s := base.GetOrCreateActive("telegram:1:1")
	e := &Engine{sessions: base}

	if got := e.managementSessionCount(); got != 1 {
		t.Fatalf("managementSessionCount() = %d, want 1", got)
	}
	if id := managementSessionID(s, ""); id != s.ID {
		t.Fatalf("managementSessionID() = %q, want bare id %q", id, s.ID)
	}
	ms, ok := e.findManagementSession(s.ID)
	if !ok || ms.manager != base {
		t.Fatalf("findManagementSession(%q) = %+v, ok=%v", s.ID, ms, ok)
	}
}

func TestManagementSessions_MultiWorkspaceAggregatesAndQualifiesIDs(t *testing.T) {
	dir := t.TempDir()
	base := NewSessionManager(filepath.Join(dir, "base.json"))
	wsA := NewSessionManager(filepath.Join(dir, "a.json"))
	wsB := NewSessionManager(filepath.Join(dir, "b.json"))

	base.GetOrCreateActive("telegram:1:1")
	wsA.GetOrCreateActive("telegram:2:2")
	wsA.GetOrCreateActive("telegram:3:3")
	wsB.GetOrCreateActive("telegram:4:4")

	e := &Engine{sessions: base, multiWorkspace: true}
	e.workspacePool = newWorkspacePool(0)
	e.workspacePool.GetOrCreate("/ws/a").sessions = wsA
	e.workspacePool.GetOrCreate("/ws/b").sessions = wsB

	// Base + both workspaces are counted (the old code only saw the base).
	if got := e.managementSessionCount(); got != 4 {
		t.Fatalf("managementSessionCount() = %d, want 4", got)
	}

	// Workspace ids are qualified with a stable opaque qualifier derived
	// from the store path — never the raw workspace path, which contains
	// "/" and breaks URL routing.
	managers := e.managementSessionManagers()
	var qualA, wsAPath string
	for _, mm := range managers {
		if mm.manager == wsA {
			qualA = mm.qualifier
			wsAPath = mm.workspace
		}
	}
	if qualA == "" {
		t.Fatal("workspace A has empty qualifier")
	}
	if strings.Contains(qualA, "/") {
		t.Fatalf("qualifier %q must be URL-safe (no slashes)", qualA)
	}
	wsASession := wsA.AllSessions()[0]
	qualified := managementSessionID(wsASession, qualA)
	if qualified == wsASession.ID {
		t.Fatalf("expected qualified id, got %q", qualified)
	}
	ms, ok := e.findManagementSession(qualified)
	if !ok || ms.manager != wsA || ms.workspace != wsAPath || ms.session.ID != wsASession.ID {
		t.Fatalf("findManagementSession(%q) = %+v, ok=%v", qualified, ms, ok)
	}

	// A bare workspace session id must NOT resolve in multi-workspace mode:
	// every manager starts at s1, so a bare lookup could return the base
	// session's history instead of the workspace session.
	if _, ok := e.findManagementSession(wsASession.ID); ok {
		// Only acceptable when the bare id happens to be the base session.
		baseIDs := map[string]bool{}
		for _, s := range base.AllSessions() {
			baseIDs[s.ID] = true
		}
		if !baseIDs[wsASession.ID] {
			t.Fatalf("bare workspace id %q must not resolve in multi-workspace mode", wsASession.ID)
		}
	}

	// The base session keeps its bare id and resolves to the base manager.
	baseSession := base.AllSessions()[0]
	if id := managementSessionID(baseSession, ""); id != baseSession.ID {
		t.Fatalf("base id = %q, want %q", id, baseSession.ID)
	}
	if ms, ok := e.findManagementSession(baseSession.ID); !ok || ms.manager != base {
		t.Fatalf("base session resolved to %+v, ok=%v", ms, ok)
	}
}

func TestManagementSessions_LoadsPersistedWorkspaceStores(t *testing.T) {
	dir := t.TempDir()
	base := NewSessionManager(filepath.Join(dir, "proj.json"))
	e := &Engine{sessions: base, multiWorkspace: true, name: "proj"}

	// A workspace store exists on disk but is not live in this process (the
	// workspace pool is empty right after a restart).
	wsPath := filepath.Join(dir, "proj_ws_deadbeef.json")
	ws := NewSessionManager(wsPath)
	ws.GetOrCreateActive("telegram:9:9")
	ws.Save()

	if got := e.managementSessionCount(); got != 1 {
		t.Fatalf("managementSessionCount() = %d, want 1 (persisted workspace session)", got)
	}

	// The qualifier is the opaque hex suffix, stable across cold/live.
	qualified := "deadbeef" + managementSessionWorkspaceSep + "s1"
	ms, ok := e.findManagementSession(qualified)
	if !ok || ms.session.ID != "s1" {
		t.Fatalf("findManagementSession(%q) = %+v, ok=%v", qualified, ms, ok)
	}

	// Empty workspace stores are ignored.
	NewSessionManager(filepath.Join(dir, "proj_ws_empty.json")).Save()
	if got := e.managementSessionCount(); got != 1 {
		t.Fatalf("managementSessionCount() = %d, want 1 after empty store", got)
	}
}

func TestManagementQualifier_StableAcrossLiveAndCold(t *testing.T) {
	dir := t.TempDir()
	// Same store file, once as a live manager and once as a cold disk load:
	// the qualifier must be identical so ids survive restarts.
	wsPath := filepath.Join(dir, "proj_ws_ab12cd34.json")
	live := NewSessionManager(wsPath)
	live.GetOrCreateActive("telegram:1:1")
	live.Save()

	e := &Engine{
		sessions:       NewSessionManager(filepath.Join(dir, "proj.json")),
		multiWorkspace: true,
		name:           "proj",
	}
	e.workspacePool = newWorkspacePool(0)
	e.workspacePool.GetOrCreate("/some/workspace").sessions = live

	var liveQual string
	for _, mm := range e.managementSessionManagers() {
		if mm.manager == live {
			liveQual = mm.qualifier
		}
	}
	if liveQual == "" {
		t.Fatal("live manager has empty qualifier")
	}

	// Drop the live manager: the cold disk load must reuse the qualifier.
	e.workspacePool = newWorkspacePool(0)
	var coldQual string
	for _, mm := range e.managementSessionManagers() {
		if mm.manager != e.sessions {
			coldQual = mm.qualifier
		}
	}
	if coldQual != liveQual {
		t.Fatalf("cold qualifier %q != live qualifier %q", coldQual, liveQual)
	}
	// The qualified id resolves in both states.
	if _, ok := e.findManagementSession(coldQual + managementSessionWorkspaceSep + "s1"); !ok {
		t.Fatalf("cold qualified id %q not resolved", coldQual+"::s1")
	}
}

func TestFindManagementSession_UnknownQualifierIsNotFound(t *testing.T) {
	dir := t.TempDir()
	base := NewSessionManager(filepath.Join(dir, "base.json"))
	baseSess := base.GetOrCreateActive("telegram:1:1")
	wsA := NewSessionManager(filepath.Join(dir, "a.json"))
	wsSess := wsA.GetOrCreateActive("telegram:2:2")

	e := &Engine{sessions: base, multiWorkspace: true}
	e.workspacePool = newWorkspacePool(0)
	e.workspacePool.GetOrCreate("/ws/a").sessions = wsA

	// Unknown qualifier must 404 — falling back to the bare id would resolve
	// a workspace request to the base session's history.
	if _, ok := e.findManagementSession("/ws/nope" + managementSessionWorkspaceSep + wsSess.ID); ok {
		t.Fatal("unknown qualifier must not resolve")
	}
	if _, ok := e.findManagementSession("deadbeef" + managementSessionWorkspaceSep + baseSess.ID); ok {
		t.Fatal("wrong qualifier must not resolve to base session")
	}
	// Known qualifier but unknown session id must 404 without searching others.
	var qualA string
	for _, mm := range e.managementSessionManagers() {
		if mm.manager == wsA {
			qualA = mm.qualifier
		}
	}
	if _, ok := e.findManagementSession(qualA + managementSessionWorkspaceSep + "s999"); ok {
		t.Fatal("expected miss for unknown id in a known workspace")
	}
	// Bare base id still resolves to base.
	if ms, ok := e.findManagementSession(baseSess.ID); !ok || ms.manager != base {
		t.Fatalf("bare base id resolved to %+v, ok=%v", ms, ok)
	}
}

func TestManagementLiveStatus_WorkspaceAwareKey(t *testing.T) {
	activeKeys := map[string]string{
		"/ws/a:telegram:2:2": "telegram",
		"telegram:1:1":       "telegram",
	}
	// Workspace session is live only via its prefixed key; the old bare-key
	// lookup reported live:false.
	if live, _ := managementLiveStatus("telegram:2:2", "/ws/a", activeKeys); !live {
		t.Fatal("workspace session should be live via prefixed key")
	}
	if live, _ := managementLiveStatus("telegram:2:2", "", activeKeys); !live {
		t.Fatal("suffix scan should recover live state for cold entries")
	}
	if live, _ := managementLiveStatus("telegram:9:9", "/ws/a", activeKeys); live {
		t.Fatal("unknown session must not be live")
	}
	if live, _ := managementLiveStatus("telegram:1:1", "", activeKeys); !live {
		t.Fatal("bare session should be live via bare key")
	}
}

// TestManagementSessionIsolation_ListDetailDeleteSwitch is the end-to-end
// regression test for the multi-workspace history leak: with a base session
// s1 (holding BASE-SECRET) and a workspace session s1 (holding WS-SECRET),
// every management path — list, detail, switch, delete — must stay on the
// owning manager and never cross-resolve.
func TestManagementSessionIsolation_ListDetailDeleteSwitch(t *testing.T) {
	dir := t.TempDir()
	base := NewSessionManager(filepath.Join(dir, "base.json"))
	wsStore := NewSessionManager(filepath.Join(dir, "ws.json"))

	baseSess := base.GetOrCreateActive("telegram:1:1")
	baseSess.AddHistory("user", "BASE-SECRET")
	wsSess := wsStore.GetOrCreateActive("telegram:2:2")
	wsSess.AddHistory("user", "WS-SECRET")

	e := &Engine{sessions: base, multiWorkspace: true}
	e.workspacePool = newWorkspacePool(0)
	e.workspacePool.GetOrCreate("/ws/a").sessions = wsStore

	managers := e.managementSessionManagers()
	var qualA string
	for _, mm := range managers {
		if mm.manager == wsStore {
			qualA = mm.qualifier
		}
	}
	if qualA == "" {
		t.Fatal("workspace qualifier is empty")
	}
	wsQualified := managementSessionID(wsSess, qualA)

	// Detail via qualified id returns the workspace history, not base's.
	ms, ok := e.findManagementSession(wsQualified)
	if !ok {
		t.Fatalf("qualified id %q not resolved", wsQualified)
	}
	hist := ms.session.GetHistory(10)
	if len(hist) != 1 || hist[0].Content != "WS-SECRET" {
		t.Fatalf("workspace detail returned wrong history: %+v", hist)
	}
	// Detail via bare base id returns the base history.
	msBase, ok := e.findManagementSession(baseSess.ID)
	if !ok || msBase.manager != base {
		t.Fatalf("bare base id resolved to %+v, ok=%v", msBase, ok)
	}
	if hist := msBase.session.GetHistory(10); len(hist) != 1 || hist[0].Content != "BASE-SECRET" {
		t.Fatalf("base detail returned wrong history: %+v", hist)
	}
	// Unknown qualifier never resolves — even when the bare id exists on base.
	if _, ok := e.findManagementSession("nope" + managementSessionWorkspaceSep + wsSess.ID); ok {
		t.Fatal("unknown qualifier must not fall back to bare id")
	}

	// Switch via qualified id operates on the owning manager only.
	switched, err := ms.manager.SwitchSession("telegram:2:2", ms.session.ID)
	if err != nil || switched.ID != wsSess.ID {
		t.Fatalf("workspace switch failed: %+v err=%v", switched, err)
	}
	if got := base.ActiveSessionID("telegram:2:2"); got != "" {
		t.Fatalf("workspace switch leaked into base manager: %q", got)
	}

	// Delete via qualified id removes only the workspace session.
	if !ms.manager.DeleteByID(ms.session.ID) {
		t.Fatal("workspace delete failed")
	}
	if base.FindByID(baseSess.ID) == nil {
		t.Fatal("workspace delete removed the base session")
	}
	if _, ok := e.findManagementSession(wsQualified); ok {
		t.Fatal("deleted workspace session still resolves")
	}
	if _, ok := e.findManagementSession(baseSess.ID); !ok {
		t.Fatal("base session should still resolve after workspace delete")
	}
}
