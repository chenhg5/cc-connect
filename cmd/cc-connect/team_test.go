package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamMigration_PreservesSourceAndCredentialPlaceholders(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "old.toml"), filepath.Join(dir, "new.toml")
	data := `# source comment
[[agent_profiles]]
id = "profile"
role = "manager"
[agent_profiles.agent]
type = "codex"
[[projects]]
name = "boss"
agent_profile = "profile"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "${TEAM_APP}"
app_secret = "${TEAM_SECRET}"
`
	if err := os.WriteFile(input, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAM_SECRET", "must-not-be-exported")
	if err := migrateTeamFile(input, output); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(output)
	if !strings.Contains(string(got), "[[bots]]") || !strings.Contains(string(got), "${TEAM_SECRET}") || strings.Contains(string(got), "must-not-be-exported") {
		t.Fatal("bad migrated config")
	}
	orig, _ := os.ReadFile(input)
	if string(orig) != data {
		t.Fatal("original modified")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential export permissions")
	}
	if err := migrateTeamFile(input, output); err == nil {
		t.Fatal("existing destination overwritten")
	}
}
