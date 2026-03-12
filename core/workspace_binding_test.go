package core

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceBindingManager_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", "C123", "my-channel", "/home/user/workspace/my-channel")

	b := mgr.Lookup("project:claude", "C123")
	if b == nil {
		t.Fatal("expected binding, got nil")
	}
	if b.ChannelName != "my-channel" {
		t.Errorf("expected channel name 'my-channel', got %q", b.ChannelName)
	}
	if b.Workspace != "/home/user/workspace/my-channel" {
		t.Errorf("expected workspace path, got %q", b.Workspace)
	}

	// Reload from disk
	mgr2 := NewWorkspaceBindingManager(storePath)
	b2 := mgr2.Lookup("project:claude", "C123")
	if b2 == nil {
		t.Fatal("expected binding after reload, got nil")
	}
	if b2.Workspace != "/home/user/workspace/my-channel" {
		t.Errorf("expected workspace path after reload, got %q", b2.Workspace)
	}
}

func TestWorkspaceBindingManager_Unbind(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", "C123", "chan", "/path")
	mgr.Unbind("project:claude", "C123")

	if b := mgr.Lookup("project:claude", "C123"); b != nil {
		t.Error("expected nil after unbind")
	}
}

func TestWorkspaceBindingManager_ListByProject(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))
	mgr.Bind("project:claude", "C1", "chan1", "/path1")
	mgr.Bind("project:claude", "C2", "chan2", "/path2")
	mgr.Bind("project:other", "C3", "chan3", "/path3")

	list := mgr.ListByProject("project:claude")
	if len(list) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(list))
	}
}
