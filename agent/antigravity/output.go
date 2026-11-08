package antigravity

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type agyOutputParser struct {
	sessionID string
	hasText   bool
	toolSteps map[int]bool
	// lastToolInput remembers the latest parameters per tool name so a
	// denied_actions entry can name its target; agy may report the denied
	// step as DONE without output or error.
	lastToolInput map[string]map[string]any
}

type agyOutputEnvelope struct {
	Event          string          `json:"event"`
	ConversationID string          `json:"conversation_id"`
	StepUpdate     agyStepUpdate   `json:"step_update"`
	Result         agyStreamResult `json:"result"`
}

type agyStepUpdate struct {
	ConversationID string       `json:"conversation_id"`
	StepIndex      int          `json:"step_index"`
	State          string       `json:"state"`
	StepType       string       `json:"step_type"`
	ToolName       string       `json:"tool_name"`
	TextDelta      string       `json:"text_delta"`
	ToolInfo       *agyToolInfo `json:"tool_info"`
}

type agyToolInfo struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
	Output     any            `json:"output"`
	Error      *agyToolError  `json:"error"`
}

type agyToolError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type agyStreamResult struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	Response       string `json:"response"`
	Error          string `json:"error"`
	// DeniedActions lists tool confirmations that agy soft-denied because print
	// mode cannot prompt; the turn then ends with an empty response.
	DeniedActions []agyDeniedAction `json:"denied_actions"`
}

type agyDeniedAction struct {
	Action      string `json:"action"`
	DisplayName string `json:"display_name"`
}

func newAgyOutputParser() *agyOutputParser {
	return &agyOutputParser{toolSteps: make(map[int]bool), lastToolInput: make(map[string]map[string]any)}
}

// parseLine returns recognized=false for ordinary text output so older CLI
// versions and wrappers can still be consumed as a plain response.
func (p *agyOutputParser) parseLine(line []byte) (recognized bool, events []core.Event) {
	var header struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(line, &header); err != nil || header.Event == "" {
		return false, nil
	}

	var envelope agyOutputEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return true, nil
	}

	sessionID := envelope.ConversationID
	if sessionID == "" {
		sessionID = envelope.StepUpdate.ConversationID
	}
	if sessionID == "" {
		sessionID = envelope.Result.ConversationID
	}
	if sessionID != "" && sessionID != p.sessionID {
		p.sessionID = sessionID
		events = append(events, core.Event{Type: core.EventText, SessionID: sessionID})
	}

	switch envelope.Event {
	case "step_update":
		step := envelope.StepUpdate
		switch step.StepType {
		case "agent_response":
			if step.TextDelta != "" {
				p.hasText = true
				events = append(events, core.Event{Type: core.EventText, Content: step.TextDelta})
			}
		case "tool":
			toolName := step.ToolName
			var input map[string]any
			if step.ToolInfo != nil {
				if toolName == "" {
					toolName = step.ToolInfo.Name
				}
				input = step.ToolInfo.Parameters
			}
			if toolName == "" {
				toolName = "tool"
			}
			if input != nil {
				p.lastToolInput[toolName] = input
			}
			if !p.toolSteps[step.StepIndex] {
				p.toolSteps[step.StepIndex] = true
				toolInput := ""
				if input != nil {
					toolInput = formatAgyToolInput(input)
				}
				events = append(events, core.Event{
					Type:         core.EventToolUse,
					ToolName:     toolName,
					ToolInput:    toolInput,
					ToolInputRaw: input,
				})
			}
			if strings.EqualFold(step.State, "DONE") || strings.EqualFold(step.State, "ERROR") {
				result := core.Event{
					Type:       core.EventToolResult,
					ToolName:   toolName,
					ToolStatus: "completed",
				}
				success := true
				if step.ToolInfo != nil {
					result.ToolResult = formatAgyOutputValue(step.ToolInfo.Output)
					if step.ToolInfo.Error != nil {
						result.ToolStatus = "failed"
						result.ToolResult = step.ToolInfo.Error.Message
						success = false
					}
				}
				if success && strings.EqualFold(step.State, "ERROR") {
					result.ToolStatus = "failed"
					success = false
				}
				result.ToolSuccess = &success
				events = append(events, result)
			}
		}
	case "result":
		res := envelope.Result
		if !p.hasText && res.Response != "" {
			p.hasText = true
			events = append(events, core.Event{Type: core.EventText, Content: res.Response})
		}
		if notice := p.formatDeniedActions(res.DeniedActions); notice != "" {
			if p.hasText {
				notice = "\n\n" + notice
			}
			p.hasText = true
			events = append(events, core.Event{Type: core.EventText, Content: notice})
		}
	}
	return true, events
}

// formatDeniedActions explains why a headless agy turn stopped early.
// Without it the user only sees an empty reply.
func (p *agyOutputParser) formatDeniedActions(actions []agyDeniedAction) string {
	var items []string
	seen := make(map[string]bool)
	for _, a := range actions {
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			name = strings.TrimSpace(a.Action)
		}
		if name == "" {
			continue
		}
		if target := agyActionTarget(p.lastToolInput[strings.TrimSpace(a.Action)]); target != "" {
			name += " (" + target + ")"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		items = append(items, name)
	}
	if len(items) == 0 {
		return ""
	}
	return "⚠️ agy stopped this turn without finishing: it denied " + strings.Join(items, ", ") +
		" because the action needs interactive confirmation, which agy print mode cannot ask for."
}

// agyActionTarget picks the path or command a tool call acted on.
func agyActionTarget(input map[string]any) string {
	for _, key := range []string{"CommandLine", "Path", "AbsolutePath", "TargetFile", "DirectoryPath", "Url"} {
		if v, _ := input[key].(string); strings.TrimSpace(v) != "" {
			v = strings.TrimSpace(v)
			if r := []rune(v); len(r) > 120 {
				v = string(r[:120]) + "..."
			}
			return v
		}
	}
	return ""
}

func formatAgyOutputValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
