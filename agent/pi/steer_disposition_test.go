package pi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// These tests pin the prompt-ACK disposition contract that busy-steer relies on.
// The expectations come from a live Pi 1.5.0 RPC capture (2026-11-03):
//
//	idle prompt                     → {"disposition":"started"}
//	prompt while a run is in flight → {"disposition":"queued"}
//
// The raw record shapes are copied from that capture, not invented.

// drainSteer starts SendBusySteer in a goroutine and returns the command Pi
// received plus a channel carrying the result.
func drainSteer(t *testing.T, s *piSession, w *issue2161UnitWire, text string) (map[string]any, <-chan core.SteerResult, <-chan error) {
	t.Helper()
	resCh := make(chan core.SteerResult, 1)
	errCh := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		res, err := s.SendBusySteer(context.Background(), text, "msg-1", nil, nil)
		resCh <- res
		errCh <- err
	}()
	t.Cleanup(func() {
		s.cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("SendBusySteer goroutine did not stop after cancellation")
		}
	})
	select {
	case cmd := <-w.commands:
		return cmd, resCh, errCh
	case <-time.After(5 * time.Second):
		t.Fatal("RPC command was not written")
		return nil, nil, nil
	}
}

func awaitSteer(t *testing.T, resCh <-chan core.SteerResult, errCh <-chan error) (core.SteerResult, error) {
	t.Helper()
	select {
	case err := <-errCh:
		return <-resCh, err
	case <-time.After(5 * time.Second):
		t.Fatal("SendBusySteer did not return after its correlated response")
		return core.SteerResult{}, nil
	}
}

// TestSteerDisposition_BusySteerQueued: Pi holds the prompt in the running
// turn's steering queue and says so in the ACK.
func TestSteerDisposition_BusySteerQueued(t *testing.T) {
	s, w := issue2161UnitSession(t)

	// A run must be in flight for Pi to queue instead of start.
	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err == false {
		t.Fatal("agent_start was not consumed by the settled lifecycle")
	}

	cmd, resCh, errCh := drainSteer(t, s, w, "补充：还要覆盖账本")
	if cmd["type"] != "prompt" {
		t.Fatalf("command type = %v, want prompt", cmd["type"])
	}
	if cmd["streamingBehavior"] != "steer" {
		t.Fatalf("streamingBehavior = %v, want steer", cmd["streamingBehavior"])
	}
	id, _ := cmd["id"].(string)
	if id == "" {
		t.Fatal("busy-steer prompt must carry a request id")
	}

	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "queued"},
	})

	res, err := awaitSteer(t, resCh, errCh)
	if err != nil {
		t.Fatalf("SendBusySteer error = %v, want nil", err)
	}
	if res.Disposition != core.SteerQueued {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerQueued)
	}
	if res.RequestID != id {
		t.Fatalf("RequestID = %q, want %q", res.RequestID, id)
	}
}

// TestSteerDisposition_IdlePromptStarted: the previous turn had already ended,
// so Pi started a new run. The engine must be able to warn the user about it
// instead of claiming the input joined the running task.
func TestSteerDisposition_IdlePromptStarted(t *testing.T) {
	s, w := issue2161UnitSession(t)

	cmd, resCh, errCh := drainSteer(t, s, w, "顺便加个测试")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "started"},
	})

	res, err := awaitSteer(t, resCh, errCh)
	if err != nil {
		t.Fatalf("SendBusySteer error = %v, want nil", err)
	}
	if res.Disposition != core.SteerStarted {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerStarted)
	}
}

// TestSteerDisposition_HandledByExtension: an extension consumed the prompt
// without a model run.
func TestSteerDisposition_HandledByExtension(t *testing.T) {
	s, w := issue2161UnitSession(t)

	cmd, resCh, errCh := drainSteer(t, s, w, "/something-extension-owned")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "handled"},
	})

	res, err := awaitSteer(t, resCh, errCh)
	if err != nil {
		t.Fatalf("SendBusySteer error = %v, want nil", err)
	}
	if res.Disposition != core.SteerHandled {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerHandled)
	}
}

// TestSteerDisposition_MissingFieldIsUnconfirmed: never over-claim. If Pi omits
// data.disposition, report uncertain delivery and reject the incompatible peer.
func TestSteerDisposition_MissingFieldIsUnconfirmed(t *testing.T) {
	s, w := issue2161UnitSession(t)

	cmd, resCh, errCh := drainSteer(t, s, w, "no disposition in ACK")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{},
	})

	res, err := awaitSteer(t, resCh, errCh)
	var rejected *core.SteerNotAcceptedError
	if err == nil || !strings.Contains(err.Error(), "disposition") || errors.As(err, &rejected) {
		t.Fatalf("invalid protocol must fail without certifying replay safety: %v", err)
	}
	if res.Disposition != core.SteerUnconfirmed {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerUnconfirmed)
	}
}

// TestSteerDisposition_RejectedReturnsError: failed ACKs surface their error.
// A generic error does not establish whether extension side effects occurred.
func TestSteerDisposition_RejectedReturnsError(t *testing.T) {
	s, w := issue2161UnitSession(t)

	cmd, resCh, errCh := drainSteer(t, s, w, "rejected")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": false, "error": "prompt rejected while queued",
	})

	_, err := awaitSteer(t, resCh, errCh)
	if err == nil {
		t.Fatal("SendBusySteer error = nil, want rejection error")
	}
	if err.Error() != "prompt rejected while queued" {
		t.Fatalf("error = %v, want the harness rejection message", err)
	}
}

// JSON mode cannot accept mid-turn input; it must reject before any write.
func TestSteerDisposition_JSONModeRejectsBeforeWriting(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.rpc = false

	_, err := s.SendBusySteer(context.Background(), "json steer", "msg-json", nil, nil)
	var rejected *core.SteerNotAcceptedError
	if !errors.As(err, &rejected) {
		t.Fatalf("JSON steer error = %v, want definite rejection", err)
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("JSON steer unexpectedly wrote %v", cmd)
	default:
	}
}
