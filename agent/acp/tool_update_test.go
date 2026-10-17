package acp

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestToolCallUpdates_DevinSequence replays the exact tool_call /
// tool_call_update sequence Devin sends for one shell command. The
// production progress card showed the result twice (terminal preview + exit
// update carry the same output) and titled it with the raw toolCallId
// because updates carry no title.
func TestToolCallUpdates_DevinSequence(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	const id = "7e65ab4f9#cf5ee50092374606977e4a40e896c554"
	for _, u := range []string{
		`{"sessionUpdate":"tool_call","toolCallId":"` + id + `","title":"Ran echo","kind":"execute","rawInput":{"command":"echo display-test"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"` + id + `","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"` + id + `","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"display-test"}}],"_meta":{"cognition.ai/terminalPreview":true}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"` + id + `","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"display-test"}}],"_meta":{"terminal_exit":{"exit_code":0}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"` + id + `","status":"completed"}`,
	} {
		s.onNotification("session/update", []byte(`{"sessionId":"test-session-id","update":`+u+`}`))
	}
	var results []core.Event
	for len(s.events) > 0 {
		if ev := <-s.events; ev.Type == core.EventToolResult {
			results = append(results, ev)
		}
	}
	if len(results) != 1 {
		t.Fatalf("got %d tool results, want 1: %+v", len(results), results)
	}
	if results[0].ToolName != "Ran echo" || results[0].Content != "display-test" {
		t.Fatalf("tool result = %q / %q, want \"Ran echo\" / \"display-test\"", results[0].ToolName, results[0].Content)
	}
}
