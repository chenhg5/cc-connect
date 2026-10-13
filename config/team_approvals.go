package config

import "strings"

// UseTeamApprovalDefaults selects human approval for newly created Team bots
// and explicit backend changes. Existing members retain their configured mode
// when only their name, role or instructions change.
func UseTeamApprovalDefaults(agent *AgentConfig) {
	if agent.Options == nil {
		agent.Options = make(map[string]any)
	}
	switch agent.Type {
	case "codex":
		// exec cannot return a user's approval to the running CLI process.
		agent.Options["backend"] = "app_server"
		agent.Options["mode"] = "suggest"
	case "claudecode":
		agent.Options["mode"] = "default"
	case "acp":
		if agent.Options["cmd"] != "traecli" {
			return
		}
		var args []string
		switch raw := agent.Options["args"].(type) {
		case []string:
			args = raw
		case []any:
			for _, arg := range raw {
				if value, ok := arg.(string); ok {
					args = append(args, value)
				}
			}
		}
		if len(args) == 0 {
			args = []string{"acp", "serve"}
		}
		agent.Options["args"] = traeApprovalArgs(args)
		// Let the explicit CLI preset determine the initial mode; an inherited
		// ACP session mode could override it when the session starts.
		delete(agent.Options, "mode")
	}
}

func traeApprovalArgs(args []string) []string {
	result := []string{"--permission-mode", "default"}
	for i := 0; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		switch flag {
		case "--permission-mode", "--sandbox", "-s", "--ask-for-approval", "-a":
			if !inline && i+1 < len(args) {
				i++
			}
		case "--yolo", "-y", "--dangerously-bypass-approvals-and-sandbox":
			// Do not inherit bypass flags from a parent into a new member.
		default:
			result = append(result, args[i])
		}
	}
	return result
}
