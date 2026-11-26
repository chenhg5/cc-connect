package config

import (
	"os"
	"strings"
	"testing"
)

const commandAliasFixture = `# keep introduction
language = "en"
unknown_top = "keep"

[[commands]]
name = "deploy" # keep command note
prompt = "old"

[[commands]]
name = "review"
prompt = "review"

[[aliases]]
name = "d" # keep alias note
command = "/deploy" # keep value note

[[aliases]]
name = "r"
command = "/review"

[[projects]]
name = "alpha"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "slack"
`

func TestCommandAliasCRUDPreservesDocument(t *testing.T) {
	tests := []struct {
		name         string
		run          func() error
		want, absent string
	}{
		{"add command", func() error { return AddCommand(CommandConfig{Name: "new", Prompt: "new prompt"}) }, `name = "new"`, ""},
		{"remove command", func() error { return RemoveCommand("deploy") }, `name = "review"`, `name = "deploy"`},
		{"update alias", func() error { return AddAlias(AliasConfig{Name: "d", Command: "/new"}) }, `command = "/new" # keep value note`, `command = "/deploy"`},
		{"add alias", func() error { return AddAlias(AliasConfig{Name: "n", Command: "/new"}) }, `name = "n"`, ""},
		{"remove alias", func() error { return RemoveAlias("d") }, `name = "r"`, `name = "d"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFixture(t, commandAliasFixture)
			patchConfigPath(t, path)
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(got)
			if !strings.HasPrefix(text, "# keep introduction\nlanguage = \"en\"\nunknown_top = \"keep\"") || !strings.Contains(text, `name = "alpha"`) || !strings.Contains(text, tc.want) || tc.absent != "" && strings.Contains(text, tc.absent) {
				t.Fatalf("unrelated document changed or edit missing:\n%s", text)
			}
			if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("unexpected fallback backup: %v", err)
			}
		})
	}
}
