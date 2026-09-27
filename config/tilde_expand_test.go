package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandLeadingHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		"~":                      home,
		"~/.codex/workspace":     filepath.Join(home, ".codex/workspace"),
		"/abs/path":              "/abs/path",
		"relative/path":          "relative/path",
		"/not/a/tilde~/path":     "/not/a/tilde~/path",
	}
	for in, want := range cases {
		if got := expandLeadingHome(in, home); got != want {
			t.Errorf("expandLeadingHome(%q) = %q, want %q", in, got, want)
		}
	}
}
