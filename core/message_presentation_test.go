package core

import (
	"context"
	"testing"
)

func TestNativeFinalOnlyResponseDoesNotStartAnEmptyProcessCard(t *testing.T) {
	sink := &executionTestSink{}
	c := &nativeWorkflowCard{inner: sink, sink: sink, omitFinal: true}
	for _, text := range []string{"直接", "回答问题"} {
		if err := c.ObserveExecution(context.Background(), Event{Type: EventText, Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.blocks) != 0 {
		t.Fatal("final-only text created a process card that becomes empty at completion")
	}
	if err := c.Finalize(context.Background(), "直接回答问题"); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 0 || sink.answer != "" {
		t.Fatal("separate handoff duplicated in process card")
	}
}
