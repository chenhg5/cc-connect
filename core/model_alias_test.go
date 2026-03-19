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
