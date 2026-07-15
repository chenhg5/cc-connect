package acp

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// mapSessionUpdate turns one ACP session/update payload into zero or more core events.
func mapSessionUpdate(sessionID string, params json.RawMessage) []core.Event {
	var wrap struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil || len(wrap.Update) == 0 {
		slog.Debug("acp: mapSessionUpdate: failed to parse wrap", "error", err, "params", string(params))
		return nil
	}
	sid := wrap.SessionID
	if sid == "" {
		sid = sessionID
	}

	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(wrap.Update, &head); err != nil {
		slog.Debug("acp: mapSessionUpdate: failed to parse head", "error", err, "update", string(wrap.Update))
		return nil
	}

	switch head.SessionUpdate {
	case "agent_message_chunk":
		return mapAgentMessageChunk(sid, wrap.Update)
	case "tool_call":
		return mapToolCall(sid, wrap.Update)
	case "tool_call_update":
		return mapToolCallUpdate(sid, wrap.Update)
	case "plan":
		return mapPlan(sid, wrap.Update)
	case "user_message_chunk":
		// History replay during session/load — suppress to avoid echoing user input.
		return nil
	default:
		// Optional vendor / future ACP shapes — best-effort text extraction.
		return mapSessionUpdateFallback(sid, head.SessionUpdate, wrap.Update)
	}
}

// mapAgentMessageChunk handles agent_message_chunk and user_message_chunk updates.
// It supports multiple JSON formats for broader vendor compatibility:
// - Standard ACP: {"content": {"type": "text", "text": "..."}}
// - Alternative: {"content": {"text": "..."}} (type omitted)
// - Alternative: {"text": "..."} (top-level text field)
// - Alternative: {"content": "..."} (content as string)
func mapAgentMessageChunk(sessionID string, update json.RawMessage) []core.Event {
	// Try standard ACP format first
	var u struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(update, &u); err == nil && u.Content.Text != "" {
		return []core.Event{{
			Type:      core.EventText,
			Content:   u.Content.Text,
			SessionID: sessionID,
		}}
	}

	// Try alternative format: content.text without type field
	var alt1 struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(update, &alt1); err == nil && alt1.Content.Text != "" {
		return []core.Event{{
			Type:      core.EventText,
			Content:   alt1.Content.Text,
			SessionID: sessionID,
		}}
	}

	// Try alternative format: top-level text field
	var alt2 struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(update, &alt2); err == nil && alt2.Text != "" {
		return []core.Event{{
			Type:      core.EventText,
			Content:   alt2.Text,
			SessionID: sessionID,
		}}
	}

	// Try alternative format: content as string
	var alt3 struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(update, &alt3); err == nil && alt3.Content != "" {
		return []core.Event{{
			Type:      core.EventText,
			Content:   alt3.Content,
			SessionID: sessionID,
		}}
	}

	// Log unknown format for debugging
	slog.Debug("acp: mapAgentMessageChunk: unknown format", "update", string(update))
	return nil
}

func mapToolCall(sessionID string, update json.RawMessage) []core.Event {
	var u struct {
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		Kind       string          `json:"kind"`
		Status     string          `json:"status"`
		RawInput   json.RawMessage `json:"rawInput"`
	}
	if err := json.Unmarshal(update, &u); err != nil {
		return nil
	}
	toolName := u.Title
	if toolName == "" {
		toolName = u.Kind
	}
	if toolName == "" {
		toolName = "tool"
	}
	toolInput := summarizeACPToolInput(u.Kind, u.RawInput)
	if toolInput == "" {
		toolInput = u.Title
	}
	return []core.Event{{
		Type:      core.EventToolUse,
		ToolName:  toolName,
		ToolInput: toolInput,
		SessionID: sessionID,
	}}
}

func mapToolCallUpdate(sessionID string, update json.RawMessage) []core.Event {
	var u struct {
		Title      string `json:"title"`
		ToolCallID string `json:"toolCallId"`
		Status     string `json:"status"`
		Content    []struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(update, &u); err != nil {
		return nil
	}
	toolLabel := u.Title
	if toolLabel == "" {
		toolLabel = u.ToolCallID
	}
	if toolLabel == "" {
		toolLabel = "tool"
	}
	body := extractToolCallContentText(u.Content)
	st := strings.ToLower(strings.TrimSpace(u.Status))

	switch {
	case st == "completed" || st == "failed":
		if body == "" && st == "completed" {
			return nil
		}
		if st == "failed" && body == "" {
			body = "(failed)"
		}
		return []core.Event{{
			Type:      core.EventToolResult,
			ToolName:  toolLabel,
			Content:   truncateRunes(body, 800),
			SessionID: sessionID,
		}}
	case st == "in_progress" || st == "pending":
		// Stream intermediate tool output to IM (ACP allows content while not terminal).
		if body == "" {
			return nil
		}
		return []core.Event{{
			Type:      core.EventToolResult,
			ToolName:  toolLabel,
			Content:   truncateRunes(body, 800),
			SessionID: sessionID,
		}}
	default:
		if body != "" {
			return []core.Event{{
				Type:      core.EventToolResult,
				ToolName:  toolLabel,
				Content:   truncateRunes(body, 800),
				SessionID: sessionID,
			}}
		}
		return nil
	}
}

func extractToolCallContentText(blocks []struct {
	Type    string `json:"type"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}) string {
	var b strings.Builder
	for _, c := range blocks {
		if c.Content.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Content.Text)
		}
	}
	return b.String()
}

// mapSessionUpdateFallback handles unknown sessionUpdate values (vendor extensions
// that still carry human-readable text). Never guesses auth or tool semantics.
func mapSessionUpdateFallback(sessionID string, kind string, update json.RawMessage) []core.Event {
	// Some agents may send reasoning as a dedicated discriminator; map to EventThinking.
	switch strings.ToLower(kind) {
	case "reasoning", "reasoning_chunk", "thinking", "agent_thinking_chunk":
		var u struct {
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Text string `json:"text"`
		}
		if json.Unmarshal(update, &u) != nil {
			return nil
		}
		t := u.Content.Text
		if t == "" {
			t = u.Text
		}
		if t == "" {
			return nil
		}
		return []core.Event{{
			Type:      core.EventThinking,
			Content:   t,
			SessionID: sessionID,
		}}
	case "message", "message_chunk", "text", "response":
		// Common vendor extensions for text output
		var u struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
			Text    string `json:"text"`
			Message string `json:"message"`
		}
		if json.Unmarshal(update, &u) != nil {
			return nil
		}
		t := u.Content.Text
		if t == "" {
			t = u.Text
		}
		if t == "" {
			t = u.Message
		}
		if t == "" {
			return nil
		}
		return []core.Event{{
			Type:      core.EventText,
			Content:   t,
			SessionID: sessionID,
		}}
	default:
		// Last resort: try to extract any text-like field from the JSON
		var generic struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
			Text    string `json:"text"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(update, &generic); err != nil {
			return nil
		}
		t := generic.Content.Text
		if t == "" {
			t = generic.Text
		}
		if t == "" {
			t = generic.Message
		}
		if t != "" {
			slog.Debug("acp: mapSessionUpdateFallback: extracted text from unknown format", "kind", kind, "text_len", len(t))
			return []core.Event{{
				Type:      core.EventText,
				Content:   t,
				SessionID: sessionID,
			}}
		}
	}
	slog.Debug("acp: mapSessionUpdateFallback: unrecognized format", "kind", kind, "update", string(update))
	return nil
}

func mapPlan(sessionID string, update json.RawMessage) []core.Event {
	var u struct {
		Entries []struct {
			Content  string `json:"content"`
			Priority string `json:"priority"`
			Status   string `json:"status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(update, &u); err != nil || len(u.Entries) == 0 {
		return nil
	}
	var b strings.Builder
	for i, e := range u.Entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := e.Content
		if e.Status != "" {
			line = "[" + e.Status + "] " + line
		}
		b.WriteString(line)
	}
	return []core.Event{{
		Type:      core.EventThinking,
		Content:   b.String(),
		SessionID: sessionID,
	}}
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}

// permissionOption matches ACP session/request_permission option entries.
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

func pickPermissionOptionID(allow bool, options []permissionOption) string {
	if len(options) == 0 {
		return ""
	}
	if allow {
		for _, o := range options {
			if strings.Contains(strings.ToLower(o.Kind), "allow") {
				return o.OptionID
			}
		}
		for _, o := range options {
			if strings.Contains(strings.ToLower(o.Name), "allow") {
				return o.OptionID
			}
		}
		return options[0].OptionID
	}
	for _, o := range options {
		if strings.Contains(strings.ToLower(o.Kind), "reject") || strings.Contains(strings.ToLower(o.Kind), "deny") {
			return o.OptionID
		}
	}
	for _, o := range options {
		if strings.Contains(strings.ToLower(o.Name), "reject") || strings.Contains(strings.ToLower(o.Name), "deny") {
			return o.OptionID
		}
	}
	return options[len(options)-1].OptionID
}

func buildPermissionResult(allow bool, optionID string) map[string]any {
	if !allow {
		if optionID == "" {
			return map[string]any{
				"outcome": map[string]any{"outcome": "cancelled"},
			}
		}
		return map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": optionID,
			},
		}
	}
	return map[string]any{
		"outcome": map[string]any{
			"outcome":  "selected",
			"optionId": optionID,
		},
	}
}
