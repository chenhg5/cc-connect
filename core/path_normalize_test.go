package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDirPath(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmp, "link-project")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("symlinks not supported")
	}

	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"symlink", linkDir, resolvedRealDir},
		{"clean only", filepath.Join(tmp, "real-project", ".", "..", "real-project"), resolvedRealDir},
		{"nonexistent", "/nonexistent/path/./foo/../bar", "/nonexistent/path/bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeDirPath(tt.input); got != tt.want {
				t.Fatalf("NormalizeDirPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
