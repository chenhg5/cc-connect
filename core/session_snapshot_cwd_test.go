package core

import (
	"path/filepath"
	"testing"
)

// TestSessionManager_PersistsAgentCwd is the regression for the bug where
// Save()'s hand-written snapshot deep-copy omitted AgentCwd, so a captured cwd
// never survived a restart even though it was set on the live Session. Unlike a
// direct json.Marshal(Session) test (which passes regardless), this exercises
// the snapshot path Save actually uses.
func TestSessionManager_PersistsAgentCwd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	const cwd = "/workspace/.sessions/abc123/owner-repo"

	sm := NewSessionManager(path)
	s := sm.GetOrCreateActive("user1")
	s.SetAgentSessionID("sess-1", "claudecode")
	s.SetAgentCwd(cwd)
	sm.Save()

	// Reload from disk with a fresh manager — mirrors a daemon restart.
	sm2 := NewSessionManager(path)
	s2 := sm2.GetOrCreateActive("user1")
	if got := s2.GetAgentSessionID(); got != "sess-1" {
		t.Fatalf("agent_session_id not persisted across save/reload: got %q", got)
	}
	if got := s2.GetAgentCwd(); got != cwd {
		t.Fatalf("agent_cwd not persisted across save/reload: got %q, want %q", got, cwd)
	}
}
