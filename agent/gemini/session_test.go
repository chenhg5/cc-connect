package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// sanitizeFileName mirrors the logic in geminiSession.Send for file name sanitization.
func sanitizeFileName(fileName string, index int) string {
	fname := filepath.Base(fileName)
	if fname == "" || fname == "." || fname == ".." {
		fname = fmt.Sprintf("cc-connect-file-%d", index)
	}
	return fname
}

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		index    int
		wantSafe bool
		want     string
	}{
		{"normal file", "report.pdf", 0, true, "report.pdf"},
		{"path traversal", "../../etc/passwd", 0, true, "passwd"},
		{"deep traversal", "../../../tmp/evil.sh", 1, true, "evil.sh"},
		{"absolute path", "/etc/shadow", 0, true, "shadow"},
		{"empty name", "", 2, true, "cc-connect-file-2"},
		{"dot only", ".", 3, true, "cc-connect-file-3"},
		{"double dot", "..", 4, true, "cc-connect-file-4"},
		{"subdir file", "subdir/file.txt", 0, true, "file.txt"},
		{"slash path", "dir/subdir/evil.dll", 0, true, "evil.dll"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFileName(tt.input, tt.index)
			if tt.want != "" && got != tt.want {
				t.Errorf("sanitizeFileName(%q, %d) = %q, want %q", tt.input, tt.index, got, tt.want)
			}
			if strings.Contains(got, "..") || strings.Contains(got, "/") {
				t.Errorf("sanitizeFileName(%q, %d) = %q — still contains path traversal chars", tt.input, tt.index, got)
			}
		})
	}
}

// drainEvents reads all events from the channel until it blocks for the given timeout.
func drainEvents(ch <-chan core.Event, timeout time.Duration) []core.Event {
	var events []core.Event
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, evt)
		case <-time.After(timeout):
			return events
		}
	}
}

func TestHandleMessage_DeltaBuffersUntilResult(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Hello ",
		"delta":   true,
	})
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "world!",
		"delta":   true,
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected no events before result, got %d", len(events))
	}

	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})

	events = drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected EventText and EventResult, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "Hello world!" {
		t.Fatalf("event 0: expected EventText 'Hello world!', got %v %q", events[0].Type, events[0].Content)
	}
	if events[1].Type != core.EventResult {
		t.Fatalf("event 1: expected EventResult, got %v", events[1].Type)
	}
}

func TestHandleMessage_DeltaThoughtEmitsThinking(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":  "message",
		"role":  "assistant",
		"delta": true,
		"content": []any{
			map[string]any{"type": "thought", "thought": "checking long context"},
		},
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != core.EventThinking || events[0].Content != "checking long context" {
		t.Fatalf("expected EventThinking, got %v %q", events[0].Type, events[0].Content)
	}
}

func TestHandleMessage_MixedContentSeparatesThoughtFromText(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":  "message",
		"role":  "agent",
		"delta": true,
		"content": []any{
			map[string]any{"type": "thought", "thought": "internal scratchpad"},
			map[string]any{"type": "text", "text": "visible answer"},
		},
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected thinking event before result, got %d", len(events))
	}
	if events[0].Type != core.EventThinking || events[0].Content != "internal scratchpad" {
		t.Fatalf("event 0: expected EventThinking, got %v %q", events[0].Type, events[0].Content)
	}

	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})
	events = drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected EventText and EventResult, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "visible answer" {
		t.Fatalf("event 0: expected EventText, got %v %q", events[0].Type, events[0].Content)
	}
	if events[1].Type != core.EventResult {
		t.Fatalf("event 1: expected EventResult, got %v", events[1].Type)
	}
}

func TestHandleMessage_TopLevelThinkingDeltaDoesNotEmitText(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":     "message",
		"role":     "assistant",
		"content":  "hidden chain",
		"delta":    true,
		"thinking": true,
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != core.EventThinking || events[0].Content != "hidden chain" {
		t.Fatalf("expected EventThinking, got %v %q", events[0].Type, events[0].Content)
	}
}

func TestHandleMessage_FalseThinkingFlagKeepsText(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":     "message",
		"role":     "assistant",
		"content":  "visible text",
		"delta":    true,
		"thinking": "false",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected no events before result, got %d", len(events))
	}

	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})
	events = drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected EventText and EventResult, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "visible text" {
		t.Fatalf("expected EventText, got %v %q", events[0].Type, events[0].Content)
	}
}

func TestHandleMessage_FlattenedThoughtMarkersKeepOnlyLastSegment(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Acknowledging success [Thought: true]Celebrating tiny victories ",
		"delta":   true,
	})
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "[Thought: true]真正回复",
		"delta":   true,
	})
	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected EventText and EventResult, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "真正回复" {
		t.Fatalf("expected final segment only, got %v %q", events[0].Type, events[0].Content)
	}
}

func TestStripGeminiFlattenedThoughts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no marker", "normal answer", "normal answer"},
		{"single marker", "thought[Thought: true]answer", "answer"},
		{"multiple markers", "a[Thought: true]b[Thought: true]\n answer", "answer"},
		{"only thought", "a[Thought: true]", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripGeminiFlattenedThoughts(tt.in); got != tt.want {
				t.Fatalf("stripGeminiFlattenedThoughts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandleThinkingEvent(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type": "thinking",
		"thought": map[string]any{
			"subject":     "Plan",
			"description": "inspect files",
		},
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != core.EventThinking || events[0].Content != "Plan\ninspect files" {
		t.Fatalf("expected EventThinking with subject and description, got %v %q", events[0].Type, events[0].Content)
	}
}

func TestHandleMessage_NonDeltaBuffered(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	// Non-delta message should be buffered (no immediate event)
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Thinking about this...",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 immediate events for non-delta message, got %d", len(events))
	}
	if len(gs.pendingMsgs) != 1 || gs.pendingMsgs[0] != "Thinking about this..." {
		t.Errorf("expected 1 pending message, got %v", gs.pendingMsgs)
	}
}

func TestHandleMessage_NonDeltaFlushedAsThinkingOnToolUse(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	// Buffer a non-delta message
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Let me check...",
	})

	// tool_use should flush it as thinking
	gs.handleEvent(map[string]any{
		"type":      "tool_use",
		"tool_name": "shell",
		"tool_id":   "t1",
		"parameters": map[string]any{
			"command": "ls",
		},
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != core.EventThinking || events[0].Content != "Let me check..." {
		t.Errorf("event 0: expected EventThinking, got %v %q", events[0].Type, events[0].Content)
	}
	if events[1].Type != core.EventToolUse {
		t.Errorf("event 1: expected EventToolUse, got %v", events[1].Type)
	}
}

func TestHandleMessage_NonDeltaFlushedAsTextOnResult(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	// Buffer a non-delta message
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Here is the result.",
	})

	// result should flush it as text
	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "Here is the result." {
		t.Errorf("event 0: expected EventText, got %v %q", events[0].Type, events[0].Content)
	}
	if events[1].Type != core.EventResult {
		t.Errorf("event 1: expected EventResult, got %v", events[1].Type)
	}
}

func TestHandleMessage_UserMessagesIgnored(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "user",
		"content": "Hello agent",
		"delta":   true,
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for user message, got %d", len(events))
	}
}

func TestHandleMessage_EmptyContentIgnored(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "",
		"delta":   true,
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for empty content, got %d", len(events))
	}
}

func TestHandleMessage_MixedDeltaAndNonDelta(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	// Simulate a realistic Gemini CLI output sequence:
	// 1. non-delta thinking message
	// 2. tool_use flushes thinking
	// 3. tool_result
	// 4. delta streaming responses
	// 5. result

	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Let me look at the files.",
	})
	gs.handleEvent(map[string]any{
		"type":       "tool_use",
		"tool_name":  "shell",
		"tool_id":    "t1",
		"parameters": map[string]any{"command": "ls"},
	})
	gs.handleEvent(map[string]any{
		"type":    "tool_result",
		"tool_id": "t1",
		"status":  "success",
		"output":  "file1.txt\nfile2.txt",
	})
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "Here are ",
		"delta":   true,
	})
	gs.handleEvent(map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": "the files.",
		"delta":   true,
	})
	gs.handleEvent(map[string]any{
		"type":   "result",
		"status": "success",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)

	// Expected sequence: EventThinking, EventToolUse, EventToolResult, EventText, EventResult
	if len(events) != 5 {
		var types []string
		for _, e := range events {
			types = append(types, string(e.Type))
		}
		t.Fatalf("expected 5 events, got %d: %v", len(events), types)
	}

	expects := []struct {
		typ     core.EventType
		content string
	}{
		{core.EventThinking, "Let me look at the files."},
		{core.EventToolUse, ""},
		{core.EventToolResult, ""},
		{core.EventText, "Here are the files."},
		{core.EventResult, ""},
	}

	for i, exp := range expects {
		if events[i].Type != exp.typ {
			t.Errorf("event %d: expected type %v, got %v", i, exp.typ, events[i].Type)
		}
		if exp.content != "" && events[i].Content != exp.content {
			t.Errorf("event %d: expected content %q, got %q", i, exp.content, events[i].Content)
		}
	}
}

func TestHandleInit_StoresSessionID(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":       "init",
		"session_id": "abc123",
		"model":      "gemini-2.0-flash",
	})

	if sid := gs.CurrentSessionID(); sid != "abc123" {
		t.Errorf("expected session_id abc123, got %q", sid)
	}

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 || events[0].Type != core.EventText {
		t.Errorf("expected 1 EventText from init, got %v", events)
	}
}

func TestHandleError_EmitsEventError(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	gs.handleEvent(map[string]any{
		"type":     "error",
		"severity": "error",
		"message":  "something broke",
	})

	events := drainEvents(gs.events, 50*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != core.EventError {
		t.Errorf("expected EventError, got %v", events[0].Type)
	}
	if !strings.Contains(events[0].Error.Error(), "something broke") {
		t.Errorf("error should contain message, got %v", events[0].Error)
	}
}

func TestFormatToolParams(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		params   map[string]any
		want     string
	}{
		{"shell command", "shell", map[string]any{"command": "ls -la"}, "ls -la"},
		{"Bash command", "Bash", map[string]any{"command": "echo hello"}, "echo hello"},
		{"write file path only", "write_file", map[string]any{"file_path": "/tmp/test.txt"}, "/tmp/test.txt"},
		{"write file with content", "write_file", map[string]any{"file_path": "/tmp/test.txt", "content": "hello world"}, "`/tmp/test.txt`\n```\nhello world\n```"},
		{"write file full content", "write_file", map[string]any{"file_path": "/tmp/test.txt", "content": strings.Repeat("x", 200)}, "`/tmp/test.txt`\n```\n" + strings.Repeat("x", 200) + "\n```"},
		{"read file path", "read_file", map[string]any{"path": "/tmp/test.txt"}, "/tmp/test.txt"},
		{"replace single line", "replace", map[string]any{"file_path": "/tmp/test.go", "old_string": "foo", "new_string": "bar"}, "`/tmp/test.go`\n```diff\n- foo\n+ bar\n```"},
		{"replace with context", "replace", map[string]any{"file_path": "/tmp/test.go", "old_string": "func foo() {\n    return 1\n}", "new_string": "func foo() {\n    return 2\n}"}, "`/tmp/test.go`\n```diff\n  func foo() {\n-     return 1\n+     return 2\n  }\n```"},
		{"list directory path", "list_directory", map[string]any{"path": "/home/user"}, "/home/user"},
		{"list directory dir_path", "list_directory", map[string]any{"dir_path": "/home/user"}, "/home/user"},
		{"web fetch prompt", "web_fetch", map[string]any{"prompt": "https://example.com fetch this page"}, "https://example.com fetch this page"},
		{"web fetch url fallback", "web_fetch", map[string]any{"url": "https://example.com"}, "https://example.com"},
		{"search query", "google_web_search", map[string]any{"query": "golang testing"}, "golang testing"},
		{"activate skill", "activate_skill", map[string]any{"name": "obsidian-markdown"}, "obsidian-markdown"},
		{"search code", "search_code", map[string]any{"query": "func main"}, "func main"},
		{"Grep pattern", "Grep", map[string]any{"pattern": "TODO"}, "TODO"},
		{"grep_search pattern", "grep_search", map[string]any{"pattern": "TODO"}, "TODO"},
		{"glob lowercase", "glob", map[string]any{"pattern": "*.go"}, "*.go"},
		{"save memory", "save_memory", map[string]any{"fact": "user prefers dark mode"}, "user prefers dark mode"},
		{"ask user", "ask_user", map[string]any{"questions": []any{map[string]any{"question": "Which DB?", "header": "DB", "type": "choice"}}}, "Which DB?"},
		{"enter plan mode", "enter_plan_mode", map[string]any{"reason": "complex task"}, "complex task"},
		{"exit plan mode", "exit_plan_mode", map[string]any{"plan_path": "/tmp/plan.md"}, "/tmp/plan.md"},
		{"nil params", "shell", nil, ""},
		{"unknown single key", "custom_tool", map[string]any{"key": "value"}, "key: value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolParams(tt.toolName, tt.params)
			if got != tt.want {
				t.Errorf("formatToolParams(%q, %v) = %q, want %q", tt.toolName, tt.params, got, tt.want)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cc-connect", "cc-connect"},
		{"Daily", "daily"},
		{"My Project", "my-project"},
		{"hello_world", "hello-world"},
		{"Test.123", "test-123"},
		{"---weird---", "weird"},
		{"", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSessionMessage_TextContent(t *testing.T) {
	// Test plain string content
	m1 := sessionMessage{Type: "user", RawContent: json.RawMessage(`"hello world"`)}
	if got := m1.textContent(); got != "hello world" {
		t.Errorf("string content: got %q, want %q", got, "hello world")
	}

	// Test array of parts content
	m2 := sessionMessage{Type: "user", RawContent: json.RawMessage(`[{"text":"line 1"},{"text":"line 2"}]`)}
	if got := m2.textContent(); got != "line 1\nline 2" {
		t.Errorf("array content: got %q, want %q", got, "line 1\nline 2")
	}

	// Test empty content
	m3 := sessionMessage{Type: "user", RawContent: nil}
	if got := m3.textContent(); got != "" {
		t.Errorf("nil content: got %q, want empty", got)
	}
}

func TestParseGeminiJSONLSession(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"sessionId":"66c97d5d-48a6-4386-937d-29dfbe5caaa8","projectHash":"abc","startTime":"2026-05-10T18:32:15.435Z","lastUpdated":"2026-05-10T18:32:15.435Z","kind":"main"}`,
		`{"id":"u1","timestamp":"2026-05-10T18:32:16.508Z","type":"user","content":[{"text":"hello from jsonl"}]}`,
		`{"$set":{"lastUpdated":"2026-05-10T18:32:23.150Z"}}`,
		`{"id":"g1","timestamp":"2026-05-10T18:32:23.150Z","type":"gemini","content":"hi"}`,
	}, "\n"))

	sf, err := parseGeminiJSONLSession(data)
	if err != nil {
		t.Fatalf("parseGeminiJSONLSession() error = %v", err)
	}
	if sf.SessionID != "66c97d5d-48a6-4386-937d-29dfbe5caaa8" {
		t.Fatalf("SessionID = %q", sf.SessionID)
	}
	if sf.Kind != "main" {
		t.Fatalf("Kind = %q", sf.Kind)
	}
	if got := len(sf.Messages); got != 2 {
		t.Fatalf("len(Messages) = %d", got)
	}
	if got := extractSessionSummary(sf); got != "hello from jsonl" {
		t.Fatalf("summary = %q", got)
	}
	if sf.LastUpdated.Format(time.RFC3339) != "2026-05-10T18:32:23Z" {
		t.Fatalf("LastUpdated = %s", sf.LastUpdated.Format(time.RFC3339))
	}
}

func TestComputeLineDiff(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new_ string
		want string
	}{
		{
			"single line fully different",
			"foo", "bar",
			"- foo\n+ bar",
		},
		{
			"common prefix and suffix",
			"func foo() {\n    return 1\n}",
			"func foo() {\n    return 2\n}",
			"  func foo() {\n-     return 1\n+     return 2\n  }",
		},
		{
			"large context ellipsis",
			"a\nb\nc\nd\nold\ne\nf\ng\nh",
			"a\nb\nc\nd\nnew\ne\nf\ng\nh",
			"  ...\n  d\n- old\n+ new\n  e\n  ...",
		},
		{
			"common prefix only",
			"header\nold1\nold2",
			"header\nnew1",
			"  header\n- old1\n- old2\n+ new1",
		},
		{
			"common suffix only",
			"old1\nfooter",
			"new1\nnew2\nfooter",
			"- old1\n+ new1\n+ new2\n  footer",
		},
		{
			"identical",
			"same\nlines",
			"same\nlines",
			"", // prefixLen covers all lines, no diff
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeLineDiff(tt.old, tt.new_)
			if got != tt.want {
				t.Errorf("computeLineDiff:\n  old=%q\n  new=%q\n  got=%q\n  want=%q", tt.old, tt.new_, got, tt.want)
			}
		})
	}
}

func TestGeminiSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	s, err := newGeminiSession(context.Background(), "echo", "/tmp", "", "default", core.ContinueSession, nil, 0)
	if err != nil {
		t.Fatalf("newGeminiSession: %v", err)
	}
	defer s.Close()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}
