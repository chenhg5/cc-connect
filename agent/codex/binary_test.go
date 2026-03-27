package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCodexBinary_FallsBackToHomeBin(t *testing.T) {
	tmpHome := t.TempDir()
	binDir := filepath.Join(tmpHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	fakeCodex := filepath.Join(binDir, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}

	t.Setenv("HOME", tmpHome)
	t.Setenv("PATH", "/usr/bin:/bin")

	got, err := resolveCodexBinary()
	if err != nil {
		t.Fatalf("resolveCodexBinary: %v", err)
	}
	if got != fakeCodex {
		t.Fatalf("resolveCodexBinary() = %q, want %q", got, fakeCodex)
	}
}
