package pi

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const compactPromptRejection = "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry."

func TestBusySteer_OnlyNativeCompactionRejectionAllowsReplay(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		safe          bool
	}{
		{"native_compaction_preflight", compactPromptRejection, true},
		{"extension_may_have_side_effects", "extension failed after updating a file", false},
		{"unknown_preflight", "prompt rejected while queued", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w := issue2161UnitSession(t)
			s.handleEvent(map[string]any{"type": "agent_start"})
			cmd, resCh, errCh := drainSteer(t, s, w, "ordinary text")
			s.handleEvent(issue2161UnitResponse(cmd, false, tc.message))
			_, err := awaitSteer(t, resCh, errCh)
			var rejected *core.SteerNotAcceptedError
			if err == nil || errors.As(err, &rejected) != tc.safe {
				t.Fatalf("error=%v safe replay=%v, want %v", err, rejected != nil, tc.safe)
			}
			issue2161UnitNoTerminal(t, s, "supplement rejection must not terminate original run")
		})
	}
}

func TestBusySteer_CanceledACKWaitKeepsRunAndAcceptsLateACK(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.handleEvent(map[string]any{"type": "agent_start"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.SendBusySteer(ctx, "canceled supplement", "canceled-wait", nil, nil); done <- err }()
	// Observe the actual write before ending the wait: a short timer created
	// before scheduling Send can expire without writing and strand this test.
	var cmd map[string]any
	select {
	case cmd = <-w.commands:
	case err := <-done:
		t.Fatalf("send returned before writing: %v", err)
	case <-time.After(time.Second):
		t.Fatal("RPC command was not written")
	}
	cancel()
	select {
	case err := <-done:
		var rejected *core.SteerNotAcceptedError
		if !errors.Is(err, context.Canceled) || errors.As(err, &rejected) {
			t.Fatalf("canceled wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing ACK ignored caller cancellation")
	}
	if s.ctx.Err() != nil {
		t.Fatal("canceling ACK wait canceled original session")
	}
	issue2161UnitNoTerminal(t, s, "ACK wait cancellation")
	s.handleEvent(issue2161UnitResponse(cmd, true, "queued"))
	s.handleEvent(map[string]any{"type": "agent_end"})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("late ACK events=%+v", events)
	}
	s.rpcLifecycleMu.Lock()
	defer s.rpcLifecycleMu.Unlock()
	if len(s.rpcRequests) != 0 {
		t.Fatalf("late ACK left %d requests", len(s.rpcRequests))
	}
}

func TestBusySteer_ExpiredDeadlineBeforeSendIsNotAccepted(t *testing.T) {
	s, w := issue2161UnitSession(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := s.SendBusySteer(ctx, "never write", "expired", nil, nil)
	var rejected *core.SteerNotAcceptedError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &rejected) {
		t.Fatalf("expired deadline error=%v", err)
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("expired request wrote %v", cmd)
	default:
	}
}

func TestBusySteer_CanceledBeforeSendIsNotAccepted(t *testing.T) {
	s, w := issue2161UnitSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := s.SendBusySteer(ctx, "never write", "canceled", nil, nil); done <- err }()
	select {
	case err := <-done:
		var rejected *core.SteerNotAcceptedError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &rejected) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled send waited for ACK")
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("canceled send wrote %v", cmd)
	default:
	}
}

type shortSteerWire struct {
	n   int
	err error
}

func (w shortSteerWire) Write(p []byte) (int, error) { return w.n, w.err }
func (w shortSteerWire) Close() error                { return nil }

func TestBusySteer_PartialWriteAndEOFAreUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"partial_with_error", 1, io.ErrClosedPipe},
		{"short_without_error", 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := issue2161UnitSession(t)
			s.rpcStdin = shortSteerWire{tc.n, tc.err}
			done := make(chan error, 1)
			go func() { _, err := s.SendBusySteer(context.Background(), "partial", "partial", nil, nil); done <- err }()
			select {
			case err := <-done:
				var rejected *core.SteerNotAcceptedError
				if err == nil || errors.As(err, &rejected) {
					t.Fatalf("partial write error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("short write waited for ACK")
			}
		})
	}
	t.Run("EOF", func(t *testing.T) {
		s, w := issue2161UnitSession(t)
		_, resCh, errCh := drainSteer(t, s, w, "eof")
		s.failSettledRPCRequests(io.EOF)
		_, err := awaitSteer(t, resCh, errCh)
		var rejected *core.SteerNotAcceptedError
		if !errors.Is(err, io.EOF) || errors.As(err, &rejected) {
			t.Fatalf("EOF error=%v", err)
		}
	})
}

func TestBusySteer_CapabilityRequiresRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		rpc  bool
		want bool
	}{
		{"default_rpc", true, true},
		{"json", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(tc.rpc)
			defer s.cancel()
			if got := s.SupportsBusySteer(); got != tc.want {
				t.Fatalf("SupportsBusySteer()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestBusySteer_ZeroByteWriteAllowsQueueFallback(t *testing.T) {
	s, _ := issue2161UnitSession(t)
	s.rpcStdin = shortSteerWire{n: 0, err: io.ErrClosedPipe}
	_, err := s.SendBusySteer(context.Background(), "unsent", "unsent", nil, nil)
	var rejected *core.SteerNotAcceptedError
	if !errors.Is(err, io.ErrClosedPipe) || !errors.As(err, &rejected) {
		t.Fatalf("error=%v", err)
	}
}

func TestBusySteer_SlashCommandErrorCannotCertifyNativePreflight(t *testing.T) {
	s, w := issue2161UnitSession(t)
	cmd, resCh, errCh := drainSteer(t, s, w, "/extension")
	s.handleEvent(issue2161UnitResponse(cmd, false, compactPromptRejection))
	_, err := awaitSteer(t, resCh, errCh)
	var rejected *core.SteerNotAcceptedError
	if err == nil || errors.As(err, &rejected) {
		t.Fatalf("slash extension error=%v", err)
	}
}
