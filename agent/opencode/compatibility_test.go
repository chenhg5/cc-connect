package opencode

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeMemoryFilesUseCurrentPaths(t *testing.T) {
	workDir := t.TempDir()
	homeDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	a := &Agent{workDir: workDir}

	if got, want := a.ProjectMemoryFile(), filepath.Join(workDir, "AGENTS.md"); got != want {
		t.Fatalf("ProjectMemoryFile() = %q, want %q", got, want)
	}
	if got, want := a.GlobalMemoryFile(), filepath.Join(configDir, "opencode", "AGENTS.md"); got != want {
		t.Fatalf("GlobalMemoryFile() = %q, want %q", got, want)
	}
}

func TestOpencodeGlobalMemoryFileFallsBackToHomeConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")

	a := &Agent{}
	want := filepath.Join(homeDir, ".config", "opencode", "AGENTS.md")
	if got := a.GlobalMemoryFile(); got != want {
		t.Fatalf("GlobalMemoryFile() = %q, want %q", got, want)
	}
}

func TestOpencodeDBPath_PrefersExplicitEnvironment(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.db")
	t.Setenv("OPENCODE_DB", explicit)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	t.Setenv("HOME", t.TempDir())

	if got := opencodeDBPath(); got != explicit {
		t.Fatalf("opencodeDBPath() = %q, want OPENCODE_DB %q", got, explicit)
	}
}

func TestOpencodeDBPath_ResolvesRelativeEnvironmentInDataDir(t *testing.T) {
	xdgDataHome := t.TempDir()
	t.Setenv("OPENCODE_DB", "isolated/custom.db")
	t.Setenv("XDG_DATA_HOME", xdgDataHome)

	want := filepath.Join(xdgDataHome, "opencode", "isolated", "custom.db")
	if got := opencodeDBPath(); got != want {
		t.Fatalf("opencodeDBPath() = %q, want %q", got, want)
	}
}

func TestOpencodeDBPath_PreservesMemoryDatabase(t *testing.T) {
	t.Setenv("OPENCODE_DB", ":memory:")
	if got := opencodeDBPath(); got != ":memory:" {
		t.Fatalf("opencodeDBPath() = %q, want :memory:", got)
	}
}

func TestOpencodeDBPath_XDGAndHomeFallbacks(t *testing.T) {
	t.Run("XDG_DATA_HOME", func(t *testing.T) {
		xdgDataHome := t.TempDir()
		t.Setenv("OPENCODE_DB", "")
		t.Setenv("XDG_DATA_HOME", xdgDataHome)

		want := filepath.Join(xdgDataHome, "opencode", "opencode.db")
		if got := opencodeDBPath(); got != want {
			t.Fatalf("opencodeDBPath() = %q, want %q", got, want)
		}
	})

	t.Run("home", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("OPENCODE_DB", "")
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", homeDir)
		t.Setenv("USERPROFILE", homeDir)

		want := filepath.Join(homeDir, ".local", "share", "opencode", "opencode.db")
		if got := opencodeDBPath(); got != want {
			t.Fatalf("opencodeDBPath() = %q, want %q", got, want)
		}
	})
}

func TestNewMissingCLIUsesCurrentInstallGuide(t *testing.T) {
	missingCLI := filepath.Join(t.TempDir(), "missing-opencode")
	_, err := New(map[string]any{"cmd": missingCLI})
	if err == nil {
		t.Fatal("New() error = nil, want missing CLI error")
	}
	if !strings.Contains(err.Error(), "https://opencode.ai/download") {
		t.Fatalf("New() error = %q, want current install guide", err)
	}
}
