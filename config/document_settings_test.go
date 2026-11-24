package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const preservedSettingsFixture = `# keep this introduction
language = "en" # language note
unknown_top = "a # sign"
extra.named = "keep"

[display]
tool_messages = true # existing display note

[[projects]]
name = "alpha" # project identity
admin_from = "old"
unknown_project = "keep"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/a#b"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
allow_from = "old"

[[projects]]
name = "beta"
admin_from = "untouched"

[projects.agent]
type = "codex"
`

func TestSaveGlobalSettingsPreservesUneditedDocument(t *testing.T) {
	path := writeConfigFixture(t, preservedSettingsFixture)
	patchConfigPath(t, path)
	language := "zh"
	tools := false
	if err := SaveGlobalSettings(GlobalSettingsUpdate{Language: &language, ToolMessages: &tools}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(preservedSettingsFixture, `language = "en" # language note`, `language = "zh" # language note`, 1)
	want = strings.Replace(want, `tool_messages = true # existing display note`, `tool_messages = false # existing display note`, 1)
	if string(got) != want {
		t.Fatalf("global settings rewrote unrelated TOML:\n%s", got)
	}
}

func TestSaveProjectSettingsPreservesOtherProjects(t *testing.T) {
	path := writeConfigFixture(t, preservedSettingsFixture)
	patchConfigPath(t, path)
	admin := "new"
	workDir := "/tmp/changed"
	if err := SaveProjectSettings("alpha", ProjectSettingsUpdate{
		AdminFrom: &admin,
		WorkDir:   &workDir,
		PlatformAllowFrom: map[string]string{
			"telegram": "u1",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(preservedSettingsFixture, `admin_from = "old"`, `admin_from = "new"`, 1)
	want = strings.Replace(want, `work_dir = "/tmp/a#b"`, `work_dir = "/tmp/changed"`, 1)
	want = strings.Replace(want, `allow_from = "old"`, `allow_from = "u1"`, 1)
	if string(got) != want {
		t.Fatalf("project settings rewrote unrelated TOML:\n%s", got)
	}
}

func TestSaveGlobalSettingsNoOpPreservesBytes(t *testing.T) {
	path := writeConfigFixture(t, preservedSettingsFixture)
	patchConfigPath(t, path)
	if err := SaveGlobalSettings(GlobalSettingsUpdate{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != preservedSettingsFixture {
		t.Fatalf("no-op save rewrote config:\n%s", got)
	}
}

func TestSaveGlobalSettingsPreservesMultilineAndCRLF(t *testing.T) {
	original := strings.ReplaceAll(`# top
language = "en" # note
unknown = """first line
second # line = [value]"""

[display]
tool_messages = true
`, "\n", "\r\n")
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	language := "ru"
	if err := SaveGlobalSettings(GlobalSettingsUpdate{Language: &language}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, `language = "en" # note`, `language = "ru" # note`, 1)
	if string(got) != want {
		t.Fatalf("multiline/CRLF document changed outside edited value:\n%s", got)
	}
}

func TestSaveGlobalSettingsFallbackBacksUpOriginal(t *testing.T) {
	// A ] inside a quoted table name is valid TOML, but outside the
	// source-span editor's header subset. This forces its guarded fallback.
	original := preservedSettingsFixture + "\n[projects.agent.options]\n\n[\"odd]table\"]\nkeep = true\n"
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	language := "ru"
	if err := SaveGlobalSettings(GlobalSettingsUpdate{Language: &language}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatal("fallback backup does not contain the exact original document")
	}
	if info, err := os.Stat(path + ".bak"); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions: info=%v err=%v", info, err)
	}
	cfg := readConfigFixture(t, path)
	if cfg.Language != language {
		t.Fatalf("fallback did not save requested language: %q", cfg.Language)
	}
}

func TestSaveProjectSettingsInsertsAndDeletesKeys(t *testing.T) {
	original := `# keep
language = "en"
[[projects]]
name = "alpha"
admin_from = "old"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "/tmp/old" # remove me
`
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	empty, base, workspace := "", "/tmp/workspaces", "multi-workspace"
	if err := SaveProjectSettings("alpha", ProjectSettingsUpdate{WorkDir: &empty, WorkspaceBaseDir: &base, WorkspaceMode: &workspace}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "work_dir =") || !strings.Contains(string(got), `base_dir = "/tmp/workspaces"`) || !strings.Contains(string(got), `mode = "multi-workspace"`) {
		t.Fatalf("settings insert/delete failed: %s", got)
	}
	if !strings.HasPrefix(string(got), "# keep\n") {
		t.Fatalf("unrelated comment moved: %s", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.Base(path)+".bak")); !os.IsNotExist(err) {
		t.Fatalf("unexpected fallback backup: %v", err)
	}
}

func TestSaveGlobalSettingsInsertsMissingTable(t *testing.T) {
	original := "# top comment\nlanguage = \"en\"\n\n[[projects]]\nname = \"alpha\"\n"
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	level := "debug"
	if err := SaveGlobalSettings(GlobalSettingsUpdate{LogLevel: &level}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), original) || !strings.Contains(string(got), "[log]\nlevel = \"debug\"\n") {
		t.Fatalf("missing table insertion changed existing content: %s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("missing-table edit unexpectedly used fallback: %v", err)
	}
}

func TestSaveProjectSettingsKeepsProviderCompanionChanges(t *testing.T) {
	original := `# unchanged preface
[[providers]]
name = "claude-only"
agent_types = ["claudecode"]
[[providers]]
name = "codex-only"
agent_types = ["codex"]
[[projects]]
name = "alpha"
[projects.agent]
type = "claudecode" # selected agent
provider_refs = ["claude-only", "codex-only"]
[projects.agent.options]
provider = "claude-only"
`
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	newType := "codex"
	if err := SaveProjectSettings("alpha", ProjectSettingsUpdate{AgentType: &newType}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# unchanged preface\n") || !strings.Contains(string(got), `type = "codex" # selected agent`) || !strings.Contains(string(got), `provider_refs = ["codex-only"]`) || strings.Contains(string(got), `provider = "claude-only"`) {
		t.Fatalf("agent switch companion edits are incomplete: %s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("agent switch unexpectedly used fallback: %v", err)
	}
}

func TestSaveGlobalSettingsKeepsConfigPermissions(t *testing.T) {
	path := writeConfigFixture(t, preservedSettingsFixture)
	patchConfigPath(t, path)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	language := "zh"
	if err := SaveGlobalSettings(GlobalSettingsUpdate{Language: &language}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("config mode = %04o, want 0640", info.Mode().Perm())
	}
}
