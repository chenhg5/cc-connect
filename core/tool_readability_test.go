package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolReadability_ProgressPairsConcurrentResults(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	for _, raw := range []string{
		`{"kind":"tool_use","tool":"Bash","text":"echo first","tool_call_id":"a"}`,
		`{"kind":"tool_use","tool":"Bash","text":"echo second","tool_call_id":"b"}`,
		`{"kind":"tool_result","tool":"Bash","text":"first","tool_call_id":"a","success":true}`,
		`{"kind":"tool_result","tool":"Bash","text":"second","tool_call_id":"b","success":true}`,
	} {
		var entry ProgressCardEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatal(err)
		}
		if !w.AppendStructured(entry, entry.Text) {
			t.Fatal("progress was not handled")
		}
	}
	payload, ok := ParseProgressCardPayload(w.content)
	if !ok || len(payload.Items) != 2 {
		t.Fatalf("want two complete invocations, got %#v", payload)
	}
	for i, want := range []string{"first", "second"} {
		data, err := json.Marshal(payload.Items[i])
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if err := json.Unmarshal(data, &row); err != nil {
			t.Fatal(err)
		}
		if row["input"] != "echo "+want || payload.Items[i].Text != want || payload.Items[i].Kind != ProgressEntryToolResult {
			t.Fatalf("result attached to wrong invocation: %s", data)
		}
	}
}

func TestToolReadability_EmptyOutputAndEvictedCalls(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	w.maxEntries = 1
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", ToolCallID: "a", Text: "true"}, "true")
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryInfo, Text: "another update"}, "another update")
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, ToolCallID: "a", Status: "completed"}, "completed with no output")
	payload, ok := ParseProgressCardPayload(w.content)
	if !ok || len(payload.Items) != 1 {
		t.Fatalf("invalid card: %#v", payload)
	}
	row := payload.Items[0]
	if row.Input != "true" || row.Text != "" || row.Tool != "Bash" {
		t.Fatalf("empty output fabricated or input lost: %#v", row)
	}
	if !payload.Truncated || len(w.pendingTools) != 0 {
		t.Fatal("window or pending-call state was lost")
	}
}

func TestToolReadability_RichResultsUseCallID(t *testing.T) {
	steps := []ToolStep{
		{ToolCallID: "a", Kind: ToolStepKindTool, Name: "Bash", Summary: "echo first"},
		{ToolCallID: "b", Kind: ToolStepKindTool, Name: "Bash", Summary: "echo second"},
	}
	steps = mergeRichToolResult(steps, Event{ToolCallID: "a", ToolName: "Bash"}, "first", 500)
	steps = mergeRichToolResult(steps, Event{ToolCallID: "b", ToolName: "Bash"}, "second", 500)
	steps = mergeRichToolResult(steps, Event{ToolCallID: "a", ToolName: "Bash"}, "first updated", 500)
	if len(steps) != 2 || steps[0].Result != "first updated" || steps[1].Result != "second" {
		t.Fatalf("results attached to wrong calls: %#v", steps)
	}
	steps = mergeRichToolResult(steps, Event{ToolCallID: "missing", ToolName: "Bash", ToolInput: "echo orphan"}, "orphan", 500)
	if len(steps) != 3 || steps[1].Result != "second" || steps[2].Summary != "echo orphan" {
		t.Fatalf("orphan overwrote a completed call: %#v", steps)
	}
}

func TestToolReadability_NoIDDoesNotGuessConcurrentCall(t *testing.T) {
	w := &compactProgressWriter{items: []ProgressCardEntry{
		{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "first"},
		{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "second"},
	}}
	if w.matchToolResult(ProgressCardEntry{Kind: ProgressEntryToolResult, Tool: "Bash"}) != -1 {
		t.Fatal("ambiguous result was paired")
	}
	steps := []ToolStep{{Name: "Bash", Summary: "first"}, {Name: "Bash", Summary: "second"}}
	steps = mergeRichToolResult(steps, Event{ToolName: "Bash"}, "unknown", 500)
	if len(steps) != 3 || steps[0].Done || steps[1].Done {
		t.Fatal("ambiguous rich result was paired")
	}
}

func TestToolReadability_PreviewBudgetsAndStructuredInput(t *testing.T) {
	lines := "START\n" + strings.Repeat("building package\n", 1000) + "FAIL"
	if got := TruncateToolOutput(lines, 500); len(strings.Split(got, "\n")) > 8 || !strings.HasSuffix(got, "FAIL") || !strings.HasPrefix(got, "START") {
		t.Fatalf("multiline preview is too long or lost context: %q", got)
	}
	output := "START\n" + strings.Repeat("界", 1000) + "\nFAIL"
	preview := TruncateToolOutput(output, 100)
	if utf8.RuneCountInString(preview) > 100 || !strings.HasPrefix(preview, "START") || !strings.HasSuffix(preview, "FAIL") || !strings.Contains(preview, "\n…\n") {
		t.Fatalf("invalid preview: %q", preview)
	}
	input, err := json.Marshal(map[string]any{"cmd": output, "cwd": "/repo", "timeout_ms": 120000})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolInputPreview(string(input), 100)), &got); err != nil {
		t.Fatal(err)
	}
	if got["cmd"] != preview || got["cwd"] != "/repo" || got["timeout_ms"] != float64(120000) {
		t.Fatalf("structured input was corrupted: %#v", got)
	}
	for _, limit := range []int{1, 2, 3, 4, 5} {
		if utf8.RuneCountInString(TruncateToolOutput(output, limit)) > limit {
			t.Fatal("tiny budget exceeded")
		}
	}
}

func TestToolReadability_RedactsBeforeTruncation(t *testing.T) {
	secret := strings.Repeat("private", 100)
	for _, text := range []string{
		"token=" + secret, `{"password":"` + secret + `","command":"pwd"}`,
		"Authorization: Bearer " + secret, "curl --api-key " + secret,
		"https://user:" + secret + "@example.com?q=1&token=" + secret,
	} {
		if preview := TruncateToolOutput(text, 80); strings.Contains(preview, "private") {
			t.Fatalf("preview leaked a credential: %q", preview)
		}
	}
}

func TestToolReadability_EmptyInputDoesNotBecomeFallbackMarkup(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "WebSearch", ToolCallID: "search"}, "Tool #1 WebSearch")
	payload, ok := ParseProgressCardPayload(w.content)
	if !ok || len(payload.Items) != 1 || payload.Items[0].Text != "" {
		t.Fatalf("invented tool input: %#v", payload)
	}
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, ToolCallID: "search"}, "")
	payload, ok = ParseProgressCardPayload(w.content)
	if !ok || len(payload.Items) != 1 || payload.Items[0].Kind != ProgressEntryToolResult || payload.Items[0].Input != "" {
		t.Fatalf("empty result was dropped: %#v", payload)
	}
}
