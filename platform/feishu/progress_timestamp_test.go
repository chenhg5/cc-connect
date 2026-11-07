package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestProgressCards_ShowStepTimestamps(t *testing.T) {
	for _, kind := range []core.ProgressCardEntryKind{
		core.ProgressEntryInfo, core.ProgressEntryThinking, core.ProgressEntryToolUse,
		core.ProgressEntryToolResult, core.ProgressEntryError,
	} {
		t.Run(string(kind), func(t *testing.T) {
			var item core.ProgressCardEntry
			if err := json.Unmarshal([]byte(`{"text":"pwd","tool":"Bash","timestamp":"2026-10-23T14:03:07+08:00"}`), &item); err != nil {
				t.Fatal(err)
			}
			item.Kind = kind
			b, err := json.Marshal(renderProgressEntryElement(item, "en"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "14:03:07") {
				t.Fatalf("progress row is missing occurrence time: %s", b)
			}
		})
	}
	for _, kind := range []core.ToolStepKind{core.ToolStepKindTool, core.ToolStepKindThinking} {
		t.Run("rich_"+string(kind), func(t *testing.T) {
			var step core.ToolStep
			if err := json.Unmarshal([]byte(`{"Name":"Bash","Summary":"pwd","Timestamp":"2026-10-23T14:03:07+08:00"}`), &step); err != nil {
				t.Fatal(err)
			}
			step.Kind = kind
			b, err := json.Marshal(richStepElement(step))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "14:03:07") {
				t.Fatalf("rich row is missing occurrence time: %s", b)
			}
			if fallback := compactRichFallbackMarkdown([]core.ToolStep{step}); !strings.Contains(fallback, "14:03:07") {
				t.Fatalf("oversize card fallback is missing occurrence time: %s", fallback)
			}
		})
	}
}

func TestProgressCards_LegacyStepsDoNotInventTimestamps(t *testing.T) {
	if got := renderProgressEntryElement(core.ProgressCardEntry{Text: "legacy"}, "en")["content"]; got != "legacy" {
		t.Fatalf("legacy progress content = %v, want legacy", got)
	}
	step := core.ToolStep{Kind: core.ToolStepKindThinking, Summary: "legacy"}
	text := richStepElement(step)["text"].(map[string]any)
	if text["content"] != "legacy" {
		t.Fatalf("legacy rich content = %v, want legacy", text["content"])
	}
}
