package antigravity

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
	"unicode/utf8"
)

// Exercise the adapter's real result decoder, including pre-existing summary cuts.
func TestToolResultTail_PreservesOriginalOutput(t *testing.T) {
	for _, prefix := range []string{strings.Repeat("older output\n", 100), strings.Repeat("汉", 600)} {
		output := prefix + "\n最近一行\nFINAL_ERROR_MARKER"
		var events []core.Event
		for _, failed := range []bool{false, true} {
			info := map[string]any{"name": "shell", "output": output}
			if failed {
				info["error"] = map[string]any{"message": output}
				info["output"] = "partial output"
			}
			raw, err := json.Marshal(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 1, "state": "DONE", "step_type": "tool", "tool_info": info}})
			if err != nil {
				t.Fatal(err)
			}
			_, parsed := newAgyOutputParser().parseLine(raw)
			events = append(events, parsed...)
		}
		found := 0
		for _, event := range events {
			if event.Type != core.EventToolResult {
				continue
			}
			found++
			if !strings.HasSuffix(event.ToolResultTail, "最近一行\nFINAL_ERROR_MARKER") || utf8.RuneCountInString(event.ToolResultTail) > 500 || !utf8.ValidString(event.ToolResultTail) {
				t.Fatalf("original output tail lost: %#v", event)
			}
		}
		if found == 0 {
			t.Fatal("no tool result emitted")
		}
	}
}
