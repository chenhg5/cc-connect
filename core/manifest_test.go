package core

import "testing"

type manifestTestAgent struct {
	stubAgent
}

func (a *manifestTestAgent) NativeCommands() []SlashCommandSpec {
	return []SlashCommandSpec{
		{Name: "help", Description: "Agent help"},
		{Name: "commit", Description: "Create a commit", UsageHint: "[message]"},
	}
}

func (a *manifestTestAgent) CLIDisplayName() string { return "Manifest Test Agent" }

func TestEngineDiscoverAgentNativeGatewayCommands(t *testing.T) {
	agent := &manifestTestAgent{}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.disabledCmds = map[string]bool{"help": true}

	got := e.DiscoverAgentNativeGatewayCommands()
	if len(got) != 1 {
		t.Fatalf("expected 1 native command after filtering, got %#v", got)
	}
	if got[0].Name != "commit" {
		t.Fatalf("native command = %q, want commit", got[0].Name)
	}
	if got[0].UsageHint != "[message]" {
		t.Fatalf("usage hint = %q, want %q", got[0].UsageHint, "[message]")
	}
}
