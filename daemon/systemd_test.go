//go:build linux

package daemon

import (
	"strings"
	"testing"
)

func TestEscapeSystemdEnvValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Simple path without special characters
		{"/usr/bin:/usr/local/bin", "/usr/bin:/usr/local/bin"},
		// Path with spaces (WSL Windows paths)
		{"/mnt/c/Program Files/Git/cmd", "/mnt/c/Program Files/Git/cmd"},
		// Path with backslash
		{`C:\Windows\System32`, `C:\\Windows\\System32`},
		// Path with double quote (unlikely but should be escaped)
		{`/path/with"quote`, `/path/with\"quote`},
		// Combined: spaces and backslashes
		{"/mnt/c/Program Files\\Git", "/mnt/c/Program Files\\\\Git"},
	}

	for _, tt := range tests {
		got := escapeSystemdEnvValue(tt.input)
		if got != tt.expected {
			t.Errorf("escapeSystemdEnvValue(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildUnit_PathWithSpaces(t *testing.T) {
	cfg := Config{
		BinaryPath: "/usr/local/bin/cc-connect",
		WorkDir:    "/home/user",
		LogFile:    "/tmp/cc-connect.log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin:/mnt/c/Program Files/Git/cmd:/mnt/c/Program Files/nodejs",
	}

	m := &systemdManager{system: false}
	unit := m.buildUnit(cfg)

	// PATH should be wrapped in double quotes
	if !strings.Contains(unit, `Environment="PATH=`) {
		t.Error("PATH should be wrapped in double quotes")
	}

	// Should contain the Windows path with spaces
	if !strings.Contains(unit, "/mnt/c/Program Files/Git/cmd") {
		t.Error("PATH should contain the Windows path with spaces")
	}

	// Should NOT have unquoted Environment=PATH= (which breaks systemd parsing)
	if strings.Contains(unit, "Environment=PATH=") && !strings.Contains(unit, `Environment="PATH=`) {
		t.Error("PATH should not be unquoted")
	}
}

func TestBuildUnit_EscapesSpecialChars(t *testing.T) {
	cfg := Config{
		BinaryPath: "/usr/local/bin/cc-connect",
		WorkDir:    "/home/user",
		LogFile:    "/tmp/cc-connect.log",
		LogMaxSize: 10485760,
		EnvPATH:    `/usr/bin:/path\with\backslash`,
	}

	m := &systemdManager{system: false}
	unit := m.buildUnit(cfg)

	// Backslashes should be escaped
	if !strings.Contains(unit, `PATH=/usr/bin:/path\\with\\backslash"`) {
		t.Errorf("Backslashes should be escaped in PATH, got:\n%s", unit)
	}
}