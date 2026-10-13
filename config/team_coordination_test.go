package config

import (
	"strings"
	"testing"
)

func TestTeamCoordination_ManagerRoutingAndDelegationInstructions(t *testing.T) {
	c := Config{Teams: []TeamConfig{{ID: "team", Manager: "manager", AutoCoordinate: true, Members: []TeamMember{{Bot: "manager", Role: "manager"}, {Bot: "dev", Role: "developer"}}}}}
	for _, name := range []string{"manager", "dev"} {
		p, err := c.effectiveTeamMember(ProjectConfig{Name: name, Agent: AgentConfig{Type: "codex"}})
		if err != nil {
			t.Fatal(err)
		}
		prompt, _ := p.Agent.Options["append_system_prompt"].(string)
		if name == "manager" && !strings.Contains(prompt, "team dispatch") {
			t.Fatal("manager cannot discover approval-preserving delegation")
		}
		if name == "manager" && (!strings.Contains(prompt, "delegate substantive") || !strings.Contains(prompt, "only after the command succeeds")) {
			t.Fatal("manager must actually dispatch execution work before reporting delegation")
		}
		if name == "manager" && (!strings.Contains(prompt, "--reason") || !strings.Contains(prompt, "required next action")) {
			t.Fatal("manager must explain new role creation and actionable failures")
		}
		if !strings.Contains(prompt, "dev") {
			t.Fatal("team roster missing")
		}
	}
}

func TestTeamCoordination_ManagerUsesProportionateWorkflow(t *testing.T) {
	for _, backend := range []string{"codex", "acp"} {
		t.Run(backend, func(t *testing.T) {
			c := Config{Teams: []TeamConfig{{ID: "team", Manager: "manager", AutoCoordinate: true, Members: []TeamMember{{Bot: "manager", Role: "manager"}, {Bot: "dev", Role: "developer"}}}}}
			p, err := c.effectiveTeamMember(ProjectConfig{Name: "manager", Agent: AgentConfig{Type: backend}})
			if err != nil {
				t.Fatal(err)
			}
			prompt, _ := p.Agent.Options["append_system_prompt"].(string)
			for _, rule := range []string{
				"Classify the current request",
				"one qualified executor",
				"developer who already owns the repository context",
				"queue a plan for that member",
				"one manager acceptance",
				"Local commit:",
				"Git worktree:",
				"specific unresolved question, defect, material risk",
				"mandatory repository checks",
				"Never expand authorization",
				"member's answer itself is the deliverable",
				"Do not routinely re-open all cited files",
				"accept it with concise evidence and use NO_REPLY",
				"ensure the useful answer reaches the user once",
			} {
				if !strings.Contains(prompt, rule) {
					t.Errorf("manager prompt lacks %q", rule)
				}
			}
			if strings.Contains(prompt, "delegate substantive planning/review, implementation and verification") || strings.Contains(prompt, "pass each accepted deliverable to the next role") {
				t.Fatal("unconditional multi-role pipeline conflicts with the simple-task policy")
			}
			for _, rule := range []string{"team dispatch", "team accept", "submitted is NOT accepted", "only after the command succeeds"} {
				if !strings.Contains(prompt, rule) {
					t.Errorf("simplification lost task accountability: %q", rule)
				}
			}
			member, err := c.effectiveTeamMember(ProjectConfig{Name: "dev", Agent: AgentConfig{Type: backend}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(member.Agent.Options["append_system_prompt"].(string), "queue a plan for that member") {
				t.Fatal("manager scheduling policy leaked into a worker role")
			}
		})
	}
}
