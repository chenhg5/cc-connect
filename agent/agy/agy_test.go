package agy_test

import (
	"testing"

	"github.com/chenhg5/cc-connect/agent/agy"
)

func TestNew_defaults(t *testing.T) {
	opts := map[string]any{
		"cmd":      "/bin/echo",
		"work_dir": "/tmp",
		"mode":     "yolo",
	}
	a, err := agy.New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if a.Name() != "agy" {
		t.Errorf("Name() = %q, want %q", a.Name(), "agy")
	}
}

func TestNew_missingBinary(t *testing.T) {
	opts := map[string]any{
		"cmd": "/nonexistent/agy-binary",
	}
	_, err := agy.New(opts)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"auto", "yolo"},
		{"default", "default"},
		{"", "default"},
		{"unknown", "default"},
	}
	for _, c := range cases {
		got := agy.NormalizeModeExported(c.input)
		if got != c.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
