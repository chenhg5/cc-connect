package claudecode

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// nopWriteCloser is a writable stdin stand-in for tests.
type nopWriteCloser struct{ w io.Writer }

func (n nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (nopWriteCloser) Close() error                  { return nil }

// TestHandleResultReportsTriggerSource verifies that the adapter reports the
// trigger of a turn, as far as the CLI's stream reveals it: a background task
// completion notification, or output produced inside a subagent sidechain.
// Core cannot know either of these on its own (issue #1803).
func TestHandleResultReportsTriggerSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("background task completion", func(t *testing.T) {
		cs := &claudeSession{
			events: make(chan core.Event, 8),
			ctx:    ctx,
		}
		cs.handleResult(map[string]any{
			"type":      "result",
			"result":    "background task done",
			"num_turns": float64(1),
			"origin":    map[string]any{"kind": "task-notification"},
		})

		evt := <-cs.events
		if evt.TriggerSource != core.TriggerSourceTaskNotification {
			t.Fatalf("TriggerSource = %q, want %q", evt.TriggerSource, core.TriggerSourceTaskNotification)
		}
	})

	t.Run("subagent sidechain output", func(t *testing.T) {
		cs := &claudeSession{
			events: make(chan core.Event, 8),
			ctx:    ctx,
		}
		// Claude Code marks output produced inside a subagent with a
		// non-empty parent_tool_use_id.
		cs.handleReadLoopLine(`{"type":"assistant","parent_tool_use_id":"toolu_01","message":{"content":[]}}`)
		cs.handleResult(map[string]any{
			"type":      "result",
			"result":    "subagent finished",
			"num_turns": float64(1),
		})

		evt := <-cs.events
		if evt.TriggerSource != core.TriggerSourceSubagent {
			t.Fatalf("TriggerSource = %q, want %q", evt.TriggerSource, core.TriggerSourceSubagent)
		}
	})

	t.Run("no trigger observed", func(t *testing.T) {
		cs := &claudeSession{
			events: make(chan core.Event, 8),
			ctx:    ctx,
		}
		cs.handleResult(map[string]any{
			"type":      "result",
			"result":    "done",
			"num_turns": float64(1),
		})

		evt := <-cs.events
		if evt.TriggerSource != "" {
			t.Fatalf("TriggerSource = %q, want empty (core falls back)", evt.TriggerSource)
		}
	})
}

// TestSendResetsSidechainMarker verifies that starting a new user-initiated
// turn forgets the subagent activity of the previous turn, so the next result
// reports the trigger of the turn that is actually running.
func TestSendResetsSidechainMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
		stdin:  nopWriteCloser{&bytes.Buffer{}},
	}
	cs.alive.Store(true)
	cs.sidechainSeen.Store(true)

	if err := cs.Send("hello", "m1", nil, nil); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if cs.sidechainSeen.Load() {
		t.Fatal("a new user-initiated turn must reset the subagent marker")
	}
}

// TestTerminalResultConsumesSidechainMarker verifies that the subagent
// observation is scoped to the turn that made it: once a terminal result
// closes that turn, a later result — an unsolicited one in particular, which
// never goes through Send() — must not be reported as subagent-triggered.
func TestTerminalResultConsumesSidechainMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.handleReadLoopLine(`{"type":"assistant","parent_tool_use_id":"toolu_01","message":{"content":[]}}`)
	cs.handleResult(map[string]any{
		"type":      "result",
		"result":    "subagent finished",
		"num_turns": float64(1),
	})

	evt := <-cs.events
	if evt.TriggerSource != core.TriggerSourceSubagent {
		t.Fatalf("TriggerSource = %q, want %q", evt.TriggerSource, core.TriggerSourceSubagent)
	}
	if !evt.Done {
		t.Fatal("a plain result must close the turn (Done = true)")
	}

	// The next turn is unrelated to the subagent that just finished.
	cs.handleResult(map[string]any{
		"type":      "result",
		"result":    "unrelated",
		"num_turns": float64(1),
	})

	evt = <-cs.events
	if evt.TriggerSource != "" {
		t.Fatalf("TriggerSource = %q, want empty: the subagent observation belongs to the turn that already ended", evt.TriggerSource)
	}
}

// TestCompactionResultKeepsSidechainMarker verifies that a mid-turn
// compaction result — which does not end the turn — leaves the subagent
// observation in place, so the turn's final result still reports it.
func TestCompactionResultKeepsSidechainMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.handleReadLoopLine(`{"type":"assistant","parent_tool_use_id":"toolu_01","message":{"content":[]}}`)
	cs.handleResult(map[string]any{
		"type":      "result",
		"result":    "compacted",
		"subtype":   "compact",
		"num_turns": float64(1),
	})

	evt := <-cs.events
	if evt.Done {
		t.Fatal("a compaction result must not close the turn (Done = false)")
	}
	if evt.TriggerSource != core.TriggerSourceSubagent {
		t.Fatalf("TriggerSource = %q, want %q", evt.TriggerSource, core.TriggerSourceSubagent)
	}
	if !cs.sidechainSeen.Load() {
		t.Fatal("a mid-turn compaction result must not consume the subagent observation")
	}

	// The turn's final result still reports the subagent trigger.
	cs.handleResult(map[string]any{
		"type":      "result",
		"result":    "subagent finished",
		"num_turns": float64(2),
	})

	evt = <-cs.events
	if !evt.Done {
		t.Fatal("the final result must close the turn (Done = true)")
	}
	if evt.TriggerSource != core.TriggerSourceSubagent {
		t.Fatalf("TriggerSource = %q, want %q", evt.TriggerSource, core.TriggerSourceSubagent)
	}
	if cs.sidechainSeen.Load() {
		t.Fatal("the terminal result must consume the subagent observation")
	}
}
