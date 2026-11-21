package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// This fixture exercises the real RPC request/response path entirely in memory.
// It does not launch Codex or require an account, process, or network.
type mergeSteerWriter struct{ writes chan []byte }

func (w *mergeSteerWriter) Write(p []byte) (int, error) {
	w.writes <- append([]byte(nil), p...)
	return len(p), nil
}
func (*mergeSteerWriter) Close() error { return nil }

func TestMergedSteerUsageSettlement(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "completed", "new-turn", "transport-first", "transport-after"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &mergeSteerWriter{writes: make(chan []byte, 4)}
			s := &appServerSession{
				ctx: ctx, stdin: w, events: make(chan core.Event, 8),
				pending: make(map[int64]chan rpcResponseEnvelope), currentTurn: "old",
			}
			s.threadID.Store("thread")
			s.alive.Store(true)
			s.turnTokens.newThread()
			s.turnTokens.beginTurn()
			if scenario == "failure" {
				s.pendingMsgs = []string{"buffered answer"}
			}
			old := codexTokenUsage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 5}
			s.turnTokens.observe(old, old)
			// Empty root keeps descendant metering entirely in memory.
			s.sidechain.observe("child", codexTokenUsage{InputTokens: 10, CachedInputTokens: 2, OutputTokens: 1})
			done := make(chan error, 1)
			go func() { done <- s.steerTurn("thread", "old", nil) }()
			id := mergeSteerRequest(t, w, "turn/steer")
			switch scenario {
			case "completed", "new-turn":
				s.completeTurn()
				mergeSteerEvent(t, s, core.EventResult, 80, 20, 5)
				if scenario == "new-turn" {
					mergeSteerStart(t, s, w)
				}
			case "transport-first":
				s.finishTransport(io.EOF)
			}
			if scenario == "success" {
				s.handleResponse(rpcResponseEnvelope{ID: id, Result: json.RawMessage(`{"turnId":"old"}`)})
			} else {
				s.handleResponse(rpcResponseEnvelope{ID: id, Error: &rpcError{Code: -32000, Message: "steer failed"}})
			}
			err := mergeSteerAwait(t, done)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("steer error = %v, scenario %s", err, scenario)
			}
			if scenario == "failure" {
				s.stateMu.Lock()
				preserved := len(s.pendingMsgs) == 1 && s.pendingMsgs[0] == "buffered answer"
				s.stateMu.Unlock()
				if !preserved {
					t.Fatal("usage retirement discarded the buffered answer")
				}
			}
			switch scenario {
			case "success":
				mergeSteerNoEvent(t, s)
				s.completeTurn()
				mergeSteerEvent(t, s, core.EventResult, 80, 20, 5)
			case "failure", "transport-after", "transport-first":
				mergeSteerEvent(t, s, core.EventError, 80, 20, 5)
				if scenario == "transport-after" {
					s.finishTransport(io.EOF)
				}
				// Late completion cannot settle the retired turn again.
				s.completeTurn()
				if scenario == "failure" {
					mergeSteerStart(t, s, w)
				}
			}
			mergeSteerNoEvent(t, s)
			if scenario == "failure" || scenario == "new-turn" {
				s.stateMu.Lock()
				if s.currentTurn != "new" || !s.turnTokens.active {
					s.stateMu.Unlock()
					t.Fatal("late steering error retired the new turn")
				}
				s.turnTokens.observe(codexTokenUsage{InputTokens: 140, CachedInputTokens: 30, OutputTokens: 8}, codexTokenUsage{})
				s.stateMu.Unlock()
				s.completeTurn()
				mergeSteerEvent(t, s, core.EventResult, 30, 10, 3)
				mergeSteerNoEvent(t, s)
			}
		})
	}
}

func mergeSteerRequest(t *testing.T, w *mergeSteerWriter, method string) int64 {
	t.Helper()
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	select {
	case data := <-w.writes:
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-memory RPC request timed out")
	}
	if req.Method != method {
		t.Fatalf("method = %q, want %q", req.Method, method)
	}
	return req.ID
}

func mergeSteerAwait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("in-memory RPC response timed out")
		return nil
	}
}

func mergeSteerStart(t *testing.T, s *appServerSession, w *mergeSteerWriter) {
	t.Helper()
	s.stateMu.Lock()
	revision := s.turnRevision
	s.stateMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.startTurn("thread", nil, revision) }()
	id := mergeSteerRequest(t, w, "turn/start")
	s.handleResponse(rpcResponseEnvelope{ID: id, Result: json.RawMessage(`{"turn":{"id":"new"}}`)})
	if err := mergeSteerAwait(t, done); err != nil {
		t.Fatal(err)
	}
}

func mergeSteerEvent(t *testing.T, s *appServerSession, kind core.EventType, input, cached, output int) {
	t.Helper()
	select {
	case event := <-s.events:
		if event.Type != kind || event.InputTokens != input || event.CacheReadInputTokens != cached || event.OutputTokens != output {
			t.Fatalf("unexpected settled event: %+v", event)
		}
		if input == 80 {
			if event.Sidechain == nil || event.Sidechain.InputTokens != 8 || event.Sidechain.CacheReadInputTokens != 2 || event.Sidechain.OutputTokens != 1 {
				t.Fatalf("missing descendant settlement: %+v", event.Sidechain)
			}
		} else if event.Sidechain != nil {
			t.Fatalf("descendant usage billed again: %+v", event.Sidechain)
		}
		if kind == core.EventError && event.Error == nil {
			t.Fatal("error event has no diagnostic")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settled event timed out")
	}
}

func mergeSteerNoEvent(t *testing.T, s *appServerSession) {
	t.Helper()
	select {
	case event := <-s.events:
		t.Fatalf("unexpected extra event: %+v", event)
	default:
	}
}
