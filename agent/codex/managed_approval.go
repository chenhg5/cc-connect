package codex

import "fmt"

// Keep the server's structured decision intact. The button carries only a
// scoped choice ID; clients cannot supply or broaden policy amendments.
func managedApprovalChoices(params map[string]any, request *managedRequest) []string {
	available, explicit := params["availableDecisions"].([]any)
	if !explicit {
		available = []any{"accept", "decline", "cancel"}
		if amendment, ok := params["proposedExecpolicyAmendment"].([]any); ok && len(amendment) > 0 {
			available = append(available, map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": amendment}})
		}
	}
	request.choicePayloads = map[string]any{}
	var choices []string
	for _, payload := range available {
		choice := managedApprovalChoiceID(payload)
		if choice == "" {
			continue
		}
		if _, exists := request.choicePayloads[choice]; exists {
			choice = fmt.Sprintf("%s_%d", choice, len(choices))
		}
		request.choicePayloads[choice] = payload
		choices = append(choices, choice)
	}
	return choices
}

func managedApprovalChoiceID(payload any) string {
	if value, ok := payload.(string); ok {
		return map[string]string{"accept": "allow", "acceptForSession": "allow_session", "decline": "deny", "cancel": "cancel"}[value]
	}
	value, ok := payload.(map[string]any)
	if !ok || len(value) != 1 {
		return ""
	}
	if amendment, ok := value["acceptWithExecpolicyAmendment"].(map[string]any); ok {
		if prefix, ok := amendment["execpolicy_amendment"].([]any); ok && len(prefix) > 0 {
			return "allow_similar"
		}
	}
	if amendment, ok := value["applyNetworkPolicyAmendment"].(map[string]any); ok {
		if policy, ok := amendment["network_policy_amendment"].(map[string]any); ok {
			if action, ok := policy["action"].(string); ok && (action == "allow" || action == "deny") {
				return "network_" + action
			}
		}
	}
	return ""
}

func managedApprovalChoiceDetails(payload any) string {
	value, _ := payload.(map[string]any)
	if amendment, ok := value["acceptWithExecpolicyAmendment"].(map[string]any); ok {
		return appServerJSON(amendment["execpolicy_amendment"])
	}
	if amendment, ok := value["applyNetworkPolicyAmendment"].(map[string]any); ok {
		if policy, ok := amendment["network_policy_amendment"].(map[string]any); ok {
			host, _ := policy["host"].(string)
			return host
		}
	}
	return ""
}
