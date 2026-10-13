package codex

import "testing"

func TestWorkspaceAgentOptions_PreservesRolePrompts(t *testing.T) {
	parent := &Agent{cmd: "true", systemPrompt: "System policy", appendPrompt: "Planning role"}
	child, err := New(parent.WorkspaceAgentOptions())
	if err != nil {
		t.Fatal(err)
	}
	a := child.(*Agent)
	if a.systemPrompt != parent.systemPrompt || a.appendPrompt != parent.appendPrompt {
		t.Fatalf("workspace lost instructions: system=%q append=%q", a.systemPrompt, a.appendPrompt)
	}
}
