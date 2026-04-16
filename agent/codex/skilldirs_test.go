package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillDirs_UsesProjectAgentAndCodexHomes(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexHome := filepath.Join(tmp, "codex-home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested", "pkg")

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	for _, dir := range []string{
		filepath.Join(repo, "nested", "pkg"),
		filepath.Join(repo, "nested"),
		repo,
		codexHome,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	a := &Agent{workDir: workDir, codexHome: codexHome}
	got := a.SkillDirs()
	want := []string{
		filepath.Join(workDir, ".agents", "skills"),
		filepath.Join(workDir, ".codex", "skills"),
		filepath.Join(repo, "nested", ".agents", "skills"),
		filepath.Join(repo, "nested", ".codex", "skills"),
		filepath.Join(repo, ".agents", "skills"),
		filepath.Join(repo, ".codex", "skills"),
		filepath.Join(codexHome, "skills"),
		filepath.Join(home, ".agents", "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(SkillDirs()) = %d, want %d\n got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SkillDirs()[%d] = %q, want %q\nfull=%v", i, got[i], want[i], got)
		}
	}
}

func TestSkillDirs_FallsBackToEnvCodexHome(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexHome := filepath.Join(tmp, "profile-home")
	workDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	found := false
	for _, dir := range got {
		if dir == filepath.Join(codexHome, "skills") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SkillDirs() missing CODEX_HOME skills dir: %v", got)
	}
}
