package controlplane

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreActivationSkipsHandoffThenRestoresExactSlotAndDatabase(t *testing.T) {
	directory := t.TempDir()
	releases := filepath.Join(directory, "releases")
	previous := filepath.Join(releases, "v0.1.0")
	target := filepath.Join(releases, "v0.2.0")
	for _, slot := range []string{previous, target} {
		if err := os.MkdirAll(slot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(slot, "cc-connect-control"), []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(directory, "current")
	if err := os.Symlink(target, current); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "control.db")
	backup := filepath.Join(directory, "control.backup.db")
	if err := os.WriteFile(database, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(directory, "activation.json")
	if err := writeActivation(recordPath, ActivationRecord{RunID: "run", TargetTag: "v0.2.0", TargetDirectory: target,
		PreviousTag: "v0.1.0", PreviousDirectory: previous, DatabasePath: database, DatabaseBackup: backup, SkipNextRecovery: true}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreActivation(recordPath, releases, current, database); err != nil {
		t.Fatalf("handoff recovery error = %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(current)
	canonicalTarget, _ := filepath.EvalSymlinks(target)
	if resolved != canonicalTarget {
		t.Fatalf("handoff changed current link to %s", resolved)
	}
	if err := RestoreActivation(recordPath, releases, current, database); err != nil {
		t.Fatalf("candidate failure recovery error = %v", err)
	}
	resolved, _ = filepath.EvalSymlinks(current)
	canonicalPrevious, _ := filepath.EvalSymlinks(previous)
	if resolved != canonicalPrevious {
		t.Fatalf("current link = %s, want %s", resolved, previous)
	}
	raw, _ := os.ReadFile(database)
	if string(raw) != "previous" {
		t.Fatalf("database = %q", raw)
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Fatalf("activation record still exists: %v", err)
	}
}

func TestRestoreActivationRejectsPathsOutsideConfiguredReleaseDirectory(t *testing.T) {
	directory := t.TempDir()
	releases := filepath.Join(directory, "releases")
	outside := filepath.Join(directory, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "cc-connect-control"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "control.db")
	backup := filepath.Join(directory, "backup.db")
	_ = os.WriteFile(database, []byte("candidate"), 0o600)
	_ = os.WriteFile(backup, []byte("previous"), 0o600)
	recordPath := filepath.Join(directory, "activation.json")
	if err := writeActivation(recordPath, ActivationRecord{RunID: "run", TargetTag: "v2", TargetDirectory: outside,
		PreviousTag: "v1", PreviousDirectory: outside, DatabasePath: database, DatabaseBackup: backup}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreActivation(recordPath, releases, filepath.Join(directory, "current"), database); err == nil {
		t.Fatal("outside previous release was accepted")
	}
}
