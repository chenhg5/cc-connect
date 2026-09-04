package feishu

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// parseInboundCard unwraps Feishu's raw_card_content response and keeps the
// original card JSON separate from the readable summary. The raw payload is
// intentionally omitted when it exceeds maxBytes; callers still receive the
// summary and a truncation marker rather than invalid JSON.
func parseInboundCard(content, messageID string, maxBytes int) (core.InboundCard, bool) {
	raw := unwrapInboundCardJSON(content)
	if len(raw) == 0 {
		return core.InboundCard{}, false
	}
	var card map[string]json.RawMessage
	if err := json.Unmarshal(raw, &card); err != nil {
		return core.InboundCard{}, false
	}

	var schema string
	_ = json.Unmarshal(card["schema"], &schema)
	version := "v1"
	if schema == "2.0" {
		version = "v2"
	}
	summary := extractInteractiveCardText(content)
	if summary == "[interactive card]" || strings.TrimSpace(summary) == "" {
		summary = extractInteractiveCardText(string(raw))
	}

	result := core.InboundCard{
		MessageID: messageID,
		Schema:    schema,
		Version:   version,
		Summary:   summary,
	}
	if maxBytes <= 0 || len(raw) <= maxBytes {
		result.Raw = append(json.RawMessage(nil), raw...)
	} else {
		result.Truncated = true
	}
	return result, true
}

func unwrapInboundCardJSON(content string) []byte {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var wrapper struct {
		JSONCard json.RawMessage `json:"json_card"`
	}
	if json.Unmarshal([]byte(content), &wrapper) == nil && len(wrapper.JSONCard) > 0 {
		var nested string
		if json.Unmarshal(wrapper.JSONCard, &nested) == nil {
			return []byte(nested)
		}
		return append([]byte(nil), wrapper.JSONCard...)
	}
	return []byte(content)
}
