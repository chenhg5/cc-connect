package core

import (
	"path/filepath"
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

	// Workspace ids are qualified so they don't collide across managers.
	wsASession := wsA.AllSessions()[0]
	qualified := managementSessionID(wsASession, "/ws/a")
	if qualified == wsASession.ID {
		t.Fatalf("expected qualified id, got %q", qualified)
	}
	ms, ok := e.findManagementSession(qualified)
	if !ok || ms.manager != wsA || ms.workspace != "/ws/a" || ms.session.ID != wsASession.ID {
		t.Fatalf("findManagementSession(%q) = %+v, ok=%v", qualified, ms, ok)
	}

	// A bare id still resolves to the first matching manager.
	if _, ok := e.findManagementSession(wsASession.ID); !ok {
		t.Fatalf("bare id %q not resolved", wsASession.ID)
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

	qualified := filepath.Base(wsPath) + managementSessionWorkspaceSep + "s1"
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

func TestFindManagementSession_UnknownWorkspaceFallsBack(t *testing.T) {
	dir := t.TempDir()
	base := NewSessionManager(filepath.Join(dir, "base.json"))
	wsA := NewSessionManager(filepath.Join(dir, "a.json"))
	s := wsA.GetOrCreateActive("telegram:1:1")

	e := &Engine{sessions: base, multiWorkspace: true}
	e.workspacePool = newWorkspacePool(0)
	e.workspacePool.GetOrCreate("/ws/a").sessions = wsA

	// Unknown workspace prefix should fall back to searching the bare id.
	ms, ok := e.findManagementSession("/ws/nope" + managementSessionWorkspaceSep + s.ID)
	if !ok || ms.session.ID != s.ID {
		t.Fatalf("fallback lookup failed: %+v ok=%v", ms, ok)
	}

	if _, ok := e.findManagementSession("/ws/a" + managementSessionWorkspaceSep + "s999"); ok {
		t.Fatal("expected miss for unknown id in a known workspace")
	}
}
