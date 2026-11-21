package config

import (
	"os"
	"path/filepath"
	"testing"
)

// codex_home is a path-like agent option, so it has to be expanded like
// work_dir: `codex_home = "~/.codex-cc-connect"` must reach the CLI as the
// absolute path (issue #1909).
func TestExpandHomeInConfig_ExpandsCodexHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}

	cfg := &Config{
		Projects: []ProjectConfig{
			{
				Agent: AgentConfig{
					Options: map[string]any{
						"codex_home": "~/.codex-cc-connect",
						"work_dir":   "~/work",
					},
				},
			},
		},
	}

	expandHomeInConfig(cfg)

	got, _ := cfg.Projects[0].Agent.Options["codex_home"].(string)
	want := filepath.Join(home, ".codex-cc-connect")
	if got != want {
		t.Fatalf("codex_home = %q, want %q", got, want)
	}

	// work_dir keeps working the same way.
	gotWork, _ := cfg.Projects[0].Agent.Options["work_dir"].(string)
	if wantWork := filepath.Join(home, "work"); gotWork != wantWork {
		t.Fatalf("work_dir = %q, want %q", gotWork, wantWork)
	}
}

func TestExpandHomeInConfig_KeepsAbsoluteCodexHome(t *testing.T) {
	abs := "/var/lib/codex-home"
	cfg := &Config{
		Projects: []ProjectConfig{
			{Agent: AgentConfig{Options: map[string]any{"codex_home": abs}}},
		},
	}

	expandHomeInConfig(cfg)

	got, _ := cfg.Projects[0].Agent.Options["codex_home"].(string)
	if got != abs {
		t.Fatalf("codex_home = %q, want %q", got, abs)
	}
}
