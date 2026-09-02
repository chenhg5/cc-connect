package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandHomeInConfig_LoadsAndExpandsWorkDir is a regression test for
// issue #1782: a config with work_dir = "~/.codex/workspace" must load with
// the tilde expanded, not passed literally to exec.Cmd.Dir.
func TestExpandHomeInConfig_LoadsAndExpandsWorkDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `[[projects]]
name = "tilde-test"
base_dir = "~/projects"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "~/.codex/workspace"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "x"
app_secret = "y"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadPermissive(cfgPath)
	if err != nil {
		t.Fatalf("LoadPermissive: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(cfg.Projects))
	}
	proj := cfg.Projects[0]

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	wantWorkDir := filepath.Join(home, ".codex/workspace")
	gotWorkDir, _ := proj.Agent.Options["work_dir"].(string)
	if gotWorkDir != wantWorkDir {
		t.Errorf("work_dir = %q, want %q", gotWorkDir, wantWorkDir)
	}

	wantBaseDir := filepath.Join(home, "projects")
	if proj.BaseDir != wantBaseDir {
		t.Errorf("base_dir = %q, want %q", proj.BaseDir, wantBaseDir)
	}
}
