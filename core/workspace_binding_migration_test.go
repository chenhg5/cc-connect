package core

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceBindingManagerMigrateChannelKeyPreservesDefault(t *testing.T) {
	mgr := NewWorkspaceBindingManager(filepath.Join(t.TempDir(), "bindings.json"))
	projectKey := "project:codex"
	oldKey := workspaceChannelKey("feishu", "oc_chat")
	newKey := workspaceChannelKey("feishu", "oc_chat:topic:om_root")
	mgr.Bind(projectKey, oldKey, "group", "/workspace/default")

	if !mgr.MigrateChannelKey(projectKey, oldKey, newKey) {
		t.Fatal("expected topic binding to inherit the chat default")
	}
	if got := mgr.Lookup(projectKey, oldKey); got == nil || got.Workspace != "/workspace/default" {
		t.Fatalf("chat default was not preserved: %+v", got)
	}
	if got := mgr.Lookup(projectKey, newKey); got == nil || got.Workspace != "/workspace/default" {
		t.Fatalf("topic did not inherit default: %+v", got)
	}
}

func TestWorkspaceBindingManagerMigrateChannelKeyDoesNotOverwriteTopic(t *testing.T) {
	mgr := NewWorkspaceBindingManager(filepath.Join(t.TempDir(), "bindings.json"))
	projectKey := "project:codex"
	oldKey := workspaceChannelKey("feishu", "oc_chat")
	newKey := workspaceChannelKey("feishu", "oc_chat:topic:om_root")
	mgr.Bind(projectKey, oldKey, "group", "/workspace/default")
	mgr.Bind(projectKey, newKey, "topic", "/workspace/topic")

	if mgr.MigrateChannelKey(projectKey, oldKey, newKey) {
		t.Fatal("existing topic binding must not be overwritten")
	}
	if got := mgr.Lookup(projectKey, newKey); got == nil || got.Workspace != "/workspace/topic" {
		t.Fatalf("topic binding changed: %+v", got)
	}
}
