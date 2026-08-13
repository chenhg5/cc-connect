package dsh

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// collectAgg creates an aggregator that records emissions (mutex-guarded —
// timer flushes run on their own goroutine).
type aggCollector struct {
	mu  sync.Mutex
	out []string
}

func (c *aggCollector) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.out = append(c.out, s)
}

func (c *aggCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.out)
}

func (c *aggCollector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.out...)
}

func collectAgg(minChars int, maxWait time.Duration) (*chunkAggregator, *aggCollector) {
	c := &aggCollector{}
	agg := newChunkAggregator(minChars, maxWait, c.add)
	return agg, c
}

func TestChunkAggregator_SizeFlush(t *testing.T) {
	agg, c := collectAgg(40, 0) // no timer: only size flush
	// 10 deltas × 5 runes = 50 ≥ 40 → single flush at the 8th delta.
	for i := 0; i < 10; i++ {
		agg.Append("word ")
	}
	if c.count() != 1 {
		t.Fatalf("expected 1 flush, got %d: %v", c.count(), c.all())
	}
	if c.all()[0] != strings.Repeat("word ", 8) {
		t.Fatalf("flush content mismatch: %q", c.all()[0])
	}
	// Remaining 10 runes stay buffered until explicit flush.
	agg.Flush()
	if c.count() != 2 || c.all()[1] != strings.Repeat("word ", 2) {
		t.Fatalf("expected tail flush, got %v", c.all())
	}
	// Flush with empty buffer emits nothing.
	agg.Flush()
	if c.count() != 2 {
		t.Fatalf("empty flush must not emit, got %v", c.all())
	}
}

func TestChunkAggregator_BoundaryFlush(t *testing.T) {
	agg, c := collectAgg(100, 0) // no timer, high threshold: boundary drives flush
	agg.Append("你好")
	if c.count() != 0 {
		t.Fatal("no flush expected yet")
	}
	agg.Append("。")
	if c.count() != 1 || c.all()[0] != "你好。" {
		t.Fatalf("boundary flush mismatch: %v", c.all())
	}
}

func TestChunkAggregator_TimerFlush(t *testing.T) {
	agg, c := collectAgg(1000, 60*time.Millisecond)
	agg.Append("short")
	deadline := time.After(2 * time.Second)
	for c.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("timer flush never fired")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if c.all()[0] != "short" {
		t.Fatalf("timer flush content mismatch: %q", c.all()[0])
	}
	// A second small append arms a fresh timer.
	agg.Append("more")
	deadline2 := time.After(2 * time.Second)
	for c.count() < 2 {
		select {
		case <-deadline2:
			t.Fatal("second timer flush never fired")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestChunkAggregator_Cancel(t *testing.T) {
	agg, c := collectAgg(1000, 50*time.Millisecond)
	agg.Append("held")
	agg.Cancel()
	time.Sleep(150 * time.Millisecond)
	if c.count() != 0 {
		t.Fatalf("cancel must suppress the timer flush, got %v", c.all())
	}
	// Explicit flush still works after cancel (session boundaries call it).
	agg.Flush()
	if c.count() != 1 || c.all()[0] != "held" {
		t.Fatalf("flush after cancel mismatch: %v", c.all())
	}
}

// TestSession_TinyDeltasCoalesce verifies that token-level deltas surface as
// aggregated EventText chunks rather than one event per token, and that all
// text still arrives at turn end.
func TestSession_TinyDeltasCoalesce(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("hi", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	words := []string{"今天", "天气", "真", "不错", "，", "适合", "出去", "走走", "。"}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	for i, w := range words {
		m.broadcast("rpc-2-"+string(rune('a'+i)), frameEvent(id, evTextDelta(1, 1, w)))
	}
	m.broadcast("rpc-3", frameEvent(id, evTurnEnd(1, "completed")))

	events := drainEvents(t, ds, 5*time.Second)

	var text strings.Builder
	var textEvents int
	for _, e := range events {
		if e.Type == core.EventText {
			textEvents++
			text.WriteString(e.Content)
		}
	}
	if text.String() != strings.Join(words, "") {
		t.Fatalf("aggregated text mismatch: %q", text.String())
	}
	// 9 token deltas must NOT surface as 9 separate EventTexts (sentence
	// boundary + size thresholds coalesce them).
	if textEvents > 4 {
		t.Fatalf("expected coalesced EventText chunks, got %d", textEvents)
	}
}
