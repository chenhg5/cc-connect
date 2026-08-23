package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTurnLifecyclePumpOwnerRejectsConcurrentPump(t *testing.T) {
	var owner turnLifecyclePumpOwner
	started := make(chan struct{})
	if err := owner.Start(context.Background(), func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}); err != nil {
		t.Fatalf("start first pump: %v", err)
	}
	<-started

	if err := owner.Start(context.Background(), func(context.Context) {}); !errors.Is(err, errTurnLifecyclePumpRunning) {
		t.Fatalf("second start error = %v, want %v", err, errTurnLifecyclePumpRunning)
	}
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("stop first pump: %v", err)
	}
}

func TestTurnLifecyclePumpOwnerStopIsIdempotentAndAllowsRestart(t *testing.T) {
	var owner turnLifecyclePumpOwner
	firstCanceled := make(chan struct{})
	if err := owner.Start(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		close(firstCanceled)
	}); err != nil {
		t.Fatalf("start first pump: %v", err)
	}
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("stop first pump: %v", err)
	}
	select {
	case <-firstCanceled:
	default:
		t.Fatal("stop returned before the first pump observed cancellation")
	}
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("repeat stop: %v", err)
	}

	secondStarted := make(chan struct{})
	if err := owner.Start(context.Background(), func(ctx context.Context) {
		close(secondStarted)
		<-ctx.Done()
	}); err != nil {
		t.Fatalf("restart pump: %v", err)
	}
	<-secondStarted
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("stop restarted pump: %v", err)
	}
}

func TestTurnLifecyclePumpOwnerNaturalExitAllowsRestart(t *testing.T) {
	var owner turnLifecyclePumpOwner
	if err := owner.Start(context.Background(), func(context.Context) {}); err != nil {
		t.Fatalf("start first pump: %v", err)
	}
	done := owner.activeDone()
	if done == nil {
		t.Fatal("active pump has no done channel")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first pump did not finish")
	}

	if err := owner.Start(context.Background(), func(context.Context) {}); err != nil {
		t.Fatalf("restart after natural exit: %v", err)
	}
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("stop restarted pump: %v", err)
	}
}

func TestTurnLifecyclePumpOwnerTimeoutRetainsOwnership(t *testing.T) {
	var owner turnLifecyclePumpOwner
	release := make(chan struct{})
	if err := owner.Start(context.Background(), func(context.Context) {
		<-release
	}); err != nil {
		t.Fatalf("start pump: %v", err)
	}

	if err := owner.Stop(10 * time.Millisecond); !errors.Is(err, errTurnLifecyclePumpTimeout) {
		t.Fatalf("stop error = %v, want %v", err, errTurnLifecyclePumpTimeout)
	}
	if err := owner.Start(context.Background(), func(context.Context) {}); !errors.Is(err, errTurnLifecyclePumpRunning) {
		t.Fatalf("start during timed-out pump error = %v, want %v", err, errTurnLifecyclePumpRunning)
	}

	close(release)
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("wait for released pump: %v", err)
	}
	if err := owner.Start(context.Background(), func(context.Context) {}); err != nil {
		t.Fatalf("restart after released pump: %v", err)
	}
	if err := owner.Stop(time.Second); err != nil {
		t.Fatalf("stop restarted pump: %v", err)
	}
}

func TestTurnLifecyclePumpOwnerCloseIsPermanentAndConcurrentSafe(t *testing.T) {
	var owner turnLifecyclePumpOwner
	started := make(chan struct{})
	if err := owner.Start(context.Background(), func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}); err != nil {
		t.Fatalf("start pump: %v", err)
	}
	<-started

	const callers = 12
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(closeOwner bool) {
			defer wg.Done()
			if closeOwner {
				errs <- owner.Close(time.Second)
				return
			}
			errs <- owner.Stop(time.Second)
		}(i%2 == 0)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent stop/close: %v", err)
		}
	}
	if err := owner.Close(time.Second); err != nil {
		t.Fatalf("repeat close: %v", err)
	}
	if err := owner.Start(context.Background(), func(context.Context) {}); !errors.Is(err, errTurnLifecyclePumpClosed) {
		t.Fatalf("start after close error = %v, want %v", err, errTurnLifecyclePumpClosed)
	}
}

func TestRunTurnLifecycleEventPumpOwnsEventLoopAndCleanup(t *testing.T) {
	events := make(chan int, 2)
	events <- 1
	events <- 2
	close(events)
	var handled []int
	cleanupCalls := 0
	exit := runTurnLifecycleEventPump(context.Background(), events, turnLifecycleEventCallbacks[int]{
		Handle: func(event int) bool {
			handled = append(handled, event)
			return true
		},
		Cleanup: func(got turnLifecycleEventExit) {
			cleanupCalls++
			if got != turnLifecycleEventSourceClosed {
				t.Errorf("cleanup exit = %v, want source closed", got)
			}
		},
	})
	if exit != turnLifecycleEventSourceClosed || cleanupCalls != 1 {
		t.Fatalf("exit = %v, cleanup calls = %d", exit, cleanupCalls)
	}
	if len(handled) != 2 || handled[0] != 1 || handled[1] != 2 {
		t.Fatalf("handled events = %#v", handled)
	}
}

func TestRunTurnLifecycleEventPumpSupportsHandlerTerminal(t *testing.T) {
	events := make(chan string, 1)
	events <- "terminal"
	exit := runTurnLifecycleEventPump(context.Background(), events, turnLifecycleEventCallbacks[string]{
		Handle: func(event string) bool { return event != "terminal" },
	})
	if exit != turnLifecycleEventHandlerStopped {
		t.Fatalf("exit = %v, want handler stopped", exit)
	}
}
