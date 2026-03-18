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

	// Resolve the expected path through EvalSymlinks so that the test works
	// on macOS where /var is a symlink to /private/var.
	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing slash", realDir + "/", resolvedRealDir},
		{"double slash", filepath.Join(tmp, "real-project") + "//", resolvedRealDir},
		{"dot segment", filepath.Join(tmp, ".", "real-project"), resolvedRealDir},
		{"dotdot segment", filepath.Join(tmp, "real-project", "subdir", ".."), resolvedRealDir},
		{"symlink resolved", symlink, resolvedRealDir},
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

func TestNormalizeBeforePoolProducesSameKey(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pool := newWorkspacePool(15 * time.Minute)

	// Callers normalize before pool access (as resolveWorkspace does)
	ws1 := pool.GetOrCreate(normalizeWorkspacePath(realDir + "/"))
	ws2 := pool.GetOrCreate(normalizeWorkspacePath(realDir))

	if ws1 != ws2 {
		t.Error("normalized trailing slash produced a different workspace state")
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

func TestInteractiveKeyForSessionKey_NormalizesWorkspace(t *testing.T) {
	tmp := t.TempDir()
	wsDir := filepath.Join(tmp, "ws1")
	if err := os.Mkdir(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.multiWorkspace = true
	e.baseDir = tmp
	e.workspaceBindings = NewWorkspaceBindingManager(filepath.Join(tmp, "bindings.json"))

	channelID := "chan123"
	sessionKey := "test:" + channelID

	// Bind with trailing slash (unnormalized)
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir+"/")

	key := e.interactiveKeyForSessionKey(sessionKey)
	expected := normalizeWorkspacePath(wsDir) + ":" + sessionKey

	if key != expected {
		t.Errorf("interactiveKeyForSessionKey should normalize workspace path\ngot:  %s\nwant: %s", key, expected)
	}

	// Also verify it matches what we'd get with the clean path
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)
	key2 := e.interactiveKeyForSessionKey(sessionKey)
	if key != key2 {
		t.Errorf("keys should be identical regardless of trailing slash\nslash:   %s\nclean:   %s", key, key2)
	}
}
