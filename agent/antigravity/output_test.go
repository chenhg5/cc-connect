package antigravity

import (
	"encoding/json"
	"strings"
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
	if len(events) != 0 {
		t.Fatalf("response events = %+v, want buffered text until classification", events)
	}

	_, events = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"pwd"}}}}`))
	if len(events) != 2 || events[0].Type != core.EventThinking || events[0].Content != "Working" || events[1].Type != core.EventToolUse {
		t.Fatalf("tool start events = %+v, want commentary then tool-use", events)
	}
	if events[1].ToolName != "run_command" || events[1].ToolInput != "pwd" {
		t.Fatalf("tool event = %+v, want run_command with pwd input", events[1])
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

	_, events = parser.parseLine([]byte(`{"event":"result","result":{"conversation_id":"session-1","status":"SUCCESS","response":"Done"}}`))
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "Done" {
		t.Fatalf("final result = %+v, want answer after commentary", events)
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

func TestAgyOutputParser_ErrorWithAndWithoutResponse(t *testing.T) {
	// Case 1: Response non-empty, error present -> response is kept, error is NOT emitted as EventError
	parser1 := newAgyOutputParser()
	_, events1 := parser1.parseLine([]byte(`{"event":"result","result":{"status":"ERROR","response":"Here is what I found","error":"Some tool warning"}}`))
	if len(events1) != 1 || events1[0].Type != core.EventText || events1[0].Content != "Here is what I found" {
		t.Fatalf("expected EventText with response, got %+v", events1)
	}

	// Case 2: Response empty, error present -> EventError is emitted
	parser2 := newAgyOutputParser()
	_, events2 := parser2.parseLine([]byte(`{"event":"result","result":{"status":"ERROR","response":"","error":"Failed to execute"}}`))
	if len(events2) != 1 || events2[0].Type != core.EventError || events2[0].Error == nil || !strings.Contains(events2[0].Error.Error(), "Failed to execute") {
		t.Fatalf("expected EventError with error message, got %+v", events2)
	}
}

func TestAgyOutputParser_CommentaryBeforeToolDoesNotEnterFinalAnswer(t *testing.T) {
	p := newAgyOutputParser()
	for _, delta := range []string{"Checking ", "the build"} {
		line, _ := json.Marshal(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 0, "state": "ACTIVE", "step_type": "agent_response", "text_delta": delta}})
		_, events := p.parseLine(line)
		if len(events) != 0 {
			t.Fatalf("unclassified response entered final text before tool boundary: %+v", events)
		}
	}
	_, events := p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command"}}`))
	if len(events) != 2 || events[0].Type != core.EventThinking || events[0].Content != "Checking the build" || events[1].Type != core.EventToolUse {
		t.Fatalf("tool boundary = %+v, want coalesced commentary before tool", events)
	}
	_, events = p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"Build passed"}}`))
	if len(events) != 0 {
		t.Fatalf("answer should wait for classification: %+v", events)
	}
	_, events = p.parseLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"Build passed"}}`))
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "Build passed" {
		t.Fatalf("final response = %+v, want final text without commentary", events)
	}
}

func TestAgyOutputParser_ResultFallbackAfterCommentary(t *testing.T) {
	p := newAgyOutputParser()
	_, _ = p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"Working"}}`))
	_, _ = p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","tool_name":"run_command"}}`))
	_, events := p.parseLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"Final answer"}}`))
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "Final answer" {
		t.Fatalf("result-only final answer lost after commentary: %+v", events)
	}
}

func TestAgyOutputParser_LateToolResultDoesNotConsumeFinalAnswer(t *testing.T) {
	p := newAgyOutputParser()
	_, _ = p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":2,"step_type":"agent_response","text_delta":"Final answer"}}`))
	_, events := p.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command"}}`))
	for _, event := range events {
		if event.Type == core.EventThinking {
			t.Fatalf("late tool reclassified final answer: %+v", events)
		}
	}
	events = p.flushResponse(core.EventText)
	if len(events) != 1 || events[0].Content != "Final answer" {
		t.Fatalf("EOF lost final answer: %+v", events)
	}
	if events = p.flushResponse(core.EventText); len(events) != 0 {
		t.Fatalf("EOF duplicated final: %+v", events)
	}
}
