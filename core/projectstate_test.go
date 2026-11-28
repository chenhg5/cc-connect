package core

import (
	"path/filepath"
	"testing"
)

func TestProjectState_SaveLoadAndClear(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "projects", "demo.state.json")

	store := NewProjectStateStore(statePath)
	store.SetWorkDirOverride("/tmp/demo")
	store.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkDirOverride(); got != "/tmp/demo" {
		t.Fatalf("WorkDirOverride() = %q, want %q", got, "/tmp/demo")
	}

	reloaded.ClearWorkDirOverride()
	reloaded.Save()

	cleared := NewProjectStateStore(statePath)
	if got := cleared.WorkDirOverride(); got != "" {
		t.Fatalf("WorkDirOverride() after clear = %q, want empty", got)
	}
}

func TestWorkspaceDirOverride(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "projects", "demo.state.json")
	workspaceA := "/tmp/workspace-a"
	workspaceB := "/tmp/workspace-b"

	store := NewProjectStateStore(statePath)
	store.SetWorkDirOverride("/tmp/global")
	store.SetWorkspaceDirOverride(workspaceA, "/tmp/workspace-a/override")
	store.SetWorkspaceDirOverride(workspaceB, "/tmp/workspace-b/override")
	store.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkDirOverride(); got != "/tmp/global" {
		t.Fatalf("WorkDirOverride() = %q, want %q", got, "/tmp/global")
	}
	if got := reloaded.WorkspaceDirOverride(workspaceA); got != "/tmp/workspace-a/override" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want %q", workspaceA, got, "/tmp/workspace-a/override")
	}
	if got := reloaded.WorkspaceDirOverride(workspaceB); got != "/tmp/workspace-b/override" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want %q", workspaceB, got, "/tmp/workspace-b/override")
	}
	if got := reloaded.WorkspaceDirOverride("/tmp/missing"); got != "" {
		t.Fatalf("WorkspaceDirOverride(missing) = %q, want empty", got)
	}

	reloaded.ClearWorkspaceDirOverride(workspaceA)
	reloaded.Save()

	cleared := NewProjectStateStore(statePath)
	if got := cleared.WorkDirOverride(); got != "/tmp/global" {
		t.Fatalf("WorkDirOverride() after workspace clear = %q, want %q", got, "/tmp/global")
	}
	if got := cleared.WorkspaceDirOverride(workspaceA); got != "" {
		t.Fatalf("WorkspaceDirOverride(%q) after clear = %q, want empty", workspaceA, got)
	}
	if got := cleared.WorkspaceDirOverride(workspaceB); got != "/tmp/workspace-b/override" {
		t.Fatalf("WorkspaceDirOverride(%q) after clearing other workspace = %q, want %q", workspaceB, got, "/tmp/workspace-b/override")
	}
}

func TestWorkspaceModelOverride(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "projects", "demo.state.json")
	workspaceA := "/tmp/workspace-a"
	workspaceB := "/tmp/workspace-b"

	store := NewProjectStateStore(statePath)
	store.SetWorkspaceModelOverride(workspaceA, "opus")
	store.SetWorkspaceModelOverride(workspaceB, "sonnet")
	store.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceModelOverride(workspaceA); got != "opus" {
		t.Fatalf("WorkspaceModelOverride(%q) = %q, want %q", workspaceA, got, "opus")
	}
	if got := reloaded.WorkspaceModelOverride(workspaceB); got != "sonnet" {
		t.Fatalf("WorkspaceModelOverride(%q) = %q, want %q", workspaceB, got, "sonnet")
	}

	reloaded.ClearWorkspaceModelOverride(workspaceA)
	reloaded.Save()

	cleared := NewProjectStateStore(statePath)
	if got := cleared.WorkspaceModelOverride(workspaceA); got != "" {
		t.Fatalf("WorkspaceModelOverride(%q) after clear = %q, want empty", workspaceA, got)
	}
	if got := cleared.WorkspaceModelOverride(workspaceB); got != "sonnet" {
		t.Fatalf("WorkspaceModelOverride(%q) after clearing other workspace = %q, want %q", workspaceB, got, "sonnet")
	}
}

// The provider a workspace model was picked for is stored next to the model and
// cleared with it: the rebuild path uses it to decide whether that model may be
// handed to the provider that is active now.
func TestWorkspaceModelProvider(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "projects", "demo.state.json")
	workspaceA := "/tmp/workspace-a"
	workspaceB := "/tmp/workspace-b"

	store := NewProjectStateStore(statePath)
	store.SetWorkspaceModelOverride(workspaceA, "openai/gpt-6.1-sol")
	store.SetWorkspaceModelProvider(workspaceA, "chatgpt")
	store.SetWorkspaceModelOverride(workspaceB, "sonnet")
	store.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceModelProvider(workspaceA); got != "chatgpt" {
		t.Fatalf("WorkspaceModelProvider(%q) = %q, want chatgpt", workspaceA, got)
	}
	// A model stored without a provider (state written before that record existed)
	// reads as empty, which is what makes the rebuild path fall back to the
	// provider's own model list.
	if got := reloaded.WorkspaceModelProvider(workspaceB); got != "" {
		t.Fatalf("WorkspaceModelProvider(%q) = %q, want empty for an entry without a record", workspaceB, got)
	}

	reloaded.ClearWorkspaceModelOverride(workspaceA)
	reloaded.Save()

	cleared := NewProjectStateStore(statePath)
	if got := cleared.WorkspaceModelProvider(workspaceA); got != "" {
		t.Fatalf("WorkspaceModelProvider(%q) after clear = %q, want empty", workspaceA, got)
	}
	if got := cleared.WorkspaceModelOverride(workspaceA); got != "" {
		t.Fatalf("WorkspaceModelOverride(%q) after clear = %q, want empty", workspaceA, got)
	}
	if got := cleared.WorkspaceModelOverride(workspaceB); got != "sonnet" {
		t.Fatalf("WorkspaceModelOverride(%q) after clearing another workspace = %q, want sonnet", workspaceB, got)
	}
}
