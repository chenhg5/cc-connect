package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func cleanupExecutionStream(t *testing.T, c *queryCardStream) {
	t.Helper()
	t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.done = true
		if c.flushTimer != nil {
			c.flushTimer.Stop()
			c.flushTimer = nil
		}
	})
}

func TestExecutionFinalizationCancelsPendingFlush(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	ctx := context.Background()
	if err := c.UpdateExecution(ctx, []core.ExecutionBlock{{Text: "First"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateExecution(ctx, []core.ExecutionBlock{{Text: "Partial"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishExecution(ctx, []core.ExecutionBlock{{Text: "Retained progress", Done: true}}, "Final answer", core.CardStatusDone); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	timer := c.flushTimer
	c.mu.Unlock()
	if timer != nil {
		t.Fatal("finalization left a delayed writer alive")
	}
	if strings.Contains(f.shown("om_1"), "Partial") || !strings.Contains(f.shown("om_2"), "Final answer") {
		t.Fatal("pending chunk replaced authoritative conclusion")
	}
}

func TestExecutionFlushesLastChunkWhileAgentIsStillRunning(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, s := range []string{"", "Checking", "Checking the code"} {
		if err := c.UpdateExecution(ctx, []core.ExecutionBlock{{Text: s}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.shown("om_1"), "Checking the code") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("last public chunk never becomes visible without another agent event")
}
