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

func TestHandleMessage_DeltaEmitsEventTextImmediately(t *testing.T) {
	gs := &geminiSession{
		events: make(chan core.Event, 64),
		ctx:    context.Background(),
	}

	// Delta message should emit EventText immediately
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
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != core.EventText || events[0].Content != "Hello " {
		t.Errorf("event 0: expected EventText 'Hello ', got %v %q", events[0].Type, events[0].Content)
	}
	if events[1].Type != core.EventText || events[1].Content != "world!" {
		t.Errorf("event 1: expected EventText 'world!', got %v %q", events[1].Type, events[1].Content)
	}

	// No pending messages should be buffered
	if len(gs.pendingMsgs) != 0 {
		t.Errorf("expected 0 pending messages, got %d", len(gs.pendingMsgs))
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
		"type":      "tool_use",
		"tool_name": "shell",
		"tool_id":   "t1",
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

	// Expected sequence: EventThinking, EventToolUse, EventToolResult, EventText, EventText, EventResult
	if len(events) != 6 {
		var types []string
		for _, e := range events {
			types = append(types, string(e.Type))
		}
		t.Fatalf("expected 6 events, got %d: %v", len(events), types)
	}

	expects := []struct {
		typ     core.EventType
		content string
	}{
		{core.EventThinking, "Let me look at the files."},
		{core.EventToolUse, ""},
		{core.EventToolResult, ""},
		{core.EventText, "Here are "},
		{core.EventText, "the files."},
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

func TestComputeLineDiff(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new_     string
		want     string
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
