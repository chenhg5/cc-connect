package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func writeTestSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillDirs_DiscoversPiAndSharedWorkspaceSkills(t *testing.T) {
	for _, localDir := range []string{".pi", ".agents"} {
		t.Run(localDir, func(t *testing.T) {
			home := t.TempDir()
			workspace := filepath.Join(t.TempDir(), "Computer Manager")
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("PI_CODING_AGENT_DIR", "")
			root := filepath.Join(workspace, localDir, "skills")
			writeTestSkill(t, root, "onboarding", "Workspace instructions")
			writeTestSkill(t, filepath.Join(home, ".pi", "agent", "skills"), "onboarding", "Global instructions")
			writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "shared-global", "Shared instructions")
			// Nested templates remain assets, not separate slash commands.
			writeTestSkill(t, filepath.Join(root, "onboarding", "references"), "template", "Template instructions")

			a := &Agent{workDir: workspace}
			registry := core.NewSkillRegistry()
			registry.SetDirs(a.SkillDirs())
			got := registry.Resolve("onboarding")
			if got == nil || got.Source != filepath.Join(root, "onboarding") {
				t.Fatalf("workspace skill missing or shadowed by global skill: %+v", got)
			}
			if registry.Resolve("shared-global") == nil {
				t.Fatal("global shared skill missing")
			}
			if got := registry.ListAll(); len(got) != 2 {
				t.Fatalf("expected two skills without duplicates or nested assets, got %v", got)
			}

			otherWorkspace := t.TempDir()
			writeTestSkill(t, filepath.Join(otherWorkspace, localDir, "skills"), "other-workspace", "Other instructions")
			a.SetWorkDir(otherWorkspace)
			registry.SetDirs(a.SkillDirs())
			if registry.Resolve("other-workspace") == nil {
				t.Fatal("skills did not follow the changed workspace")
			}
			if got := registry.Resolve("onboarding"); got == nil || got.Prompt != "Global instructions" {
				t.Fatalf("old workspace skill leaked after changing work directory: %+v", got)
			}
		})
	}
}

func TestSkillDirs_PreservesLegacyAndCustomAgentSkills(t *testing.T) {
	home, workspace, custom := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	writeTestSkill(t, filepath.Join(workspace, ".pi", "agent", "skills"), "legacy", "Legacy instructions")
	writeTestSkill(t, filepath.Join(custom, "skills"), "custom", "Custom instructions")
	registry := core.NewSkillRegistry()
	registry.SetDirs((&Agent{workDir: workspace}).SkillDirs())
	for _, name := range []string{"legacy", "custom"} {
		if registry.Resolve(name) == nil {
			t.Errorf("missing %s skill", name)
		}
	}
}
