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
}

func newAgyOutputParser() *agyOutputParser {
	return &agyOutputParser{toolSteps: make(map[int]bool)}
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
			if strings.EqualFold(step.State, "DONE") {
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
				result.ToolSuccess = &success
				events = append(events, result)
			}
		}
	case "result":
		if !p.hasText && envelope.Result.Response != "" {
			p.hasText = true
			events = append(events, core.Event{Type: core.EventText, Content: envelope.Result.Response})
		}
	}
	return true, events
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
