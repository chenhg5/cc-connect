package core

import "testing"

func TestAgentRoleTemplates_StableCatalogAndCopies(t *testing.T) {
	want := map[string]bool{"manager": true, "planner": true, "developer": true, "tester": true, "reviewer": true}
	roles := AgentRoleTemplates()
	for _, role := range roles {
		if !want[role.ID] || role.Prompt == "" || role.Version != 1 {
			t.Fatalf("invalid or duplicate template: %+v", role)
		}
		delete(want, role.ID)
		if got, err := AgentRoleByID(role.ID); err != nil || got != role {
			t.Fatalf("lookup mismatch: %+v %v", got, err)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing roles: %v", want)
	}
	roles[0].Prompt = "changed"
	if AgentRoleTemplates()[0].Prompt == "changed" {
		t.Fatal("caller modified default role template")
	}
	if _, err := AgentRoleByID("unknown"); err == nil {
		t.Fatal("unknown role accepted")
	}
}
