package config

import (
	"os"
	"strings"
	"testing"
)

const preservedProjectFixture = `# operator note
language = "en"
unknown_top = "keep"

[[projects]]
name = "alpha" # first project
unknown_project = "keep alpha"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/alpha"

[[projects.platforms]]
type = "telegram" # first platform

[projects.platforms.options]
allow_from = "alpha"

[[projects]]
name = "beta"
unknown_project = "keep beta"

[projects.agent]
type = "codex"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "beta"

[[providers]]
name = "shared"
api_key = "shared-key"
`

func TestAddPlatformToProjectPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, preservedProjectFixture)
	patchConfigPath(t, path)
	if err := AddPlatformToProject("alpha", PlatformConfig{Type: "feishu", Options: map[string]any{"app_id": "new-id"}}, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, after, ok := strings.Cut(preservedProjectFixture, "\n[[projects]]\nname = \"beta\"")
	if !ok || !strings.HasPrefix(string(got), before) || !strings.HasSuffix(string(got), after) {
		t.Fatalf("unrelated project or comment changed:\n%s", got)
	}
	if !strings.Contains(string(got), "[[projects.platforms]]\n  type = \"feishu\"") {
		t.Fatalf("new platform missing: %s", got)
	}
	cfg := readConfigFixture(t, path)
	if len(cfg.Projects[0].Platforms) != 2 || cfg.Projects[0].Platforms[1].Type != "feishu" {
		t.Fatalf("platforms = %+v", cfg.Projects[0].Platforms)
	}
}

func TestAddProjectWithPlatformPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, preservedProjectFixture)
	patchConfigPath(t, path)
	if err := AddPlatformToProject("gamma", PlatformConfig{Type: "feishu", Options: map[string]any{"app_id": "gamma-id"}}, "/gamma", "gemini"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), preservedProjectFixture) {
		t.Fatalf("existing document changed:\n%s", got)
	}
	if !strings.Contains(string(got), "name = \"gamma\"") {
		t.Fatalf("new project missing: %s", got)
	}
	cfg := readConfigFixture(t, path)
	if len(cfg.Projects) != 3 || cfg.Projects[2].Agent.Type != "gemini" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
}

func TestRemoveProjectPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, preservedProjectFixture)
	patchConfigPath(t, path)
	if err := RemoveProject("alpha"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# operator note\nlanguage = \"en\"\nunknown_top = \"keep\"") || !strings.Contains(string(got), "unknown_project = \"keep beta\"") || !strings.Contains(string(got), "api_key = \"shared-key\"") {
		t.Fatalf("unrelated document content changed:\n%s", got)
	}
	if strings.Contains(string(got), "keep alpha") {
		t.Fatalf("deleted project content remains: %s", got)
	}
	cfg := readConfigFixture(t, path)
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "beta" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
}

func TestAddPlatformWithoutOptionsPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, preservedProjectFixture)
	patchConfigPath(t, path)
	if err := AddPlatformToProject("alpha", PlatformConfig{Type: "feishu"}, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# operator note\nlanguage = \"en\"\nunknown_top = \"keep\"") || !strings.Contains(string(got), "unknown_project = \"keep beta\"") {
		t.Fatalf("unrelated document content changed:\n%s", got)
	}
	cfg := readConfigFixture(t, path)
	if len(cfg.Projects[0].Platforms) != 2 || cfg.Projects[0].Platforms[1].Type != "feishu" {
		t.Fatalf("platforms = %+v", cfg.Projects[0].Platforms)
	}
}

func TestAddFirstProjectWithoutOptionsPreservesDocument(t *testing.T) {
	const original = "# operator note\nlanguage = \"en\"\nunknown_top = \"keep\"\n"
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	if err := AddPlatformToProject("alpha", PlatformConfig{Type: "slack"}, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), original) {
		t.Fatalf("original content changed:\n%s", got)
	}
	cfg := readConfigFixture(t, path)
	if len(cfg.Projects) != 1 || cfg.Projects[0].Agent.Type != "codex" || cfg.Projects[0].Platforms[0].Type != "slack" {
		t.Fatalf("projects = %+v", cfg.Projects)
	}
}

func TestProjectPlatformInsertionPreservesCRLF(t *testing.T) {
	for _, tc := range []struct {
		name, project string
	}{
		{"existing project", "alpha"},
		{"new project", "gamma"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := strings.ReplaceAll(preservedProjectFixture, "\n", "\r\n")
			path := writeConfigFixture(t, original)
			patchConfigPath(t, path)
			if err := AddPlatformToProject(tc.project, PlatformConfig{Type: "feishu", Options: map[string]any{"app_id": "new-id"}}, "", ""); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
				t.Fatalf("inserted block used bare LF in CRLF document:\n%s", got)
			}
			if !strings.HasPrefix(string(got), "# operator note\r\nlanguage = \"en\"\r\nunknown_top = \"keep\"") {
				t.Fatalf("original content changed:\n%s", got)
			}
		})
	}
}
