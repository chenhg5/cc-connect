package pi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Evaluate the session cancellation case only after the initial ACK probe.
// The hook makes a real ACK and session cancellation ready simultaneously.
// Repetition checks both outcomes of Go's randomized ready-case selection.
type ackAtSessionDoneContext struct {
	context.Context
	once       sync.Once
	beforeDone func()
}

func (c *ackAtSessionDoneContext) Done() <-chan struct{} {
	c.once.Do(c.beforeDone)
	return c.Context.Done()
}

func TestBusySteer_KnownACKWinsConcurrentSessionCancellation(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		name := "rejection"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			for range 32 {
				s, w := issue2161UnitSession(t)
				s.handleEvent(map[string]any{"type": "agent_start"})
				s.ctx = &ackAtSessionDoneContext{Context: s.ctx, beforeDone: func() {
					var command map[string]any
					select {
					case command = <-w.commands:
					case <-time.After(time.Second):
						t.Fatal("cancellation evaluated before RPC write")
					}
					detail := compactPromptRejection
					if accepted {
						detail = "queued"
					}
					s.handleEvent(issue2161UnitResponse(command, accepted, detail))
					s.cancel()
				}}
				result, err := s.SendBusySteer(context.Background(), "correction", "cancel-race", nil, nil)
				if accepted {
					if err != nil || result.Disposition != core.SteerQueued {
						t.Fatalf("known acceptance replaced by cancellation: result=%+v err=%v", result, err)
					}
				} else {
					var rejection *core.SteerNotAcceptedError
					if !errors.As(err, &rejection) {
						t.Fatalf("known rejection replaced by cancellation: result=%+v err=%v", result, err)
					}
				}
			}
		})
	}
}
