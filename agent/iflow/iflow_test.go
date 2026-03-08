package iflow

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "default"},
		{"default", "default"},
		{"AUTO-EDIT", "auto-edit"},
		{"auto_edit", "auto-edit"},
		{"edit", "auto-edit"},
		{"plan", "plan"},
		{"yolo", "yolo"},
		{"force", "yolo"},
		{"unknown", "default"},
	}
	for _, tc := range cases {
		if got := normalizeMode(tc.in); got != tc.want {
			t.Fatalf("normalizeMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProviderEnvLocked(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "custom",
				APIKey:  "k1",
				BaseURL: "https://example.com/v1",
				Env: map[string]string{
					"FOO": "bar",
				},
			},
		},
		activeIdx: 0,
	}

	got := a.providerEnvLocked()
	wantSubset := []string{
		"IFLOW_API_KEY=k1",
		"IFLOW_apiKey=k1",
		"IFLOW_BASE_URL=https://example.com/v1",
		"IFLOW_baseUrl=https://example.com/v1",
		"FOO=bar",
	}

	for _, item := range wantSubset {
		if !contains(got, item) {
			t.Fatalf("providerEnvLocked() missing %q; got=%v", item, got)
		}
	}
}

func TestIFlowProjectKey(t *testing.T) {
	path := "/Users/test/project"
	got := iflowProjectKey(path)
	if got != "-Users-test-project" {
		t.Fatalf("iflowProjectKey(%q) = %q", path, got)
	}

	if got := iflowProjectKey(""); got != "" {
		t.Fatalf("iflowProjectKey(\"\") = %q, want empty", got)
	}
}

func TestIFlowResolvedWorkDir(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	linkDir := filepath.Join(base, "link")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	want, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(realDir): %v", err)
	}

	if got := iflowResolvedWorkDir(linkDir); got != want {
		t.Fatalf("iflowResolvedWorkDir(%q) = %q, want %q", linkDir, got, want)
	}
}

func TestExtractIFlowContentText(t *testing.T) {
	if got := extractIFlowContentText("hello"); got != "hello" {
		t.Fatalf("extractIFlowContentText string = %q", got)
	}

	arr := []any{map[string]any{"type": "text", "text": "from-array"}}
	if got := extractIFlowContentText(arr); got != "from-array" {
		t.Fatalf("extractIFlowContentText array = %q", got)
	}

	if got := extractIFlowContentText(123); got != "" {
		t.Fatalf("extractIFlowContentText unexpected = %q", got)
	}
}

func TestPermissionModesKeys(t *testing.T) {
	a := &Agent{}
	modes := a.PermissionModes()
	got := make([]string, 0, len(modes))
	for _, m := range modes {
		got = append(got, m.Key)
	}
	want := []string{"default", "auto-edit", "plan", "yolo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PermissionModes keys = %v, want %v", got, want)
	}
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
