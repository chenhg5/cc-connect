package core

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkspaceBindingManager_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")
	channelKey := workspaceChannelKey("slack", "C123")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", channelKey, "my-channel", "/home/user/workspace/my-channel")

	b := mgr.Lookup("project:claude", channelKey)
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
	b2 := mgr2.Lookup("project:claude", channelKey)
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
	channelKey := workspaceChannelKey("slack", "C123")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", channelKey, "chan", "/path")
	mgr.Unbind("project:claude", channelKey)

	if b := mgr.Lookup("project:claude", channelKey); b != nil {
		t.Error("expected nil after unbind")
	}
}

func TestWorkspaceBindingManager_ListByProject(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))
	mgr.Bind("project:claude", workspaceChannelKey("slack", "C1"), "chan1", "/path1")
	mgr.Bind("project:claude", workspaceChannelKey("slack", "C2"), "chan2", "/path2")
	mgr.Bind("project:other", workspaceChannelKey("slack", "C3"), "chan3", "/path3")

	list := mgr.ListByProject("project:claude")
	if len(list) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(list))
	}
}

func TestWorkspaceBindingManager_LookupEffective(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))
	channelKey := workspaceChannelKey("slack", "C1")

	mgr.Bind(sharedWorkspaceBindingsKey, channelKey, "shared-chan", "/shared")
	mgr.Bind("project:claude", channelKey, "local-chan", "/local")

	if b, key := mgr.LookupEffective("project:claude", channelKey); b == nil || key != "project:claude" || b.Workspace != "/local" {
		t.Fatalf("expected local override, got binding=%v key=%q", b, key)
	}

	if b, key := mgr.LookupEffective("project:other", channelKey); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared fallback, got binding=%v key=%q", b, key)
	}

	if b, key := mgr.LookupEffective("project:none", "missing"); b != nil || key != "" {
		t.Fatalf("expected nil binding, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_LoadSharedFromDisk(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")
	channelKey := workspaceChannelKey("slack", "C1")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind(sharedWorkspaceBindingsKey, channelKey, "shared-chan", "/shared")

	reloaded := NewWorkspaceBindingManager(storePath)
	if b, key := reloaded.LookupEffective("project:other", channelKey); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared binding after reload, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_RefreshesExternalChanges(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")
	channelKey := workspaceChannelKey("slack", "C1")

	mgrA := NewWorkspaceBindingManager(storePath)
	mgrB := NewWorkspaceBindingManager(storePath)

	mgrA.Bind(sharedWorkspaceBindingsKey, channelKey, "shared-chan", "/shared")

	if b, key := mgrB.LookupEffective("project:other", channelKey); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected shared binding from external update, got binding=%v key=%q", b, key)
	}

	mgrB.Bind("project:other", channelKey, "local-chan", "/local")

	if b, key := mgrA.LookupEffective("project:other", channelKey); b == nil || key != "project:other" || b.Workspace != "/local" {
		t.Fatalf("expected local override from external update, got binding=%v key=%q", b, key)
	}

	mgrA.Unbind(sharedWorkspaceBindingsKey, channelKey)

	if b, key := mgrB.LookupEffective("project:none", channelKey); b != nil || key != "" {
		t.Fatalf("expected shared binding removal to propagate, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_LegacyFallbackForScopedLookup(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))

	mgr.Bind(sharedWorkspaceBindingsKey, "C1", "legacy-chan", "/shared")

	if b, key := mgr.LookupEffective("project:other", workspaceChannelKey("slack", "C1")); b == nil || key != sharedWorkspaceBindingsKey || b.Workspace != "/shared" {
		t.Fatalf("expected legacy shared binding to be found by scoped lookup, got binding=%v key=%q", b, key)
	}
}

func TestWorkspaceBindingManager_UnbindScopedRemovesLegacyBinding(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))

	mgr.Bind("project:claude", "C1", "legacy-chan", "/shared")
	mgr.Unbind("project:claude", workspaceChannelKey("slack", "C1"))

	if b := mgr.Lookup("project:claude", workspaceChannelKey("slack", "C1")); b != nil {
		t.Fatalf("expected scoped unbind to remove legacy binding, got %+v", b)
	}
	if b := mgr.Lookup("project:claude", "C1"); b != nil {
		t.Fatalf("expected legacy binding to be removed, got %+v", b)
	}
}

func TestWorkspaceBindingManager_InheritChannelKeyPreservesDefault(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")
	mgr := NewWorkspaceBindingManager(storePath)

	projectKey := "project:claude"
	oldKey := workspaceChannelKey("feishu", "oc_chat")
	newKey := workspaceChannelKey("feishu", "oc_chat:topic:om_root")
	mgr.Bind(projectKey, oldKey, "topic-group", "/workspace/a")

	if !mgr.MigrateChannelKey(projectKey, oldKey, newKey) {
		t.Fatal("expected topic binding to inherit the chat default")
	}
	if b := mgr.Lookup(projectKey, oldKey); b == nil || b.Workspace != "/workspace/a" {
		t.Fatalf("expected chat default binding to be preserved, got %+v", b)
	}
	if b := mgr.Lookup(projectKey, newKey); b == nil || b.Workspace != "/workspace/a" {
		t.Fatalf("expected inherited topic binding, got %+v", b)
	}

	reloaded := NewWorkspaceBindingManager(storePath)
	if b := reloaded.Lookup(projectKey, newKey); b == nil || b.Workspace != "/workspace/a" {
		t.Fatalf("expected inherited binding after reload, got %+v", b)
	}
	if b := reloaded.Lookup(projectKey, oldKey); b == nil || b.Workspace != "/workspace/a" {
		t.Fatalf("expected chat default after reload, got %+v", b)
	}
}

func TestWorkspaceBindingManager_InheritChannelKeyDoesNotOverwriteDestination(t *testing.T) {
	mgr := NewWorkspaceBindingManager(filepath.Join(t.TempDir(), "bindings.json"))
	projectKey := "project:claude"
	oldKey := workspaceChannelKey("feishu", "oc_chat")
	newKey := workspaceChannelKey("feishu", "oc_chat:topic:om_root")
	mgr.Bind(projectKey, oldKey, "topic-group", "/workspace/legacy")
	mgr.Bind(projectKey, newKey, "topic", "/workspace/current")

	if mgr.MigrateChannelKey(projectKey, oldKey, newKey) {
		t.Fatal("inheritance must not overwrite an existing topic binding")
	}
	if b := mgr.Lookup(projectKey, newKey); b == nil || b.Workspace != "/workspace/current" {
		t.Fatalf("destination binding changed unexpectedly: %+v", b)
	}
	if b := mgr.Lookup(projectKey, oldKey); b == nil || b.Workspace != "/workspace/legacy" {
		t.Fatalf("chat default binding should remain: %+v", b)
	}
}

func TestWorkspaceBindingManager_InheritChannelKeyConcurrentTopics(t *testing.T) {
	mgr := NewWorkspaceBindingManager(filepath.Join(t.TempDir(), "bindings.json"))
	projectKey := "project:claude"
	oldKey := workspaceChannelKey("feishu", "oc_chat")
	targets := []string{
		workspaceChannelKey("feishu", "oc_chat:topic:om_root_a"),
		workspaceChannelKey("feishu", "oc_chat:topic:om_root_b"),
	}
	mgr.Bind(projectKey, oldKey, "topic-group", "/workspace/a")

	results := make(chan bool, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(channelKey string) {
			defer wg.Done()
			results <- mgr.MigrateChannelKey(projectKey, oldKey, channelKey)
		}(target)
	}
	wg.Wait()
	close(results)

	inherited := 0
	for ok := range results {
		if ok {
			inherited++
		}
	}
	if inherited != len(targets) {
		t.Fatalf("expected all topics to inherit the default, got %d of %d", inherited, len(targets))
	}
	for _, target := range targets {
		if b := mgr.Lookup(projectKey, target); b == nil || b.Workspace != "/workspace/a" {
			t.Fatalf("topic %q did not inherit the default: %+v", target, b)
		}
	}
}
