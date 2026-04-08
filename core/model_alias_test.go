package core

import "testing"

func TestResolveModelAlias_CaseInsensitive(t *testing.T) {
	models := []ModelOption{{Name: "gpt-5.3-codex", Alias: "Codex"}}

	got := resolveModelAlias(models, "codex")
	if got != "gpt-5.3-codex" {
		t.Fatalf("resolveModelAlias() = %q, want %q", got, "gpt-5.3-codex")
	}
}

func TestResolveModelAlias_NoMatchFallsBackToInput(t *testing.T) {
	models := []ModelOption{{Name: "gpt-5.3-codex", Alias: "codex"}}

	got := resolveModelAlias(models, "gpt-5.4")
	if got != "gpt-5.4" {
		t.Fatalf("resolveModelAlias() = %q, want original input", got)
	}
}

func TestParseModelSwitchArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{name: "legacy syntax", args: []string{"gpt"}, want: "gpt", ok: true},
		{name: "switch syntax", args: []string{"switch", "gpt"}, want: "gpt", ok: true},
		{name: "missing switch target", args: []string{"switch"}, ok: false},
		{name: "unknown subcommand", args: []string{"list", "gpt"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseModelSwitchArgs(tt.args)
			if ok != tt.ok {
				t.Fatalf("parseModelSwitchArgs() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("parseModelSwitchArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}
