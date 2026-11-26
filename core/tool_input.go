package core

import (
	"encoding/json"
	"strings"
)

// formatToolInput makes structured tool inputs readable in progress messages.
// Unknown tools and malformed inputs keep their original representation.
func formatToolInput(name, raw string) string {
	if !strings.EqualFold(name, "TodoWrite") {
		return raw
	}
	var input struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil || len(input.Todos) == 0 {
		return raw
	}
	lines := make([]string, 0, len(input.Todos))
	for _, todo := range input.Todos {
		content := strings.TrimSpace(todo.Content)
		if content == "" {
			return raw
		}
		icon := "⬜"
		switch todo.Status {
		case "in_progress":
			icon = "🔄"
		case "completed":
			icon = "✅"
		case "pending":
		default:
			return raw
		}
		lines = append(lines, icon+" "+content)
	}
	return strings.Join(lines, "\n")
}

func appendToolStep(steps []ToolStep, name, summary string) []ToolStep {
	step := ToolStep{Kind: ToolStepKindTool, Name: name, Summary: summary}
	if strings.EqualFold(name, "TodoWrite") {
		for i := len(steps) - 1; i >= 0; i-- {
			if steps[i].Kind == ToolStepKindTool && strings.EqualFold(steps[i].Name, name) {
				steps[i] = step
				return steps
			}
		}
	}
	return append(steps, step)
}
