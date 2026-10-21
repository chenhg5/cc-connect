package cursor

import (
	"path/filepath"
	"testing"
)

func TestSkillDirs_IncludesCursorAndClaudePaths(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")

	t.Setenv("HOME", home)

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	want := []string{
		filepath.Join(workDir, ".cursor", "skills"),
		filepath.Join(workDir, ".claude", "skills"),
		filepath.Join(home, ".cursor", "skills"),
		filepath.Join(home, ".claude", "skills"),
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

func TestSkillDirs_CursorBeforeClaude(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")

	t.Setenv("HOME", home)

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()

	cursorWork := filepath.Join(workDir, ".cursor", "skills")
	claudeWork := filepath.Join(workDir, ".claude", "skills")
	cursorIdx, claudeIdx := -1, -1
	for i, d := range got {
		if d == cursorWork {
			cursorIdx = i
		}
		if d == claudeWork {
			claudeIdx = i
		}
	}
	if cursorIdx == -1 || claudeIdx == -1 {
		t.Fatalf("expected both cursor and claude work-dir skill paths, got %v", got)
	}
	if cursorIdx > claudeIdx {
		t.Fatalf("cursor skills should be searched before claude skills, got cursor at %d, claude at %d (%v)", cursorIdx, claudeIdx, got)
	}
}
