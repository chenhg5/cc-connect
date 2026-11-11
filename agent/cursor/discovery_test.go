package cursor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeDiscoveryCLI(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNewDiscoversCursorAgentWithoutAgentAlias(t *testing.T) {
	dir := t.TempDir()
	writeDiscoveryCLI(t, dir, "cursor-agent")
	t.Setenv("PATH", dir)

	a, err := New(map[string]any{"work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).CLIBinaryName(); got != "cursor-agent" {
		t.Fatalf("doctor binary = %q, want cursor-agent", got)
	}
}

func TestNewFallsBackToLegacyAgentAlias(t *testing.T) {
	dir := t.TempDir()
	writeDiscoveryCLI(t, dir, "agent")
	t.Setenv("PATH", dir)

	a, err := New(map[string]any{"work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).CLIBinaryName(); got != "agent" {
		t.Fatalf("doctor binary = %q, want agent", got)
	}
}

func TestNewPrefersCursorAgentWhenBothNamesExist(t *testing.T) {
	dir := t.TempDir()
	writeDiscoveryCLI(t, dir, "agent")
	writeDiscoveryCLI(t, dir, "cursor-agent")
	t.Setenv("PATH", dir)

	a, err := New(map[string]any{"work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).CLIBinaryName(); got != "cursor-agent" {
		t.Fatalf("doctor binary = %q, want canonical cursor-agent", got)
	}
}

func TestNewDoesNotOverrideExplicitCursorCommand(t *testing.T) {
	dir := t.TempDir()
	writeDiscoveryCLI(t, dir, "cursor-agent")
	t.Setenv("PATH", dir)

	_, err := New(map[string]any{"work_dir": dir, "cmd": "cursor-agent-typo"})
	if err == nil || !strings.Contains(err.Error(), "cursor-agent-typo") {
		t.Fatalf("explicit bad command should fail with its name, got %v", err)
	}
}

func TestNewReportsBothInstallNamesWhenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := New(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "cursor-agent") || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("missing CLI should name both supported binaries, got %v", err)
	}
}
