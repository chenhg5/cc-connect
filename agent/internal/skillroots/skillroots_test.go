package skillroots

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindDiscoversNestedSkillRoots(t *testing.T) {
	root := t.TempDir()
	want := []string{
		filepath.Join(root, "cache", "vendor", "plugin", "1.0.0", "skills"),
		filepath.Join(root, "marketplaces", "official", "plugins", "frontend-design", "skills"),
	}
	for _, dir := range want {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(want[0], "real-skill", "references", "template", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir nested asset dir: %v", err)
	}

	got := Find(root)

	if len(got) != len(want) {
		t.Fatalf("Find() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Find()[%d] = %q, want %q\nfull=%v", i, got[i], want[i], got)
		}
	}
}

func TestFindAcceptsRootNamedSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	got := Find(root)
	if len(got) != 1 || got[0] != root {
		t.Fatalf("Find() = %v, want [%s]", got, root)
	}
}

func TestFindDoesNotLoopOnSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugin", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir plugin skills: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "plugin", "loop")); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	got := Find(root)

	want := filepath.Join(root, "plugin", "skills")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Find() = %v, want [%s]", got, want)
	}
}
