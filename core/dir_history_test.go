package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirHistoryAdd_DedupsSymlinkAndRealPath(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("symlinks not supported")
	}

	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}

	dh := NewDirHistory(t.TempDir())
	dh.Add("demo", linkDir)
	dh.Add("demo", realDir)

	got := dh.List("demo")
	if len(got) != 1 {
		t.Fatalf("len(history) = %d, want 1; history=%v", len(got), got)
	}
	if got[0] != resolvedRealDir {
		t.Fatalf("history[0] = %q, want %q", got[0], resolvedRealDir)
	}
}

func TestDirHistoryLoad_MigratesSymlinkEntries(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("symlinks not supported")
	}

	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	storePath := filepath.Join(dataDir, dirHistoryFileName)
	payload := []byte("{\n  \"demo\": [\n    \"" + linkDir + "\",\n    \"" + realDir + "\"\n  ]\n}\n")
	if err := os.WriteFile(storePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	dh := NewDirHistory(dataDir)
	got := dh.List("demo")
	if len(got) != 1 {
		t.Fatalf("len(history) = %d, want 1; history=%v", len(got), got)
	}
	if got[0] != resolvedRealDir {
		t.Fatalf("history[0] = %q, want %q", got[0], resolvedRealDir)
	}

	reloaded := NewDirHistory(dataDir)
	got = reloaded.List("demo")
	if len(got) != 1 || got[0] != resolvedRealDir {
		t.Fatalf("reloaded history = %v, want [%q]", got, resolvedRealDir)
	}
}

func TestDirHistoryPrevious_UsesCanonicalizedHistory(t *testing.T) {
	tmp := t.TempDir()
	realA := filepath.Join(tmp, "real-a")
	realB := filepath.Join(tmp, "real-b")
	if err := os.Mkdir(realA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(realB, 0o755); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(tmp, "link-a")
	if err := os.Symlink(realA, linkA); err != nil {
		t.Skip("symlinks not supported")
	}

	resolvedA, err := filepath.EvalSymlinks(realA)
	if err != nil {
		t.Fatal(err)
	}
	resolvedB, err := filepath.EvalSymlinks(realB)
	if err != nil {
		t.Fatal(err)
	}

	dh := NewDirHistory(t.TempDir())
	dh.Add("demo", linkA)
	dh.Add("demo", realB)

	if got := dh.Previous("demo"); got != resolvedA {
		t.Fatalf("Previous() = %q, want %q", got, resolvedA)
	}
	if got := dh.Get("demo", 1); got != resolvedB {
		t.Fatalf("Get(1) = %q, want %q", got, resolvedB)
	}
}
