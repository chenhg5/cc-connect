package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewSessionWithWorkDir(t *testing.T) {
	sm := NewSessionManager("")

	s := sm.NewSessionWithWorkDir("user1", "test-project", "/tmp/test-project")
	if s.WorkDir != "/tmp/test-project" {
		t.Errorf("expected WorkDir=/tmp/test-project, got %q", s.WorkDir)
	}
	if s.Name != "test-project" {
		t.Errorf("expected Name=test-project, got %q", s.Name)
	}
	if s.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Verify it's active
	active := sm.GetOrCreateActive("user1")
	if active.ID != s.ID {
		t.Errorf("expected active session ID=%s, got %s", s.ID, active.ID)
	}
}

func TestNewSessionWithoutWorkDir(t *testing.T) {
	sm := NewSessionManager("")

	s := sm.NewSession("user1", "regular")
	if s.WorkDir != "" {
		t.Errorf("expected empty WorkDir, got %q", s.WorkDir)
	}
}

func TestDeleteSession(t *testing.T) {
	sm := NewSessionManager("")

	s1 := sm.NewSession("user1", "first")
	s2 := sm.NewSession("user1", "second")
	s3 := sm.NewSession("user1", "third")

	// s3 should be active (last created)
	activeID := sm.ActiveSessionID("user1")
	if activeID != s3.ID {
		t.Errorf("expected active=%s, got %s", s3.ID, activeID)
	}

	// Delete non-active session by ID
	deleted, err := sm.DeleteSession("user1", s1.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted.ID != s1.ID {
		t.Errorf("expected deleted ID=%s, got %s", s1.ID, deleted.ID)
	}

	// s3 should still be active
	activeID = sm.ActiveSessionID("user1")
	if activeID != s3.ID {
		t.Errorf("expected active=%s after deleting non-active, got %s", s3.ID, activeID)
	}

	// Should have 2 sessions left
	sessions := sm.ListSessions("user1")
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}

	// Delete active session by name
	deleted, err = sm.DeleteSession("user1", "third")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted.ID != s3.ID {
		t.Errorf("expected deleted ID=%s, got %s", s3.ID, deleted.ID)
	}

	// s2 should now be active (last remaining)
	activeID = sm.ActiveSessionID("user1")
	if activeID != s2.ID {
		t.Errorf("expected active=%s after deleting active, got %s", s2.ID, activeID)
	}

	// Delete last session
	_, err = sm.DeleteSession("user1", s2.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No active session
	activeID = sm.ActiveSessionID("user1")
	if activeID != "" {
		t.Errorf("expected empty active after deleting all, got %s", activeID)
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	sm := NewSessionManager("")
	sm.NewSession("user1", "test")

	_, err := sm.DeleteSession("user1", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestDeleteSessionByName(t *testing.T) {
	sm := NewSessionManager("")
	s := sm.NewSessionWithWorkDir("user1", "my-project", "/tmp/project")

	deleted, err := sm.DeleteSession("user1", "my-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted.WorkDir != "/tmp/project" {
		t.Errorf("expected WorkDir=/tmp/project, got %q", deleted.WorkDir)
	}
	if deleted.ID != s.ID {
		t.Errorf("expected ID=%s, got %s", s.ID, deleted.ID)
	}
}

func TestSessionPersistenceWithWorkDir(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "sessions.json")

	// Create and save
	sm1 := NewSessionManager(storePath)
	sm1.NewSessionWithWorkDir("user1", "proj", "/home/user/project")
	sm1.NewSession("user1", "plain")

	// Reload
	sm2 := NewSessionManager(storePath)
	sessions := sm2.ListSessions("user1")
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions after reload, got %d", len(sessions))
	}

	var foundWorkDir bool
	for _, s := range sessions {
		if s.Name == "proj" && s.WorkDir == "/home/user/project" {
			foundWorkDir = true
		}
	}
	if !foundWorkDir {
		t.Error("WorkDir not persisted correctly")
	}
}

func TestSessionPersistenceDeleteAndReload(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "sessions.json")

	sm1 := NewSessionManager(storePath)
	sm1.NewSession("user1", "keep")
	sm1.NewSession("user1", "remove")

	_, err := sm1.DeleteSession("user1", "remove")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify
	sm2 := NewSessionManager(storePath)
	sessions := sm2.ListSessions("user1")
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after reload, got %d", len(sessions))
	}
	if sessions[0].Name != "keep" {
		t.Errorf("expected session name=keep, got %q", sessions[0].Name)
	}
}

func TestWorkDirDetection(t *testing.T) {
	// Create a temp directory to test path detection
	tmpDir := t.TempDir()

	tests := []struct {
		input     string
		isPath    bool
		dirExists bool
	}{
		{"/tmp", true, true},
		{tmpDir, true, true},
		{"/nonexistent-dir-xyz", true, false},
		{"my-session", false, false},
		{".", true, true},
		{"~", true, true},
	}

	for _, tc := range tests {
		arg := tc.input
		isPath := false
		if len(arg) > 0 && (arg[0] == '/' || arg[0] == '.' || arg[0] == '~') {
			isPath = true
		}
		if isPath != tc.isPath {
			t.Errorf("input %q: expected isPath=%v, got %v", tc.input, tc.isPath, isPath)
		}

		if isPath && tc.dirExists {
			expanded := arg
			if expanded[0] == '~' {
				home, _ := os.UserHomeDir()
				expanded = filepath.Join(home, expanded[1:])
			}
			absPath, err := filepath.Abs(expanded)
			if err != nil {
				t.Errorf("input %q: filepath.Abs error: %v", tc.input, err)
				continue
			}
			info, err := os.Stat(absPath)
			if err != nil || !info.IsDir() {
				t.Errorf("input %q: expected existing directory at %s", tc.input, absPath)
			}
		}
	}
}
