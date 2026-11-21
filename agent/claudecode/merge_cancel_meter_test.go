package claudecode

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMeteredClaudeResultRetainsCancellationConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	transcript, children := sessionTranscript(t)
	appendLines(t, filepath.Join(children, "agent-child.jsonl"),
		sidechainLineJSON("merge-child", "fixture-model", 10, 5, 0, 2))
	waiter := make(chan struct{})
	cs := &claudeSession{
		ctx:                  context.Background(),
		events:               make(chan core.Event, 4),
		sidechain:            newSidechainMeter(),
		dataDir:              t.TempDir(),
		transcriptOverride:   transcript,
		cancelConfirmTimeout: defaultCancelConfirmTimeout,
		cancelWaiters:        map[chan struct{}]struct{}{waiter: {}},
	}
	cs.sessionID.Store("sess-1")
	cs.turnActive.Store(true)

	cs.handleResult(map[string]any{"type": "result", "subtype": "compact"})
	compaction := <-cs.events
	if compaction.Sidechain != nil || !cs.turnActive.Load() {
		t.Fatal("compaction ended or billed the active turn")
	}
	select {
	case <-waiter:
		t.Fatal("compaction incorrectly confirmed cancellation")
	default:
	}

	cs.handleResult(map[string]any{"type": "result", "subtype": "error_during_execution",
		"session_id": "sess-1", "usage": map[string]any{"input_tokens": float64(20), "output_tokens": float64(7)}})
	event := <-cs.events
	select {
	case <-waiter:
	default:
		t.Fatal("metered final result did not confirm cancellation")
	}
	if cs.turnActive.Load() || !event.Done || event.Sidechain == nil || event.Sidechain.Requests != 1 {
		t.Fatalf("cancellation or descendant usage lost: %+v", event)
	}
	if event.InputTokens != 20 || event.OutputTokens != 7 {
		t.Fatalf("parent usage changed: %+v", event)
	}

	cs.handleResult(map[string]any{"type": "result", "result": "next", "session_id": "sess-1"})
	if next := <-cs.events; next.Sidechain != nil {
		t.Fatalf("descendant history billed again: %+v", next.Sidechain)
	}
}
