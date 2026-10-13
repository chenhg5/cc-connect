package core

import (
	"context"
	"testing"
	"time"
)

func TestNativeExecutionRunningResultIsNotCompleted(t *testing.T) {
	sink := &executionTestSink{}
	c := &nativeWorkflowCard{inner: sink, sink: sink}
	for _, event := range []Event{{Type: EventToolUse, ToolID: "a", ToolName: "Bash", ToolInput: "build"}, {Type: EventToolResult, ToolID: "a", ToolName: "Bash", ToolResult: "partial", ToolStatus: "running"}} {
		if err := c.ObserveExecution(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if sink.blocks[0].Done || sink.blocks[0].StartedAt.IsZero() {
		t.Fatal("running result marked complete or missing timing")
	}
	c.blocks[0].StartedAt = time.Now().Add(-200 * time.Millisecond)
	exit := 7
	if err := c.ObserveExecution(context.Background(), Event{Type: EventToolResult, ToolID: "a", ToolName: "Bash", ToolResult: "failed", ToolExitCode: &exit}); err != nil {
		t.Fatal(err)
	}
	b := sink.blocks[0]
	if !b.Done || b.Status != "failed" || b.DurationMS < 200 || b.ExitCode == nil || *b.ExitCode != 7 {
		t.Fatal("terminal status, timing or exit code lost")
	}
}
