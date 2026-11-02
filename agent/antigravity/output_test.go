package antigravity

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgyOutputParser_MapsResponseAndToolSteps(t *testing.T) {
	parser := newAgyOutputParser()

	_, events := parser.parseLine([]byte(`{"event":"init","conversation_id":"session-1"}`))
	if len(events) != 1 || events[0].Type != core.EventText || events[0].SessionID != "session-1" {
		t.Fatalf("init events = %+v, want session ID event", events)
	}

	_, events = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"agent_response","text_delta":"Working"}}`))
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "Working" {
		t.Fatalf("response events = %+v, want streamed text", events)
	}

	_, events = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"pwd"}}}}`))
	if len(events) != 1 || events[0].Type != core.EventToolUse {
		t.Fatalf("tool start events = %+v, want one tool-use event", events)
	}
	if events[0].ToolName != "run_command" || events[0].ToolInput != "pwd" {
		t.Fatalf("tool event = %+v, want run_command with pwd input", events[0])
	}

	_, events = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"pwd"},"output":"/workspace\n"}}}`))
	if len(events) != 1 || events[0].Type != core.EventToolResult {
		t.Fatalf("tool completion events = %+v, want one tool-result event", events)
	}
	if events[0].ToolResult != "/workspace\n" || events[0].ToolStatus != "completed" || events[0].ToolSuccess == nil || !*events[0].ToolSuccess {
		t.Fatalf("tool result = %+v, want successful result", events[0])
	}

	_, events = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"output":"duplicate"}}}`))
	if len(events) != 1 || events[0].Type != core.EventToolResult {
		t.Fatalf("repeated tool completion events = %+v, want result only", events)
	}

	_, events = parser.parseLine([]byte(`{"event":"result","result":{"conversation_id":"session-1","status":"SUCCESS","response":"Working"}}`))
	if len(events) != 0 {
		t.Fatalf("final result duplicated streamed text: %+v", events)
	}
}

func TestAgyOutputParser_UsesResultResponseWhenNoDeltas(t *testing.T) {
	parser := newAgyOutputParser()
	recognized, events := parser.parseLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"final answer"}}`))
	if !recognized || len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "final answer" {
		t.Fatalf("parsed result = recognized %v, events %+v", recognized, events)
	}
}

func TestAgyOutputParser_PlainTextFallback(t *testing.T) {
	parser := newAgyOutputParser()
	recognized, events := parser.parseLine([]byte("plain response"))
	if recognized || len(events) != 0 {
		t.Fatalf("plain line = recognized %v, events %+v", recognized, events)
	}
}
