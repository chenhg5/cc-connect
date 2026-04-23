package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillDirs_UsesClaudeConfigDirAndProjectParents(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configHome := filepath.Join(tmp, "profile-home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested", "pkg")

	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)

	for _, dir := range []string{
		filepath.Join(repo, "nested", "pkg"),
		filepath.Join(repo, "nested"),
		repo,
		configHome,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	want := []string{
		filepath.Join(workDir, ".claude", "skills"),
		filepath.Join(repo, "nested", ".claude", "skills"),
		filepath.Join(repo, ".claude", "skills"),
		filepath.Join(configHome, "skills"),
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

func TestSkillDirs_FallsBackToHomeClaudeDir(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	wantLast := filepath.Join(home, ".claude", "skills")
	if got[len(got)-1] != wantLast {
		t.Fatalf("last SkillDirs() = %q, want %q\nfull=%v", got[len(got)-1], wantLast, got)
	}
}
