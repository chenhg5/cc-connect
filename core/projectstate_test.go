package core

import (
	"encoding/json"
	"os"
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

// A Windows build from before the separator normalization keyed overrides by a
// backslash workspace. Those entries must stay readable after an upgrade, and
// clearing a workspace must drop the legacy entry too — otherwise the fallback
// would read back an override the caller just cleared.
func TestWorkspaceOverride_LegacyBackslashKeys(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "projects", "demo.state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}

	const (
		legacyDirKey     = `C:\work\repo:feishu:oc_1:ou_1`
		legacyModelKey   = `C:\work\repo:feishu:oc_1:ou_1`
		legacyOtherKey   = `C:\work\other:feishu:oc_2:ou_2`
		canonicalKey     = "C:/work/repo:feishu:oc_1:ou_1"
		canonicalOther   = "C:/work/other:feishu:oc_2:ou_2"
		canonicalMissing = "C:/work/missing:feishu:oc_3:ou_3"
	)

	seed, err := json.Marshal(map[string]any{
		"workspace_dir_overrides": map[string]string{
			legacyDirKey:   "/old/dir",
			legacyOtherKey: "/old/other",
		},
		"workspace_model_overrides": map[string]string{
			legacyModelKey: "glm-legacy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewProjectStateStore(statePath)

	if got := store.WorkspaceDirOverride(canonicalKey); got != "/old/dir" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want legacy value %q", canonicalKey, got, "/old/dir")
	}
	if got := store.WorkspaceModelOverride(canonicalKey); got != "glm-legacy" {
		t.Fatalf("WorkspaceModelOverride(%q) = %q, want legacy value %q", canonicalKey, got, "glm-legacy")
	}

	// A workspace that only differs by the drive suffix must not inherit its
	// neighbour's legacy override.
	if got := store.WorkspaceDirOverride(canonicalMissing); got != "" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want empty", canonicalMissing, got)
	}
	// Same for a workspace whose path shares no name with the legacy key.
	if got := store.WorkspaceDirOverride("C:/work/repo2:feishu:oc_1:ou_1"); got != "" {
		t.Fatalf("WorkspaceDirOverride(repo2) = %q, want empty", got)
	}

	// A canonical entry wins over the legacy one.
	store.SetWorkspaceDirOverride(canonicalKey, "/new/dir")
	if got := store.WorkspaceDirOverride(canonicalKey); got != "/new/dir" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want canonical value %q", canonicalKey, got, "/new/dir")
	}

	// Clearing must remove both forms, or the next read falls back to the
	// legacy entry and the override "comes back".
	store.ClearWorkspaceDirOverride(canonicalKey)
	store.ClearWorkspaceModelOverride(canonicalKey)
	store.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceDirOverride(canonicalKey); got != "" {
		t.Fatalf("WorkspaceDirOverride(%q) after clear = %q, want empty", canonicalKey, got)
	}
	if got := reloaded.WorkspaceDirOverride(legacyDirKey); got != "" {
		t.Fatalf("legacy override %q survived clear = %q, want empty", legacyDirKey, got)
	}
	if got := reloaded.WorkspaceModelOverride(canonicalKey); got != "" {
		t.Fatalf("WorkspaceModelOverride(%q) after clear = %q, want empty", canonicalKey, got)
	}
	// Clearing one workspace must not touch another's legacy entry.
	if got := reloaded.WorkspaceDirOverride(canonicalOther); got != "/old/other" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want %q", canonicalOther, got, "/old/other")
	}
}
