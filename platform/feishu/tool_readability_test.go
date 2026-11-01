package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestToolReadability_PreservesLiteralCommands(t *testing.T) {
	for _, command := range []string{
		`rg -n 'error|failed' src && git status --short`,
		"python3 - <<'PY'\nprint(\"hello\")\nPY",
		"git status --short\ngit log -3 --oneline",
		"printf '%s' `date`",
		"command -v python3",
	} {
		t.Run(command, func(t *testing.T) {
			if got := buildToolDisplay("Bash", command).Detail; got != command {
				t.Fatalf("command changed: got %q, want %q", got, command)
			}
		})
	}
}

func TestToolReadability_CommandContextAndEmbeddedFences(t *testing.T) {
	input := `{"query":"syntax"}`
	if got := buildToolDisplay("readability:lookup", input); got.Title != "readability:lookup" || got.Detail != input {
		t.Fatalf("unrecognized JSON parameters replaced by a key: %#v", got)
	}
	display := buildToolDisplay("functions.exec_command", `{"cmd":"rg 'failed' src","workdir":"/repo","timeout_ms":120000}`)
	for _, want := range []string{"rg 'failed' src", "workdir: /repo", "timeout_ms: 120000"} {
		if !strings.Contains(display.Detail, want) {
			t.Fatalf("lost command context %q: %#v", want, display)
		}
	}
	got := formatProgressToolResult("before\n```\n# not a heading\n```\nafter")
	if strings.Count(got, "```") != 2 || !strings.Contains(got, "# not a heading") || !strings.HasSuffix(got, "after\n```") {
		t.Fatalf("embedded fences escaped the literal block: %q", got)
	}
}

func TestToolReadability_ResultIsLiteralAndRedacted(t *testing.T) {
	row := renderProgressEntryElement(core.ProgressCardEntry{
		Kind: core.ProgressEntryToolResult, Tool: "Bash",
		Text: "# build failed\n[not a link](https://example.com)\nAPI_TOKEN=private-value",
	}, "zh")
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-value") {
		t.Fatal("output leaked a credential")
	}
	// Tool output is data; markdown-looking logs must remain a literal block.
	if !strings.Contains(string(data), "```text\\n# build failed") {
		t.Fatalf("output is interpreted as markup: %s", data)
	}
}
