package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for issue #1604: the per-group display default
// (`[projects.<project>.groups.<id>.display] mode`).

func TestEffectiveGroupDisplays(t *testing.T) {
	mode := func(s string) *string { return &s }
	proj := &ProjectConfig{
		Groups: map[string]*GroupConfig{
			"feishu:oc_a": {Display: &GroupDisplayConfig{Mode: mode("quiet")}},
			"feishu:oc_b": {Display: &GroupDisplayConfig{Mode: mode("COMPACT")}},
			"feishu:oc_c": {Display: &GroupDisplayConfig{}}, // no mode -> ignored
			"feishu:oc_d": {},                               // no display -> ignored
			"feishu:oc_e": nil,                              // nil group -> ignored
			"feishu:oc_f": {Display: &GroupDisplayConfig{Mode: mode("bogus")}},
		},
	}

	got := EffectiveGroupDisplays(proj)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%v)", len(got), got)
	}

	a, ok := got["feishu:oc_a"]
	if !ok {
		t.Fatal("feishu:oc_a missing")
	}
	if a.Mode != "quiet" {
		t.Errorf("feishu:oc_a mode = %q, want quiet", a.Mode)
	}
	if a.ThinkingMessages || a.ToolMessages {
		t.Errorf("feishu:oc_a thinking/tool = %v/%v, want false/false", a.ThinkingMessages, a.ToolMessages)
	}

	b, ok := got["feishu:oc_b"]
	if !ok {
		t.Fatal("feishu:oc_b missing")
	}
	if b.Mode != "compact" {
		t.Errorf("feishu:oc_b mode = %q, want compact (case-insensitive)", b.Mode)
	}
	if b.ThinkingMessages || b.ToolMessages {
		t.Errorf("feishu:oc_b thinking/tool = %v/%v, want false/false", b.ThinkingMessages, b.ToolMessages)
	}

	// An unknown mode is ignored rather than silently applied.
	if _, ok := got["feishu:oc_f"]; ok {
		t.Error("feishu:oc_f with an invalid mode must be ignored")
	}
}

func TestEffectiveGroupDisplaysNilProject(t *testing.T) {
	got := EffectiveGroupDisplays(nil)
	if got == nil {
		t.Fatal("want an empty map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestLoad_GroupDisplayMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[[projects]]
name = "demo"

[projects.agent]
type = "claudecode"

[projects.agent.options]
mode = "default"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test-token"

[projects.groups."feishu:oc_a".display]
mode = "quiet"

[projects.groups."feishu:oc_b".display]
mode = "compact"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(cfg.Projects))
	}

	groups := EffectiveGroupDisplays(&cfg.Projects[0])
	if got := groups["feishu:oc_a"].Mode; got != "quiet" {
		t.Errorf("feishu:oc_a mode = %q, want quiet", got)
	}
	if got := groups["feishu:oc_b"].Mode; got != "compact" {
		t.Errorf("feishu:oc_b mode = %q, want compact", got)
	}
}

func TestValidateGroupDisplayMode(t *testing.T) {
	mode := func(s string) *string { return &s }

	tests := []struct {
		name    string
		group   *GroupConfig
		wantErr string
	}{
		{
			name:  "valid mode",
			group: &GroupConfig{Display: &GroupDisplayConfig{Mode: mode("quiet")}},
		},
		{
			name:    "invalid mode",
			group:   &GroupConfig{Display: &GroupDisplayConfig{Mode: mode("loud")}},
			wantErr: `projects[0].groups["feishu:oc_a"].display.mode must be "full", "compact", or "quiet"`,
		},
		{
			name:  "no mode set",
			group: &GroupConfig{Display: &GroupDisplayConfig{}},
		},
		{
			name:  "no display set",
			group: &GroupConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Projects: []ProjectConfig{validProject("demo")}}
			cfg.Projects[0].Groups = map[string]*GroupConfig{"feishu:oc_a": tt.group}
			err := cfg.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate() = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate() = %q, want contains %q", err.Error(), tt.wantErr)
			}
		})
	}
}
