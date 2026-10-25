package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The padding contract: the timestamp segment is exactly 13 zero-padded
// digits, so lexicographic order is chronological even across the 9→10→11
// digit boundaries. A regression to bare %d breaks both assertions below.
func TestStaleLockDumpNamePadding(t *testing.T) {
	older := staleLockDumpName(999999999, "sessA")  // 9-digit ts
	newer := staleLockDumpName(1000000000, "sessB") // 10-digit ts
	wantOlder := "busy-stale-0000999999999-sessA.txt"
	if older != wantOlder {
		t.Fatalf("got %q, want %q", older, wantOlder)
	}
	if older >= newer {
		t.Fatalf("padded names must sort chronologically: %q vs %q", older, newer)
	}
	if !strings.HasPrefix(newer, "busy-stale-0001000000000-") {
		t.Fatalf("unexpected padded name: %q", newer)
	}
}

func TestPruneStaleLockDumps(t *testing.T) {
	dir := t.TempDir()
	// Interleaved sessions with non-monotonic per-session order: sessA's
	// newest dump (890) is the newest overall and must survive, while the
	// two oldest timestamps (880, 882) must be pruned regardless of which
	// session they belong to. This fixture would fail under a
	// session-key-leading filename, where name order groups by session first.
	names := []string{
		staleLockDumpName(1789215880, "sessA"),
		staleLockDumpName(1789215890, "sessA"),
		staleLockDumpName(1789215882, "sessB"),
		staleLockDumpName(1789215883, "sessB"),
		staleLockDumpName(1789215884, "sessB"),
		staleLockDumpName(1789215885, "sessB"),
		staleLockDumpName(1789215886, "sessB"),
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("stack"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	decoys := []string{"busy-wedge-1789215880.txt", "busy-stale-noext", "notes.txt"}
	for _, n := range decoys {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	decoyDir := filepath.Join(dir, "busy-stale-dir-1.txt")
	if err := os.Mkdir(decoyDir, 0755); err != nil {
		t.Fatal(err)
	}

	pruneStaleLockDumps(dir, 5)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "busy-stale-") && strings.HasSuffix(e.Name(), ".txt") && !e.IsDir() {
			left = append(left, e.Name())
		}
	}
	if len(left) != 5 {
		t.Fatalf("expected 5 remaining dumps, got %d: %v", len(left), left)
	}
	// The two oldest timestamps must be the ones pruned — across sessions.
	for _, gone := range []string{staleLockDumpName(1789215880, "sessA"), staleLockDumpName(1789215882, "sessB")} {
		for _, l := range left {
			if l == gone {
				t.Fatalf("oldest dump %s should have been pruned", gone)
			}
		}
	}
	// The newest five by timestamp survive, including sessA's interleaved 890.
	for _, keep := range []string{
		staleLockDumpName(1789215883, "sessB"), staleLockDumpName(1789215884, "sessB"),
		staleLockDumpName(1789215885, "sessB"), staleLockDumpName(1789215886, "sessB"),
		staleLockDumpName(1789215890, "sessA"),
	} {
		found := false
		for _, l := range left {
			if l == keep {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dump %s should have been kept", keep)
		}
	}
	for _, d := range decoys {
		if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
			t.Fatalf("decoy %s must not be pruned: %v", d, err)
		}
	}
	if _, err := os.Stat(decoyDir); err != nil {
		t.Fatalf("decoy directory must not be pruned: %v", err)
	}
}

func TestPruneStaleLockDumpsNoOps(t *testing.T) {
	// Empty dir: no panic, nothing written.
	empty := t.TempDir()
	pruneStaleLockDumps(empty, 5)
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("empty dir should stay empty: %v %v", entries, err)
	}

	// Exactly keep: nothing pruned.
	exact := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(exact, staleLockDumpName(1789215880+int64(i), "sess"))
		if err := os.WriteFile(p, []byte("stack"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pruneStaleLockDumps(exact, 3)
	entries, err := os.ReadDir(exact)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected all 3 dumps kept, got %d", len(entries))
	}

	// keep=0: everything matching is pruned.
	zero := t.TempDir()
	for i := 0; i < 2; i++ {
		p := filepath.Join(zero, staleLockDumpName(1789215880+int64(i), "sess"))
		if err := os.WriteFile(p, []byte("stack"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pruneStaleLockDumps(zero, 0)
	if entries, err := os.ReadDir(zero); err != nil || len(entries) != 0 {
		t.Fatalf("keep=0 should prune all dumps: %v %v", entries, err)
	}
}

func TestDumpStaleLockStacksEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR redirect unsupported on windows")
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", root)

	dumpStaleLockStacks("sess E2E")

	dir := filepath.Join(root, "busy-stale-dumps")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("dump dir should exist under TMPDIR: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 dump, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "busy-stale-") || !strings.HasSuffix(name, ".txt") {
		t.Fatalf("unexpected dump name: %q", name)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o0600 {
		t.Fatalf("dump must be 0600, got %v", info.Mode().Perm())
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o0700 {
		t.Fatalf("dump dir must be 0700, got %v", fi.Mode().Perm())
	}
	// The dump is written by an async goroutine guarded by a 2s timeout;
	// by the time dumpStaleLockStacks returns it should hold real content.
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "goroutine") {
		t.Fatalf("dump should contain goroutine stacks, got %d bytes", len(data))
	}
}
