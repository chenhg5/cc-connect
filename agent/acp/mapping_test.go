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

func TestMapSessionUpdate_agentMessageChunk_AlternativeFormats(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{
			name: "standard format with type",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "agent_message_chunk",
					"content": {"type": "text", "text": "hello world"}
				}
			}`,
			want: "hello world",
		},
		{
			name: "content.text without type",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "agent_message_chunk",
					"content": {"text": "alt format 1"}
				}
			}`,
			want: "alt format 1",
		},
		{
			name: "top-level text field",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "agent_message_chunk",
					"text": "alt format 2"
				}
			}`,
			want: "alt format 2",
		},
		{
			name: "content as string",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "agent_message_chunk",
					"content": "alt format 3"
				}
			}`,
			want: "alt format 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := mapSessionUpdate("", json.RawMessage(tt.params))
			if len(evs) != 1 {
				t.Fatalf("expected 1 event, got %d", len(evs))
			}
			if evs[0].Type != core.EventText {
				t.Fatalf("expected EventText, got %v", evs[0].Type)
			}
			if evs[0].Content != tt.want {
				t.Fatalf("content = %q, want %q", evs[0].Content, tt.want)
			}
		})
	}
}

func TestMapSessionUpdate_FallbackTextExtraction(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{
			name: "message_chunk vendor extension",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "message_chunk",
					"text": "vendor text"
				}
			}`,
			want: "vendor text",
		},
		{
			name: "response vendor extension",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "response",
					"message": "vendor message"
				}
			}`,
			want: "vendor message",
		},
		{
			name: "unknown kind with content.text",
			params: `{
				"sessionId": "s1",
				"update": {
					"sessionUpdate": "custom_output",
					"content": {"text": "fallback text"}
				}
			}`,
			want: "fallback text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := mapSessionUpdate("", json.RawMessage(tt.params))
			if len(evs) != 1 {
				t.Fatalf("expected 1 event, got %d", len(evs))
			}
			if evs[0].Type != core.EventText {
				t.Fatalf("expected EventText, got %v", evs[0].Type)
			}
			if evs[0].Content != tt.want {
				t.Fatalf("content = %q, want %q", evs[0].Content, tt.want)
			}
		})
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
