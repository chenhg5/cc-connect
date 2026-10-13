package claudecode

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func newLineTestSession(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{events: make(chan core.Event, 32), ctx: ctx}
	cs.alive.Store(true)
	return cs
}

func drainResults(cs *claudeSession) []core.Event {
	var out []core.Event
	for {
		select {
		case evt := <-cs.events:
			if evt.Type == core.EventResult {
				out = append(out, evt)
			}
		default:
			return out
		}
	}
}

// Recorded from claude --resume after the previous process left a background
// shell running (2026-10-13 22:12, Discord showed "(空响应)" before the answer).
func TestResumeStartupTaskNotificationResultIsDropped(t *testing.T) {
	cs := newLineTestSession(t)
	for _, line := range []string{
		`{"type":"system","subtype":"task_notification","task_id":"b1","status":"stopped","session_id":"s1"}`,
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"result","subtype":"success","result":"","num_turns":0,"session_id":"s1","usage":{"input_tokens":0,"output_tokens":0}}`,
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"pong"}]},"session_id":"s1"}`,
		`{"type":"result","subtype":"success","result":"pong","num_turns":1,"session_id":"s1","usage":{"input_tokens":2,"output_tokens":4}}`,
	} {
		cs.handleReadLoopLine(line)
	}
	res := drainResults(cs)
	if len(res) != 1 || res[0].Content != "pong" || !res[0].Done {
		t.Fatalf("results = %+v, want only the real \"pong\" result", res)
	}
}

// A notification after start-up (a background task finishing mid-session) is
// not touched: whatever result follows is delivered as before.
func TestMidSessionTaskNotificationResultIsKept(t *testing.T) {
	cs := newLineTestSession(t)
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed","session_id":"s1"}`,
		`{"type":"result","subtype":"success","result":"","num_turns":0,"session_id":"s1"}`,
	} {
		cs.handleReadLoopLine(line)
	}
	if res := drainResults(cs); len(res) != 1 {
		t.Fatalf("results = %+v, want 1", res)
	}
}

func TestCompactionEventsCarryMarker(t *testing.T) {
	cs := newLineTestSession(t)
	cs.handleReadLoopLine(`{"type":"result","subtype":"compact","session_id":"s1"}`)
	cs.handleReadLoopLine(`{"type":"system","subtype":"compact_boundary","session_id":"s1"}`)
	n := 0
	for len(cs.events) > 0 {
		evt := <-cs.events
		if c, _ := evt.Metadata[core.MetaContextCompacted].(bool); c {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("compaction markers = %d, want 2", n)
	}
}
