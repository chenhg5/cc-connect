package antigravity

import (
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

// Captured from agy 1.5.3 print mode reading a file outside the workspace:
// the tool step ends in ERROR and the result carries an empty response plus
// denied_actions. Previously both were dropped and chat showed an empty reply.
func TestAgyOutputParser_SurfacesSoftDeniedToolConfirmation(t *testing.T) {
	parser := newAgyOutputParser()
	_, _ = parser.parseLine([]byte(`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"/home/u/outside.txt"}}}}`))

	_, events := parser.parseLine([]byte(`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"ERROR","step_type":"tool","tool_name":"read_file","duration_seconds":0.05,"tool_info":{"name":"read_file","parameters":{"Path":"/home/u/outside.txt"},"error":{"type":"TOOL_ERROR","message":"permission check failed for read_file \"/home/u/outside.txt\": user denied permission"}}}}`))
	if len(events) != 1 || events[0].Type != core.EventToolResult {
		t.Fatalf("ERROR tool step events = %+v, want one tool-result event", events)
	}
	if events[0].ToolStatus != "failed" || events[0].ToolSuccess == nil || *events[0].ToolSuccess || !strings.Contains(events[0].ToolResult, "permission check failed") {
		t.Fatalf("ERROR tool result = %+v, want failed result with denial message", events[0])
	}

	_, events = parser.parseLine([]byte(`{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"","num_turns":1,"denied_actions":[{"action":"read_file","display_name":"ReadFile"}]}}`))
	if len(events) != 1 || events[0].Type != core.EventText || !strings.Contains(events[0].Content, "ReadFile (/home/u/outside.txt)") {
		t.Fatalf("denied result events = %+v, want visible denial notice naming the target", events)
	}
}

// agy sometimes reports the soft-denied step as DONE with neither output nor
// error; the notice must still name the target.
func TestAgyOutputParser_DeniedNoticeWhenStepReportedDone(t *testing.T) {
	parser := newAgyOutputParser()
	_, _ = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"/home/u/.cc-connect/config.toml"}}}}`))
	_, _ = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"/home/u/.cc-connect/config.toml"}}}}`))
	_, events := parser.parseLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[{"action":"read_file","display_name":"ReadFile"}]}}`))
	if len(events) != 1 || !strings.Contains(events[0].Content, "ReadFile (/home/u/.cc-connect/config.toml)") {
		t.Fatalf("events = %+v, want notice naming the denied path", events)
	}
}

func TestAgyOutputParser_DeniedNoticeAppendsAfterText(t *testing.T) {
	parser := newAgyOutputParser()
	_, _ = parser.parseLine([]byte(`{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"agent_response","text_delta":"Let me check."}}`))
	_, events := parser.parseLine([]byte(`{"event":"result","result":{"status":"SUCCESS","response":"Let me check.","denied_actions":[{"action":"run_command","display_name":"RunCommand"},{"action":"run_command","display_name":"RunCommand"}]}}`))
	if len(events) != 1 || !strings.HasPrefix(events[0].Content, "\n\n") || strings.Count(events[0].Content, "RunCommand") != 1 {
		t.Fatalf("events = %+v, want one deduplicated notice separated from streamed text", events)
	}
}
