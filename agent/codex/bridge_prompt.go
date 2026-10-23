package codex

import (
	"encoding/json"
	"os"
	"strings"
)

// Be conservative when the local transcript is unavailable (e.g. remote or
// newer paginated storage): reinjection is safer than silently missing host
// instructions. For legacy JSONL, avoid repeating the same bridge version on
// every reconnect and disregard messages removed by compaction.
func hasCurrentBridgePrompt(sessionID, codexHome, prompt string) bool {
	f, err := os.Open(findSessionFile(sessionID, codexHome))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var found bool
	inspect := func(raw json.RawMessage) {
		var message struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &message) != nil || message.Role != "developer" {
			return
		}
		for _, part := range message.Content {
			if strings.Contains(part.Text, "You are running inside cc-connect") {
				found = strings.Contains(part.Text, prompt)
			}
		}
	}
	err = readJSONLines(f, func(line []byte) error {
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return nil
		}
		switch record.Type {
		case "response_item":
			inspect(record.Payload)
		case "compacted":
			found = false
			var compacted struct {
				ReplacementHistory []json.RawMessage `json:"replacement_history"`
			}
			if json.Unmarshal(record.Payload, &compacted) == nil {
				for _, item := range compacted.ReplacementHistory {
					inspect(item)
				}
			}
		}
		return nil
	})
	return err == nil && found
}
