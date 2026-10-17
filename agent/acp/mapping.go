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
	case "available_commands_update", "config_option_update", "session_info_update", "usage_update", "current_mode_update":
		// Consumed by the session (when useful) and never shown as chat text.
		return nil
	default:
		// Vendor methods such as "_cognition.ai/compaction" must not leak.
		if strings.HasPrefix(head.SessionUpdate, "_") {
			return nil
		}
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
	text := acpMessageText(update)
	if text == "" {
		slog.Debug("acp: mapAgentMessageChunk: unknown format", "update", string(update))
		return nil
	}
	return []core.Event{{
		Type:      core.EventText,
		Content:   text,
		SessionID: sessionID,
	}}
}

// acpMessageText extracts human-readable text from an ACP message chunk.
// It accepts the standard content block plus a few vendor shapes.
func acpMessageText(update json.RawMessage) string {
	var u struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(update, &u); err == nil {
		if u.Content.Text != "" {
			return u.Content.Text
		}
		if u.Text != "" {
			return u.Text
		}
	}
	var asString struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(update, &asString); err == nil && asString.Content != "" {
		return asString.Content
	}
	return ""
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
	case "reasoning", "reasoning_chunk", "thinking", "agent_thinking_chunk", "agent_thought_chunk":
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
		if id := pickAllowOptionID(options); id != "" {
			return id
		}
		return options[0].OptionID
	}
	if id := optionIDByKind(options, "reject_once"); id != "" {
		return id
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

// pickAllowOptionID prefers a one-shot allow. PermissionResult cannot
// tell a single allow from allow-all, so both stay on this path.
// Session-scoped and global options are only used when nothing narrower exists.
func pickAllowOptionID(options []permissionOption) string {
	if id := optionIDByKind(options, "allow_once"); id != "" {
		return id
	}
	for _, o := range options {
		id := strings.ToLower(strings.TrimSpace(o.OptionID))
		if id == "allow_once" || id == "allow" {
			return o.OptionID
		}
	}
	var firstAllow string
	for _, o := range options {
		if !optionLooksAllow(o) {
			continue
		}
		if firstAllow == "" {
			firstAllow = o.OptionID
		}
		if strings.Contains(strings.ToLower(o.Kind), "allow") && !optionIsBroadAllow(o) {
			return o.OptionID
		}
	}
	return firstAllow
}

func optionIDByKind(options []permissionOption, kind string) string {
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o.Kind), kind) {
			return o.OptionID
		}
	}
	return ""
}

func optionLooksAllow(o permissionOption) bool {
	kind := strings.ToLower(o.Kind)
	name := strings.ToLower(o.Name)
	id := strings.ToLower(o.OptionID)
	return strings.Contains(kind, "allow") || strings.Contains(name, "allow") || strings.Contains(id, "allow")
}

func optionIsBroadAllow(o permissionOption) bool {
	blob := strings.ToLower(o.OptionID + " " + o.Name)
	return strings.Contains(blob, "bypass") || strings.Contains(blob, "global")
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
