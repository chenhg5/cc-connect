package codex

import "testing"

func TestAgentClearCommand(t *testing.T) {
	a := &Agent{}
	if got := a.ClearCommand(); got != "/clear" {
		t.Fatalf("ClearCommand() = %q, want %q", got, "/clear")
	}
}
