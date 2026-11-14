package core

import (
	"os"
	"path/filepath"
	"testing"
)

// preUpgradeProjectState is the shape an older Windows build persisted: keys use
// native separators, because normalization did not exist when they were written.
const preUpgradeProjectState = `{
  "workspace_dir_overrides": {"C:\\work\\repo:feishu:oc_1": "C:/work/other"},
  "workspace_model_overrides": {"C:\\work\\repo": "opus"}
}`

// Upgrading must not lose overrides a user already set. The canonical key has to
// look the old form up for both call shapes: the bare workspace (model override)
// and the composite "<workspace>:<sessionKey>" (directory override).
func TestProjectState_ReadsPreUpgradeKeys(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	const (
		workspace = "C:/work/repo"
		composite = "C:/work/repo:feishu:oc_1"
	)
	statePath := filepath.Join(t.TempDir(), "demo.state.json")
	// Seeded by hand so the test does not depend on the writer side of the
	// fallback to produce the old form.
	if err := os.WriteFile(statePath, []byte(preUpgradeProjectState), 0o644); err != nil {
		t.Fatal(err)
	}

	ps := NewProjectStateStore(statePath)
	if got := ps.WorkspaceModelOverride(workspace); got != "opus" {
		t.Fatalf("WorkspaceModelOverride(%q) = %q, want %q — pre-upgrade model override was lost", workspace, got, "opus")
	}
	if got := ps.WorkspaceDirOverride(composite); got != "C:/work/other" {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want %q — pre-upgrade directory override was lost", composite, got, "C:/work/other")
	}

	// Clearing must drop the pre-upgrade entry too, otherwise the very next
	// lookup resurrects the value the caller just cleared.
	ps.ClearWorkspaceModelOverride(workspace)
	ps.ClearWorkspaceDirOverride(composite)
	if got := ps.WorkspaceModelOverride(workspace); got != "" {
		t.Fatalf("WorkspaceModelOverride() after clear = %q, want empty", got)
	}
	if got := ps.WorkspaceDirOverride(composite); got != "" {
		t.Fatalf("WorkspaceDirOverride() after clear = %q, want empty", got)
	}
	ps.Save()

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceModelOverride(workspace); got != "" {
		t.Fatalf("WorkspaceModelOverride() after reload = %q, want empty", got)
	}
	if got := reloaded.WorkspaceDirOverride(composite); got != "" {
		t.Fatalf("WorkspaceDirOverride() after reload = %q, want empty", got)
	}
}

// Default mode: the fallback is off, so behaviour is exactly the historical one —
// a native-separator key is never consulted for a canonical workspace.
func TestProjectState_LegacyModeIgnoresNativeKeys(t *testing.T) {
	withWorkspaceKeyMode(t, "")

	statePath := filepath.Join(t.TempDir(), "demo.state.json")
	if err := os.WriteFile(statePath, []byte(preUpgradeProjectState), 0o644); err != nil {
		t.Fatal(err)
	}

	ps := NewProjectStateStore(statePath)
	if got := ps.WorkspaceModelOverride("C:/work/repo"); got != "" {
		t.Fatalf("WorkspaceModelOverride() = %q, want empty in legacy mode", got)
	}
	if got := ps.WorkspaceDirOverride("C:/work/repo:feishu:oc_1"); got != "" {
		t.Fatalf("WorkspaceDirOverride() = %q, want empty in legacy mode", got)
	}
	// The historical key still reads, of course.
	if got := ps.WorkspaceModelOverride(`C:\work\repo`); got != "opus" {
		t.Fatalf("WorkspaceModelOverride(native key) = %q, want %q", got, "opus")
	}
}

// The P2 case from review: two *different* workspaces must never share state just
// because their keys differ only by separator. Here `\data\a\b` is a legitimate
// Unix workspace whose name contains backslashes — the fallback must not let
// "/data/a/b" reach it, and clearing one must not clear the other.
func TestProjectState_NoCrossContaminationBetweenSeparatorForms(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	store := NewProjectStateStore(filepath.Join(t.TempDir(), "demo.state.json"))
	store.SetWorkspaceModelOverride(`\data\a\b`, "backslash-workspace")
	store.SetWorkspaceModelOverride("/data/a/b", "slash-workspace")
	store.SetWorkspaceDirOverride(`\data\a\b:sess`, "/tmp/backslash-dir")
	store.SetWorkspaceDirOverride("/data/a/b:sess", "/tmp/slash-dir")

	if got := store.WorkspaceModelOverride(`\data\a\b`); got != "backslash-workspace" {
		t.Fatalf("WorkspaceModelOverride(backslash workspace) = %q, want %q", got, "backslash-workspace")
	}
	if got := store.WorkspaceModelOverride("/data/a/b"); got != "slash-workspace" {
		t.Fatalf("WorkspaceModelOverride(slash workspace) = %q, want %q", got, "slash-workspace")
	}
	if got := store.WorkspaceDirOverride(`\data\a\b:sess`); got != "/tmp/backslash-dir" {
		t.Fatalf("WorkspaceDirOverride(backslash workspace) = %q, want %q", got, "/tmp/backslash-dir")
	}

	// Clearing one workspace must leave the other intact.
	store.ClearWorkspaceModelOverride("/data/a/b")
	store.ClearWorkspaceDirOverride("/data/a/b:sess")
	if got := store.WorkspaceModelOverride(`\data\a\b`); got != "backslash-workspace" {
		t.Fatalf("clearing the slash workspace also cleared the backslash one: %q", got)
	}
	if got := store.WorkspaceDirOverride(`\data\a\b:sess`); got != "/tmp/backslash-dir" {
		t.Fatalf("clearing the slash workspace also cleared the backslash directory override: %q", got)
	}

	// An unrelated workspace with no override of its own stays empty rather than
	// picking up a neighbour's value.
	if got := store.WorkspaceModelOverride("/data/c/d"); got != "" {
		t.Fatalf("WorkspaceModelOverride(unrelated) = %q, want empty", got)
	}
	if got := store.WorkspaceDirOverride("/data/c/d:sess"); got != "" {
		t.Fatalf("WorkspaceDirOverride(unrelated) = %q, want empty", got)
	}
}
