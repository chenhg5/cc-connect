package core

import (
	"context"
	"reflect"
	"testing"
)

func TestNativeExecutionSemanticCommandConsistency(t *testing.T) {
	for _, e := range []Event{
		{Type: EventToolUse, ToolID: "a", ToolName: "Bash", ToolInput: "printf hello"},
		{Type: EventToolUse, ToolID: "a", ToolName: "printf hello", ToolInput: "printf hello", ToolInputRaw: map[string]any{"command": "printf hello"}, Metadata: map[string]any{"tool_kind": "execute"}},
	} {
		sink := &executionTestSink{}
		c := &nativeWorkflowCard{inner: sink, sink: sink}
		if err := c.ObserveExecution(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		b := sink.blocks[0]
		if b.Tool != "command_execution" || b.Command != "printf hello" {
			t.Fatalf("inconsistent command rendering: %#v", b)
		}
	}
}

func TestCUJ_PublicOutputAdaptersShareOneExecutionPresentation(t *testing.T) {
	ctx := context.Background()
	sequences := [][]Event{
		{
			{Type: EventProgress, TextID: "message", Content: "Checking"},
			{Type: EventProgress, TextID: "message", Content: "Checking source"},
			{Type: EventToolUse, ToolID: "a", ToolName: "Bash", ToolInput: "printf hello"},
			{Type: EventToolResult, ToolID: "a", ToolResult: "he", ToolStatus: "in_progress", ToolResultDelta: true},
			{Type: EventToolResult, ToolID: "a", ToolResult: "hello", ToolStatus: "completed", ToolResultPresent: true},
		},
		{
			{Type: EventText, Content: "Checking"},
			{Type: EventText, Content: " source"},
			{Type: EventToolUse, ToolID: "a", ToolName: "printf hello", ToolInputRaw: map[string]any{"command": "printf hello"}, Metadata: map[string]any{"tool_kind": "execute"}},
			{Type: EventToolResult, ToolID: "a", ToolResult: "hello", ToolStatus: "in_progress", ToolUpdate: true, ToolResultPresent: true},
			{Type: EventToolResult, ToolID: "a", ToolStatus: "completed", ToolUpdate: true},
		},
	}
	var want []ExecutionBlock
	for _, events := range sequences {
		sink := &executionTestSink{}
		c := &nativeWorkflowCard{inner: sink, sink: sink}
		for _, e := range events {
			if err := c.ObserveExecution(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		_ = c.ObserveExecution(ctx, Event{Type: EventThinking, Content: "private"})
		_ = c.ObserveExecution(ctx, Event{Type: EventText, Content: "Final "})
		_ = c.ObserveExecution(ctx, Event{Type: EventText, Content: "answer"})
		if err := c.FinalizeWithAnswer(ctx, "", "Final answer"); err != nil {
			t.Fatal(err)
		}
		if len(sink.blocks) != 2 || sink.answer != "Final answer" {
			t.Fatalf("progress fragmented or final duplicated: %#v", sink)
		}
		for i := range sink.blocks {
			sink.blocks[i].ID = ""
			sink.blocks[i].StartedAt = ExecutionBlock{}.StartedAt
			sink.blocks[i].DurationMS = 0
		}
		if want == nil {
			want = sink.blocks
		} else if !reflect.DeepEqual(want, sink.blocks) {
			t.Fatalf("adapters differ: %#v vs %#v", want, sink.blocks)
		}
	}
}

func TestNativeExecutionStatusOnlyCompletionPreservesOutput(t *testing.T) {
	sink := &executionTestSink{}
	c := &nativeWorkflowCard{inner: sink, sink: sink}
	for _, e := range []Event{
		{Type: EventToolUse, ToolID: "a", ToolName: "Bash", ToolInput: "build"},
		{Type: EventToolResult, ToolID: "a", ToolResult: "build output", ToolStatus: "in_progress"},
		{Type: EventToolResult, ToolID: "a", ToolStatus: "completed"},
	} {
		if err := c.ObserveExecution(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if b := sink.blocks[0]; b.Output != "build output" || !b.Done {
		t.Fatalf("terminal update erased output: %#v", b)
	}
}
