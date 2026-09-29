package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func newRPCRequestTestSession(t *testing.T) (*piSession, <-chan map[string]any) {
	t.Helper()
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	s := &piSession{ctx: ctx, cancel: cancel, rpc: true, rpcStdin: writer, events: make(chan core.Event, 128)}
	s.alive.Store(true)
	commands := make(chan map[string]any, 128)
	done := make(chan struct{})
	go func() {
		defer close(done)
		decoder := json.NewDecoder(reader)
		for {
			var cmd map[string]any
			if err := decoder.Decode(&cmd); err != nil {
				return
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = s.Close()
		_ = reader.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("RPC command reader did not exit")
		}
	})
	return s, commands
}

func receiveRPCCommand(t *testing.T, commands <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case cmd := <-commands:
		return cmd
	case <-time.After(3 * time.Second):
		t.Fatal("no RPC command received")
		return nil
	}
}

func receiveRPCSendResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("RPC Send did not return")
		return nil
	}
}

func assertNoRPCWaiters(t *testing.T, s *piSession) {
	t.Helper()
	s.rpcPendingMu.Lock()
	defer s.rpcPendingMu.Unlock()
	if len(s.rpcPending) != 0 {
		t.Fatalf("leaked %d RPC waiters", len(s.rpcPending))
	}
}

func TestSendRPC_WaitsForMatchingAcknowledgement(t *testing.T) {
	for _, outcome := range []string{"success", "rejected", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			s, commands := newRPCRequestTestSession(t)
			done := make(chan error, 1)
			go func() { done <- s.Send("supplement", "", nil, nil) }()
			cmd := receiveRPCCommand(t, commands)
			for _, response := range []map[string]any{
				{"type": "response", "command": "prompt", "id": "unrelated", "success": true},
				{"type": "response", "command": "get_state", "id": cmd["id"], "success": true},
			} {
				s.handleEvent(response)
			}
			select {
			case err := <-done:
				t.Fatalf("Send returned before matching acknowledgement: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			response := map[string]any{"type": "response", "command": "prompt", "id": cmd["id"]}
			if outcome != "malformed" {
				response["success"] = outcome == "success"
				response["error"] = "preflight rejected"
			}
			s.handleEvent(response)
			err := receiveRPCSendResult(t, done)
			if (err == nil) != (outcome == "success") {
				t.Fatalf("Send = %v for %s", err, outcome)
			}
			assertNoRPCWaiters(t, s)
			select {
			case event := <-s.Events():
				t.Fatalf("matched rejection must not terminate the running turn: %+v", event)
			default:
			}
		})
	}
}

func TestSendRPC_ConcurrentOutOfOrderAcknowledgements(t *testing.T) {
	s, commands := newRPCRequestTestSession(t)
	const count = 100
	type sendResult struct {
		message string
		err     error
	}
	done := make(chan sendResult, count)
	for i := range count {
		go func() {
			message := fmt.Sprintf("supplement-%d", i)
			done <- sendResult{message, s.Send(message, "", nil, nil)}
		}()
	}
	var batch []map[string]any
	ids := make(map[string]bool)
	wantErrors := make(map[string]bool)
	for range count {
		cmd := receiveRPCCommand(t, commands)
		id, _ := cmd["id"].(string)
		if id == "" || ids[id] || id == stateProbeID {
			t.Fatalf("invalid or reused prompt id: %q", id)
		}
		ids[id] = true
		batch = append(batch, cmd)
	}
	for i := len(batch) - 1; i >= 0; i-- {
		cmd := batch[i]
		message := cmd["message"].(string)
		wantErrors[message] = i%2 == 0
		s.handleEvent(map[string]any{
			"type": "response", "command": "prompt", "id": cmd["id"],
			"success": !wantErrors[message], "error": message,
		})
	}
	for range count {
		select {
		case result := <-done:
			if (result.err != nil) != wantErrors[result.message] ||
				(result.err != nil && !strings.Contains(result.err.Error(), result.message)) {
				t.Fatalf("response delivered to wrong sender: %+v", result)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Send did not finish")
		}
	}
	assertNoRPCWaiters(t, s)
}

func TestSendRPC_TimeoutAndCancelCleanWaiters(t *testing.T) {
	for _, reason := range []string{"timeout", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			s, commands := newRPCRequestTestSession(t)
			ctx, cancel := context.WithTimeout(s.ctx, 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.requestRPCPrompt(ctx, map[string]any{"type": "prompt", "message": "unacknowledged"}) }()
			receiveRPCCommand(t, commands)
			want := context.DeadlineExceeded
			if reason == "cancel" {
				cancel()
				want = context.Canceled
			}
			if err := receiveRPCSendResult(t, done); !errors.Is(err, want) {
				t.Fatalf("Send = %v, want %v", err, want)
			}
			assertNoRPCWaiters(t, s)
		})
	}
}

func TestSendRPC_CloseReleasesConcurrentWaiters(t *testing.T) {
	s, commands := newRPCRequestTestSession(t)
	const count = 100
	done := make(chan error, count)
	for range count {
		go func() { done <- s.Send("unacknowledged", "", nil, nil) }()
	}
	for range count {
		receiveRPCCommand(t, commands)
	}
	var closers sync.WaitGroup
	for range 4 {
		closers.Add(1)
		go func() { defer closers.Done(); _ = s.Close() }()
	}
	for range count {
		if err := receiveRPCSendResult(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("Send = %v, want cancellation", err)
		}
	}
	closers.Wait()
	assertNoRPCWaiters(t, s)
	if err := s.Send("after close", "", nil, nil); err == nil {
		t.Fatal("Send succeeded after Close")
	}
}

func TestSendRPC_LateRejectionEmitsError(t *testing.T) {
	s, commands := newRPCRequestTestSession(t)
	done := make(chan error, 1)
	go func() { done <- s.Send("supplement", "", nil, nil) }()
	cmd := receiveRPCCommand(t, commands)
	response := map[string]any{"type": "response", "command": "prompt", "id": cmd["id"], "success": true}
	s.handleEvent(response)
	if err := receiveRPCSendResult(t, done); err != nil {
		t.Fatal(err)
	}
	response["success"] = false
	response["error"] = "asynchronous preflight rejection"
	s.handleEvent(response)
	select {
	case event := <-s.Events():
		if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), "asynchronous preflight rejection") {
			t.Fatalf("late rejection was lost: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("late rejection was not surfaced")
	}
	assertNoRPCWaiters(t, s)
}

func TestSendRPC_BlockedWriteHonorsDeadline(t *testing.T) {
	reader, writer := io.Pipe() // Deliberately never read: Write cannot complete.
	t.Cleanup(func() { _ = reader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	s := &piSession{ctx: ctx, cancel: cancel, rpc: true, rpcStdin: writer, events: make(chan core.Event, 1)}
	t.Cleanup(func() { _ = s.Close() })
	deadline, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- s.requestRPCPrompt(deadline, map[string]any{"type": "prompt", "message": "blocked"}) }()
	if err := receiveRPCSendResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write = %v, want deadline exceeded", err)
	}
	assertNoRPCWaiters(t, s)
	// The interrupted transport must reject subsequent prompts immediately.
	if err := s.requestRPCPrompt(ctx, map[string]any{"type": "prompt"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reused an interrupted RPC transport: %v", err)
	}
}

func TestSendRPC_ConcurrentSendAndClose(t *testing.T) {
	s, _ := newRPCRequestTestSession(t)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 100 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := s.Send("racing close", "", nil, nil); err == nil {
				t.Error("unacknowledged Send succeeded")
			}
		}()
	}
	workers.Add(1)
	go func() { defer workers.Done(); <-start; _ = s.Close() }()
	close(start)
	done := make(chan error, 1)
	go func() { workers.Wait(); done <- nil }()
	if err := receiveRPCSendResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertNoRPCWaiters(t, s)
}
