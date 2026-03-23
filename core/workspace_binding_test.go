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

func TestWorkspaceBindingManager_LookupEffective(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))

	mgr.Bind(sharedWorkspaceBindingsKey, "C1", "shared-chan", "/shared")
	mgr.Bind("project:claude", "C1", "local-chan", "/local")

	if b, key := mgr.LookupEffective("project:claude", "C1"); b == nil || key != "project:claude" || b.Workspace != "/local" {
		t.Fatalf("expected local override, got binding=%v key=%q", b, key)
	}

	if b, key := mgr.LookupEffective("project:other", "C1"); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared fallback, got binding=%v key=%q", b, key)
	}

	if b, key := mgr.LookupEffective("project:none", "missing"); b != nil || key != "" {
		t.Fatalf("expected nil binding, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_LoadSharedFromDisk(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind(sharedWorkspaceBindingsKey, "C1", "shared-chan", "/shared")

	reloaded := NewWorkspaceBindingManager(storePath)
	if b, key := reloaded.LookupEffective("project:other", "C1"); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared binding after reload, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_RefreshesExternalChanges(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgrA := NewWorkspaceBindingManager(storePath)
	mgrB := NewWorkspaceBindingManager(storePath)

	mgrA.Bind(sharedWorkspaceBindingsKey, "C1", "shared-chan", "/shared")

	if b, key := mgrB.LookupEffective("project:other", "C1"); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared binding from external update, got binding=%v key=%q", b, key)
	}

	mgrB.Bind("project:other", "C1", "local-chan", "/local")

	if b, key := mgrA.LookupEffective("project:other", "C1"); b == nil || key != "project:other" || b.Workspace != "/local" {
		t.Fatalf("expected local override from external update, got binding=%v key=%q", b, key)
	}

	mgrA.Unbind(sharedWorkspaceBindingsKey, "C1")

	if b, key := mgrB.LookupEffective("project:none", "C1"); b != nil || key != "" {
		t.Fatalf("expected shared binding removal to propagate, got binding=%v key=%q", b, key)
	}
}
