package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
)

func TestTeamAdopt_PreservesOriginalSessionsCredentialsAndNativeRuntime(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "old.toml")
	output := filepath.Join(dir, "new.toml")
	t.Setenv("TEAM_TEST_SECRET", "secret-value")
	raw := `data_dir="` + dir + `"
[[bots]]
id="boss"
name="Boss"
role="manager"
bot_owner_id="ou_owner"
admin_from="ou_owner"
[bots.agent]
type="codex"
[bots.agent.options]
work_dir="` + dir + `"
[[bots.platforms]]
type="feishu"
[bots.platforms.options]
app_id="cli_test"
app_secret="${TEAM_TEST_SECRET}"
`
	if err := os.WriteFile(input, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load(input)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := before.EffectiveProject(before.RuntimeProjects()[0])
	if err != nil {
		t.Fatal(err)
	}
	oldDir, err := botContextDataDir(dir, effective, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = adoptTeamFile(input, output); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bots) != 0 || len(c.Projects) != 1 || len(c.Teams) != 1 || c.Projects[0].ID != "" || c.Projects[0].SessionDataDir != oldDir {
		t.Fatal("migration lost runtime identity or history path")
	}
	data, _ := os.ReadFile(output)
	if strings.Contains(string(data), "secret-value") || !strings.Contains(string(data), "${TEAM_TEST_SECRET}") {
		t.Fatal("credentials expanded during export")
	}
	original, _ := os.ReadFile(input)
	if string(original) != raw {
		t.Fatal("source overwritten")
	}
	if adoptTeamFile(input, output) == nil {
		t.Fatal("overwrote migration destination")
	}
}
