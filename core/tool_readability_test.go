package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestToolReadability_JSONEscapedCredentialsRemainValid(t *testing.T) {
	for _, command := range []string{
		`API_TOKEN="private-ui-check" pwd`,
		`API_TOKEN="prefix\"private-ui-check" pwd`,
		`SESSION_ID="private-ui-check" pwd`,
		`BEARER="private-ui-check" pwd`,
		`curl -H 'Authorization: Bearer "private-ui-check"' https://example.com`,
	} {
		input, err := json.Marshal(map[string]any{"command": command, "nested": map[string]any{"password": "private-ui-check"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{0, 40, 1000} {
			got := toolInputPreview(string(input), limit)
			if !json.Valid([]byte(got)) || strings.Contains(got, "private-ui-check") {
				t.Fatalf("credential leaked or JSON damaged, limit %d: %q", limit, got)
			}
		}
	}
}

func TestToolReadability_NoIDWindowEvictionDoesNotMisassign(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "echo first"}, "first")
	for i := 0; i < 9; i++ {
		w.Append("other progress")
	}
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "echo second"}, "second")
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, Tool: "Bash", Text: "first"}, "first result")
	payload, ok := ParseProgressCardPayload(w.content)
	if !ok {
		t.Fatal("invalid payload")
	}
	for _, row := range payload.Items {
		if row.Input == "echo second" && row.Text == "first" {
			t.Fatalf("evicted call's result completed another invocation: %#v", row)
		}
	}
}

func TestToolReadability_NoIDTrackingSurvivesEvictionAndAmbiguity(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	appendCall := func(text string) {
		w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: text}, text)
	}
	appendResult := func(text string) {
		w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, Tool: "Bash", Text: text}, text)
	}
	appendCall("unique evicted")
	for i := 0; i < 10; i++ {
		w.Append("other progress")
	}
	appendResult("unique result")
	last := w.items[len(w.items)-1]
	if last.Input != "unique evicted" || last.Text != "unique result" {
		t.Fatalf("lost evicted call: %#v", last)
	}
	appendCall("first")
	appendCall("second")
	appendResult("ambiguous one")
	appendResult("ambiguous two")
	for _, item := range w.items {
		if strings.HasPrefix(item.Text, "ambiguous") && item.Input != "" {
			t.Fatalf("guessed concurrent call: %#v", item)
		}
	}
	appendCall("new unique")
	appendResult("new result")
	last = w.items[len(w.items)-1]
	if last.Input != "new unique" || last.Text != "new result" {
		t.Fatalf("group did not recover: %#v", w.items)
	}
}

func TestToolReadability_MissingIDCannotCompleteAnonymousCallWhileNativePending(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: "card", payload: true}, "test", LangEnglish, nil)
	for _, item := range []ProgressCardEntry{
		{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "native", ToolCallID: "id"},
		{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "anonymous"},
		{Kind: ProgressEntryToolResult, Tool: "Bash", Text: "unknown"},
	} {
		w.AppendStructured(item, item.Text)
	}
	if len(w.items) != 3 || w.items[2].Input != "" {
		t.Fatalf("missing ID was guessed: %#v", w.items)
	}
}

func TestToolReadability_JSONPreviewPreservesLargeNumbers(t *testing.T) {
	input := `{"request_id":9007199254740993,"command":"` + strings.Repeat("x", 100) + `"}`
	if got := toolInputPreview(input, 40); !json.Valid([]byte(got)) || !strings.Contains(got, "9007199254740993") {
		t.Fatalf("JSON number changed: %q", got)
	}
}

func TestToolReadability_EngineBoundsNestedInputInCompletedRow(t *testing.T) {
	p := &stubCompactProgressPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}, style: "card", supportPayload: true}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{ToolMessages: true, ToolMaxLen: 500, Mode: "full"})
	key := "feishu:nested-input"
	session := e.sessions.GetOrCreateActive(key)
	agentSession := newControllableSession("nested-input")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "nested-input"}
	e.interactiveStates[key] = state
	query := "HEAD" + strings.Repeat("界", 60000) + "TAIL"
	input, err := json.Marshal(map[string]any{"queries": []any{map[string]any{"query": query}}, "request_id": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "mcp:batch_search", ToolCallID: "nested", ToolInput: string(input)}
	agentSession.events <- Event{Type: EventToolResult, ToolName: "mcp:batch_search", ToolCallID: "nested", Content: "result"}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, key, "nested-message", time.Now(), nil, nil, state.replyCtx, 0)
	var completedInput string
	for _, edit := range p.getPreviewEdits() {
		if payload, ok := ParseProgressCardPayload(edit); ok {
			for _, item := range payload.Items {
				if item.Kind == ProgressEntryToolResult && item.ToolCallID == "nested" {
					completedInput = item.Input
				}
			}
		}
	}
	var got struct {
		Queries []struct {
			Query string `json:"query"`
		} `json:"queries"`
		RequestID json.Number `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(completedInput), &got); err != nil || len(got.Queries) != 1 {
		t.Fatalf("invalid completed JSON: %v, bytes=%d", err, len(completedInput))
	}
	preview := got.Queries[0].Query
	if utf8.RuneCountInString(preview) > 500 || !strings.HasPrefix(preview, "HEAD") || !strings.HasSuffix(preview, "TAIL") || !strings.Contains(preview, "\n…\n") {
		t.Fatalf("nested preview unbounded or lost head/tail: runes=%d", utf8.RuneCountInString(preview))
	}
	if got.RequestID.String() != "9007199254740993" {
		t.Fatalf("number changed: %s", got.RequestID)
	}
}

func TestToolReadability_NestedInputPreviewKeepsJSONAndRedactsBeforeShortening(t *testing.T) {
	for _, input := range []string{
		`{"nested":{"array":[{"query":"HEAD` + strings.Repeat("x", 60000) + `TAIL"}]}}`,
		`[{"query":"HEAD` + strings.Repeat("x", 60000) + `TAIL"}]`,
		`{"nested":["token=` + strings.Repeat("private-ui-check", 1000) + `"]}`,
	} {
		got := toolInputPreview(input, 500)
		if !json.Valid([]byte(got)) || len(got) > 1000 || strings.Contains(got, "private-ui-check") {
			t.Fatalf("nested preview invalid or too long: bytes=%d", len(got))
		}
		if toolInputPreview(input, 0) != RedactToolText(input) {
			t.Fatal("unlimited preview unexpectedly shortened")
		}
	}
}

func TestToolReadability_JSONPrefixDoesNotHideTrailingCommand(t *testing.T) {
	input := `{"command":"pwd"} && echo ` + strings.Repeat("x", 1000) + "TAIL"
	got := toolInputPreview(input, 100)
	if !strings.Contains(got, "\n…\n") || !strings.HasSuffix(got, "TAIL") {
		t.Fatalf("dropped command tail: %q", got)
	}
}
