package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDataDirUsesConfiguredDir(t *testing.T) {
	dir := t.TempDir()
	got := checkDataDir(dir)
	if len(got) != 1 || got[0].Status != DoctorPass || got[0].Detail != dir {
		t.Fatalf("configured dir: got %+v, want PASS for %s", got, dir)
	}

	missing := filepath.Join(dir, "missing")
	got = checkDataDir(missing)
	if len(got) != 1 || got[0].Status != DoctorWarn || got[0].Detail != missing+" does not exist" {
		t.Fatalf("missing dir: got %+v", got)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got = checkDataDir(file)
	if len(got) != 1 || got[0].Status != DoctorFail {
		t.Fatalf("file instead of dir: got %+v", got)
	}
}

func TestCheckDataDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := checkDataDir("")
	want := filepath.Join(home, ".cc-connect")
	if len(got) != 1 || got[0].Detail != want+" does not exist" {
		t.Fatalf("fallback: got %+v, want warning for %s", got, want)
	}
}
