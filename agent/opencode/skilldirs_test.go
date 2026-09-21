package opencode

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSkillDirs_UsesProjectAndGlobalLocations(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested", "pkg")

	setTestHome(t, home)

	for _, dir := range []string{
		filepath.Join(repo, "nested", "pkg"),
		filepath.Join(repo, "nested"),
		repo,
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
		filepath.Join(workDir, ".opencode", "skills"),
		filepath.Join(workDir, ".claude", "skills"),
		filepath.Join(workDir, ".agents", "skills"),
		filepath.Join(repo, "nested", ".opencode", "skills"),
		filepath.Join(repo, "nested", ".claude", "skills"),
		filepath.Join(repo, "nested", ".agents", "skills"),
		filepath.Join(repo, ".opencode", "skills"),
		filepath.Join(repo, ".claude", "skills"),
		filepath.Join(repo, ".agents", "skills"),
		filepath.Join(home, ".config", "opencode", "skills"),
		filepath.Join(home, ".claude", "skills"),
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

func TestSkillDirs_AlwaysIncludesGlobalDirs(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace", "pkg")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	setTestHome(t, home)

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	for _, global := range []string{
		filepath.Join(home, ".config", "opencode", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".agents", "skills"),
	} {
		found := false
		for _, d := range got {
			if d == global {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected global dir %q in %v", global, got)
		}
	}
}

func TestSkillDirs_EmptyWorkDirDoesNotPanic(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	a := &Agent{}
	if got := a.SkillDirs(); len(got) == 0 {
		t.Fatal("expected at least the global skill dirs")
	}
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOMEDRIVE", "")
		t.Setenv("HOMEPATH", "")
	}
}
