package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Exercise the real request writer, response dispatcher, active-turn matching,
// and pending-request cleanup. No provider credentials or model are involved.
func TestSteeringDelivery_OnlyExplicitRejectionAllowsRetry(t *testing.T) {
	for _, name := range []string{"accepted", "rejected", "invalid request", "server error", "wrong turn", "malformed", "timeout", "transport closed", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stdin := &lockedWriteCloser{}
			s := &appServerSession{ctx: ctx, stdin: stdin, currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
			s.threadID.Store("thread")
			type outcome struct {
				steered bool
				err     error
			}
			done := make(chan outcome, 1)
			go func() { ok, err := s.steerUserInput("follow up", 200*time.Millisecond); done <- outcome{ok, err} }()
			line := waitForWrittenJSONLine(t, stdin)
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
				Params struct {
					ExpectedTurnID string `json:"expectedTurnId"`
					ThreadID       string `json:"threadId"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != "turn/steer" || req.Params.ExpectedTurnID != "active" || req.Params.ThreadID != "thread" {
				t.Fatalf("request=%s", line)
			}
			switch name {
			case "accepted":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{"turnId":"active"}`)})
			case "invalid request":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Error: &rpcError{Code: -32600, Message: "no active turn to steer"}})
			case "rejected":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Error: &rpcError{Code: -32602, Message: "turn mismatch"}})
			case "server error":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Error: &rpcError{Code: -32603, Message: "internal server error"}})
			case "wrong turn":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{"turnId":"new"}`)})
			case "malformed":
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`not-json`)})
			case "transport closed":
				s.rejectPending(io.EOF)
			case "cancelled":
				cancel()
			}
			select {
			case result := <-done:
				if result.steered != (name == "accepted") {
					t.Fatalf("outcome=%+v", result)
				}
				if (result.err == nil) != (name == "accepted") {
					t.Fatalf("error=%v", result.err)
				}
				if errors.Is(result.err, core.ErrSteerRejected) != (name == "rejected" || name == "invalid request") {
					t.Fatalf("unsafe retry classification: %v", result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("request stuck")
			}
			s.pendingMu.Lock()
			pending := len(s.pending)
			s.pendingMu.Unlock()
			if pending != 0 {
				t.Fatalf("pending requests=%d", pending)
			}
			// A late ack cannot resurrect a completed waiter or start another turn.
			s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{"turnId":"active"}`)})
		})
	}
}

func TestSteeringDelivery_EmptyAndIdleDoNotWrite(t *testing.T) {
	stdin := &lockedWriteCloser{}
	s := &appServerSession{stdin: stdin}
	if ok, err := s.SteerUserInput("follow up"); ok || err != nil {
		t.Fatalf("idle=%v,%v", ok, err)
	}
	if ok, err := s.SteerUserInput(" "); ok || !errors.Is(err, core.ErrSteerRejected) {
		t.Fatalf("empty=%v,%v", ok, err)
	}
	if stdin.String() != "" {
		t.Fatal("unexpected RPC")
	}
}
