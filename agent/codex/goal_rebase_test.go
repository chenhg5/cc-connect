package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Both native Goals and ordinary chat must retain main's shared steering path,
// including its stale-turn recovery. No CLI, network, or account is used.
func TestAppServerGoalRebasePreservesSharedSteering(t *testing.T) {
	for _, goalActive := range []bool{false, true} {
		for _, failSteer := range []bool{false, true} {
			name := "ordinary"
			if goalActive {
				name = "goal"
			}
			if failSteer {
				name += "_stale"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w := &goalRPCWriter{writes: make(chan []byte, 4)}
				s := &appServerSession{
					ctx: ctx, stdin: w,
					pending: make(map[int64]chan rpcResponseEnvelope),
					events:  make(chan core.Event, 8),
					workDir: t.TempDir(), currentTurn: "active-turn",
				}
				s.threadID.Store("goal-rebase-thread")
				s.alive.Store(true)
				if goalActive {
					s.goal = &core.Goal{ThreadID: "goal-rebase-thread", Status: "active"}
				}
				done := make(chan error, 1)
				go func() { done <- s.Send("follow up", "", nil, nil) }()
				req := goalRebaseReadRequest(t, w)
				if req.Method != "turn/steer" || req.Params["threadId"] != "goal-rebase-thread" || req.Params["expectedTurnId"] != "active-turn" {
					t.Fatalf("unexpected steering request: %+v", req)
				}
				input, ok := req.Params["input"].([]any)
				if !ok || len(input) != 1 {
					t.Fatalf("unexpected steering input: %#v", req.Params["input"])
				}
				item, ok := input[0].(map[string]any)
				if !ok || item["text"] != "follow up" {
					t.Fatalf("follow-up text lost: %#v", input)
				}
				if failSteer {
					s.handleResponse(rpcResponseEnvelope{ID: req.ID, Error: &rpcError{Code: -32000, Message: "no active turn"}})
				} else {
					s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{"turnId":"active-turn"}`)})
				}
				err := goalRebaseAwait(t, done)
				if failSteer {
					if err == nil || !strings.Contains(err.Error(), "no active turn") {
						t.Fatalf("stale turn error = %v", err)
					}
					s.stateMu.Lock()
					turn, revision := s.currentTurn, s.turnRevision
					s.stateMu.Unlock()
					if turn != "" || revision != 1 {
						t.Fatalf("stale turn was not invalidated: %q revision=%d", turn, revision)
					}
					go func() { done <- s.Send("next message", "", nil, nil) }()
					next := goalRebaseReadRequest(t, w)
					if next.Method != "turn/start" || next.Params["threadId"] != "goal-rebase-thread" {
						t.Fatalf("next message did not restart normally: %+v", next)
					}
					s.handleResponse(rpcResponseEnvelope{ID: next.ID, Result: json.RawMessage(`{"turn":{"id":"new-turn"}}`)})
					if err := goalRebaseAwait(t, done); err != nil {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if s.GoalActive() != goalActive {
					t.Fatal("ordinary steering changed the native Goal state")
				}
			})
		}
	}
}

type goalRebaseRequest struct {
	ID     int64          `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func goalRebaseReadRequest(t *testing.T, w *goalRPCWriter) goalRebaseRequest {
	t.Helper()
	var req goalRebaseRequest
	select {
	case data := <-w.writes:
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-memory RPC")
	}
	return req
}

func goalRebaseAwait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("in-memory RPC did not return")
		return nil
	}
}
