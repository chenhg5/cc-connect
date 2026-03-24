package acp

import "testing"

func TestNew_DisplayNameDefault(t *testing.T) {
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if got := agent.CLIDisplayName(); got != "ACP" {
		t.Fatalf("CLIDisplayName = %q, want ACP", got)
	}
}

func TestNew_DisplayNameCustom(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"display_name": "Copilot ACP",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if got := agent.CLIDisplayName(); got != "Copilot ACP" {
		t.Fatalf("CLIDisplayName = %q, want Copilot ACP", got)
	}
}
