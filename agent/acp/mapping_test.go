package acp

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMapSessionUpdate_agentMessageChunk(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "agent_message_chunk",
			"content": {"type": "text", "text": "hello"}
		}
	}`)
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 || evs[0].Type != core.EventText || evs[0].Content != "hello" {
		t.Fatalf("got %+v", evs)
	}
}

func TestMapSessionUpdate_toolCallUpdate_inProgress(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "tool_call_update",
			"toolCallId": "c1",
			"title": "Run",
			"status": "in_progress",
			"content": [
				{"type": "content", "content": {"type": "text", "text": "partial output"}}
			]
		}
	}`)
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 || evs[0].Type != core.EventToolResult || evs[0].ToolName != "Run" {
		t.Fatalf("got %+v", evs)
	}
}

func TestMapSessionUpdate_reasoningChunk(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "reasoning_chunk",
			"content": {"type": "text", "text": "step 1"}
		}
	}`)
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 || evs[0].Type != core.EventThinking || evs[0].Content != "step 1" {
		t.Fatalf("got %+v", evs)
	}
}

func TestMapSessionUpdate_toolCall(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "tool_call",
			"toolCallId": "c1",
			"title": "Read file",
			"kind": "read",
			"status": "pending"
		}
	}`)
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 || evs[0].Type != core.EventToolUse || evs[0].ToolName != "Read file" {
		t.Fatalf("got %+v", evs)
	}
}

func TestPickPermissionOptionID(t *testing.T) {
	opts := []permissionOption{
		{OptionID: "a", Kind: "allow_once"},
		{OptionID: "r", Kind: "reject_once"},
	}
	if pickPermissionOptionID(true, opts) != "a" {
		t.Fatal("allow")
	}
	if pickPermissionOptionID(false, opts) != "r" {
		t.Fatal("deny")
	}
}

func TestBuildPermissionResult(t *testing.T) {
	allow := buildPermissionResult(true, "opt1")
	if allow["outcome"].(map[string]any)["optionId"] != "opt1" {
		t.Fatalf("%v", allow)
	}
	denySel := buildPermissionResult(false, "rej")
	if denySel["outcome"].(map[string]any)["optionId"] != "rej" {
		t.Fatalf("%v", denySel)
	}
	cancel := buildPermissionResult(false, "")
	if cancel["outcome"].(map[string]any)["outcome"] != "cancelled" {
		t.Fatalf("%v", cancel)
	}
}

func TestMapSessionUpdate_toolCall_withRawInput(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {
			"sessionUpdate": "tool_call",
			"toolCallId": "c1",
			"title": "Bash",
			"kind": "bash",
			"status": "pending",
			"rawInput": {"command": "ls -la /tmp"}
		}
	}`)
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].Type != core.EventToolUse {
		t.Fatalf("expected EventToolUse, got %s", evs[0].Type)
	}
	if evs[0].ToolInput != "ls -la /tmp" {
		t.Fatalf("expected tool input 'ls -la /tmp', got %q", evs[0].ToolInput)
	}
}

func TestSummarizeACPToolInput(t *testing.T) {
	tests := []struct {
		name string
		kind string
		raw  string
		want string
	}{
		{
			name: "bash command",
			kind: "bash",
			raw:  `{"command": "echo hello"}`,
			want: "echo hello",
		},
		{
			name: "read file",
			kind: "read",
			raw:  `{"file_path": "/tmp/test.txt"}`,
			want: "/tmp/test.txt",
		},
		{
			name: "empty raw",
			kind: "bash",
			raw:  "",
			want: "",
		},
		{
			name: "unknown kind falls back to formatted JSON",
			kind: "unknown_tool",
			raw:  `{"key": "value"}`,
			want: "{\n  \"key\": \"value\"\n}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.raw != "" {
				raw = json.RawMessage(tt.raw)
			}
			got := summarizeACPToolInput(tt.kind, raw)
			if got != tt.want {
				t.Errorf("summarizeACPToolInput(%q, %s) = %q, want %q", tt.kind, tt.raw, got, tt.want)
			}
		})
	}
}
