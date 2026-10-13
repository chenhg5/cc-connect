package acp

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPToolUpdateKeepsOmittedFieldsAndRawOutput(t *testing.T) {
	for _, tc := range []struct {
		payload, status, text string
		present               bool
	}{
		{`{"toolCallId":"a","status":"completed"}`, "completed", "", false},
		{`{"toolCallId":"a","rawOutput":"完整结果"}`, "", "完整结果", true},
		{`{"toolCallId":"a","content":[]}`, "", "", true},
		{`{"toolCallId":"a","status":"failed","rawOutput":{"error":"failure"}}`, "failed", `{"error":"failure"}`, true},
	} {
		events := mapToolCallUpdate("s", json.RawMessage(tc.payload))
		if len(events) != 1 {
			t.Fatalf("missing update: %s", tc.payload)
		}
		e := events[0]
		if !e.ToolUpdate || e.ToolResultPresent != tc.present || e.ToolStatus != tc.status || e.Content != tc.text {
			t.Fatalf("lost patch semantics: %#v", e)
		}
	}
}

func TestACPThoughtChunkNeverBecomesPublicText(t *testing.T) {
	events := mapSessionUpdate("s", json.RawMessage(`{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"private"}}}`))
	if len(events) != 1 || events[0].Type != core.EventThinking {
		t.Fatalf("reasoning mapped to public output: %#v", events)
	}
}

func TestACPCompletedInitialCallKeepsResultAndProgrammaticName(t *testing.T) {
	events := mapToolCall("s", json.RawMessage(`{"toolCallId":"a","name":"shell","title":"printf hello","kind":"execute","status":"completed","rawInput":{"command":"printf hello"},"rawOutput":"hello"}`))
	if len(events) != 2 || events[0].ToolName != "shell" || events[1].Content != "hello" || events[1].ToolStatus != "completed" {
		t.Fatalf("lost initial result: %#v", events)
	}
}
