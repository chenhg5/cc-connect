package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspacePool_GetOrCreate(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)

	state1 := pool.GetOrCreate("/workspace/a")
	state2 := pool.GetOrCreate("/workspace/a")
	state3 := pool.GetOrCreate("/workspace/b")

	if state1 != state2 {
		t.Error("expected same state for same workspace")
	}
	if state1 == state3 {
		t.Error("expected different state for different workspace")
	}
}

func TestWorkspacePool_Touch(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)
	state := pool.GetOrCreate("/workspace/a")

	before := state.LastActivity()
	time.Sleep(10 * time.Millisecond)
	state.Touch()
	after := state.LastActivity()

	if !after.After(before) {
		t.Error("expected lastActivity to advance after Touch()")
	}
}

func TestWorkspacePool_ReapIdle(t *testing.T) {
	pool := newWorkspacePool(50 * time.Millisecond)
	pool.GetOrCreate("/workspace/a")

	time.Sleep(100 * time.Millisecond)
	reaped := pool.ReapIdle()

	if len(reaped) != 1 || reaped[0] != "/workspace/a" {
		t.Errorf("expected [/workspace/a] reaped, got %v", reaped)
	}

	if s := pool.Get("/workspace/a"); s != nil {
		t.Error("expected workspace removed after reap")
	}
}

func TestNormalizeWorkspacePath(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tmp, "link-project")
	if err := os.Symlink(realDir, symlink); err != nil {
		t.Skip("symlinks not supported")
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing slash", realDir + "/", realDir},
		{"double slash", filepath.Join(tmp, "real-project") + "//", realDir},
		{"dot segment", filepath.Join(tmp, ".", "real-project"), realDir},
		{"dotdot segment", filepath.Join(tmp, "real-project", "subdir", ".."), realDir},
		{"symlink resolved", symlink, realDir},
		{"nonexistent uses Clean only", "/nonexistent/path/./foo/../bar", "/nonexistent/path/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWorkspacePath(tt.input)
			if got != tt.want {
				t.Errorf("normalizeWorkspacePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWorkspacePoolNormalizesKeys(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pool := newWorkspacePool(15 * time.Minute)

	// Access with trailing slash
	ws1 := pool.GetOrCreate(realDir + "/")
	// Access without trailing slash
	ws2 := pool.GetOrCreate(realDir)

	if ws1 != ws2 {
		t.Error("trailing slash created a different workspace state")
	}
}

func TestWorkspacePool_ReapIdle_KeepsActive(t *testing.T) {
	pool := newWorkspacePool(200 * time.Millisecond)
	state := pool.GetOrCreate("/workspace/active")

	time.Sleep(100 * time.Millisecond)
	state.Touch() // Keep it alive

	reaped := pool.ReapIdle()
	if len(reaped) != 0 {
		t.Errorf("expected no reaping for active workspace, got %v", reaped)
	}

	if s := pool.Get("/workspace/active"); s == nil {
		t.Error("expected active workspace to still exist")
	}
}
