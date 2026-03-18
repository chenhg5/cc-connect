package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRelayManager_DefaultTimeout(t *testing.T) {
	rm := NewRelayManager("")

	if rm.timeout != relayTimeout {
		t.Fatalf("rm.timeout = %v, want %v", rm.timeout, relayTimeout)
	}
}

func TestRelayManager_RelayContextHonorsConfiguredTimeout(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(3 * time.Second)

	ctx, cancel := rm.relayContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected relay context deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 3*time.Second {
		t.Fatalf("time until deadline = %v, want within (0, 3s]", remaining)
	}
}

func TestRelayManager_RelayContextDisablesTimeoutAtZero(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(0)

	baseCtx := context.Background()
	ctx, cancel := rm.relayContext(baseCtx)
	defer cancel()

	if ctx != baseCtx {
		t.Fatal("expected relayContext to return the original context when timeout is disabled")
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("expected no deadline when timeout is disabled")
	}
}

func TestHandleRelay_ReturnsPartialOnTimeout(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "chat-1", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	session.events <- Event{Type: EventText, Content: "partial response", SessionID: "relay-session"}
	time.Sleep(40 * time.Millisecond)
	session.events <- Event{Type: EventThinking, Content: "still working"}

	got := <-done
	if got.err != nil {
		t.Fatalf("HandleRelay() error = %v, want nil", got.err)
	}
	if got.resp != "partial response" {
		t.Fatalf("HandleRelay() response = %q, want %q", got.resp, "partial response")
	}
}

func TestHandleRelay_TimeoutWithoutTextReturnsContextError(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "chat-1", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	time.Sleep(40 * time.Millisecond)
	session.events <- Event{Type: EventThinking, Content: "still working"}

	got := <-done
	if got.resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", got.resp)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("HandleRelay() error = %v, want context deadline exceeded", got.err)
	}
}
