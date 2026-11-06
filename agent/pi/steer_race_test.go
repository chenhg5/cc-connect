package pi

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// These tests pin the last-turn-output race fix:
//
//	A steer sent while rpcTurnPending was true gets startsTurn=false locally,
//	but Pi may have already finished the old run and reports
//	disposition="started" — meaning it began a *new* run. Without the fix,
//	the old run's agent_settled (arriving right after the ACK) would clear
//	rpcTurnPending and emit EventResult, prematurely ending the new run.
//
// The fix bumps rpcGeneration when disposition="started" arrives on a
// non-turn-starting request, and agent_settled now passes the current
// generation (not 0) so the stale settled from the old run is rejected.

// TestSteerRace_StartedUpgradeBumpsGeneration: a steer with
// disposition="started" on a startsTurn=false request must bump the
// generation so the old run's agent_settled cannot end it.
func TestSteerRace_StartedUpgradeBumpsGeneration(t *testing.T) {
	s, w := issue2161UnitSession(t)

	// Start a run (simulates an in-flight turn).
	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("agent_start was not consumed")
	}

	genBefore := getRPCGeneration(s)

	// Steer while busy → startsTurn=false, but Pi says "started".
	cmd, resCh, errCh := drainSteer(t, s, w, "supplement that starts a new run")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "started"},
	})

	res, err := awaitSteer(t, resCh, errCh)
	if err != nil {
		t.Fatalf("SendBusySteer error = %v", err)
	}
	if res.Disposition != core.SteerStarted {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerStarted)
	}

	genAfter := getRPCGeneration(s)
	if genAfter <= genBefore {
		t.Fatalf("generation not bumped: before=%d after=%d", genBefore, genAfter)
	}

	// The old run's agent_settled arrives — it must NOT produce an
	// EventResult because the generation was bumped.
	s.handleEvent(map[string]any{"type": "agent_settled"})

	select {
	case evt := <-s.events:
		t.Fatalf("stale agent_settled produced an event: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Good: no premature terminal.
	}
}

// TestSteerRace_QueuedDoesNotBumpGeneration: a steer with
// disposition="queued" (injected into the running turn) must NOT bump the
// generation — the existing agent_settled should still end the turn normally.
func TestSteerRace_QueuedDoesNotBumpGeneration(t *testing.T) {
	s, w := issue2161UnitSession(t)

	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("agent_start was not consumed")
	}

	genBefore := getRPCGeneration(s)

	cmd, resCh, errCh := drainSteer(t, s, w, "supplement injected into running turn")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "queued"},
	})

	res, err := awaitSteer(t, resCh, errCh)
	if err != nil {
		t.Fatalf("SendBusySteer error = %v", err)
	}
	if res.Disposition != core.SteerQueued {
		t.Fatalf("disposition = %q, want %q", res.Disposition, core.SteerQueued)
	}

	genAfter := getRPCGeneration(s)
	if genAfter != genBefore {
		t.Fatalf("generation changed for queued steer: before=%d after=%d", genBefore, genAfter)
	}

	// The normal agent_end records the generation, then agent_settled
	// produces EventResult.
	s.rpcLifecycleMu.Lock()
	s.rpcSettlingGen = s.rpcGeneration
	s.rpcLifecycleMu.Unlock()
	s.handleEvent(map[string]any{"type": "agent_settled"})

	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Fatalf("expected EventResult, got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent_settled did not produce EventResult")
	}
}

// TestSteerRace_NewRunAgentSettledEndsCorrectly: after the generation bump
// from a "started" steer, the new run's agent_start → agent_settled must
// produce exactly one EventResult.
func TestSteerRace_NewRunAgentSettledEndsCorrectly(t *testing.T) {
	s, w := issue2161UnitSession(t)

	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("agent_start was not consumed")
	}

	// Steer → "started" → generation bumped.
	cmd, resCh, errCh := drainSteer(t, s, w, "starts new run")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "started"},
	})
	if _, err := awaitSteer(t, resCh, errCh); err != nil {
		t.Fatalf("SendBusySteer error = %v", err)
	}

	// Old agent_settled — must be rejected (stale generation). Set
	// rpcSettlingGen to the old generation to simulate agent_end ordering.
	s.rpcLifecycleMu.Lock()
	s.rpcSettlingGen = s.rpcGeneration - 1
	s.rpcLifecycleMu.Unlock()
	s.handleEvent(map[string]any{"type": "agent_settled"})

	// New run's agent_start.
	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("new agent_start was not consumed")
	}

	// New run's agent_end records the current generation, then agent_settled
	// produces EventResult.
	s.rpcLifecycleMu.Lock()
	s.rpcSettlingGen = s.rpcGeneration
	s.rpcLifecycleMu.Unlock()
	s.handleEvent(map[string]any{"type": "agent_settled"})

	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Fatalf("expected EventResult, got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new run's agent_settled did not produce EventResult")
	}

	// And it must be exactly one event, not two.
	select {
	case evt := <-s.events:
		t.Fatalf("unexpected second event: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Good.
	}
}

// TestSteerRace_AgentSettledGenerationGuard: agent_settled for a previous
// generation (before the steer bump) must not end the current run even if
// rpcRunStarted is true (because the new agent_start already fired).
// Real-world ordering: agent_end(old) → agent_start(new) → agent_settled(old).
func TestSteerRace_AgentSettledGenerationGuard(t *testing.T) {
	s, w := issue2161UnitSession(t)

	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("agent_start was not consumed")
	}

	// Steer → "started" → generation bumped, rpcRunStarted=false.
	cmd, resCh, errCh := drainSteer(t, s, w, "starts new run")
	id, _ := cmd["id"].(string)
	s.handleEvent(map[string]any{
		"type": "response", "id": id, "command": "prompt",
		"success": true, "data": map[string]any{"disposition": "started"},
	})
	if _, err := awaitSteer(t, resCh, errCh); err != nil {
		t.Fatalf("SendBusySteer error = %v", err)
	}

	// Old run's agent_end records the old generation in rpcSettlingGen.
	// At this point rpcGeneration was already bumped by the steer, so we
	// need to simulate agent_end arriving BEFORE the steer's ACK bumped it.
	// In practice agent_end arrives before the steer's response; here we
	// set rpcSettlingGen manually to the pre-bump generation to simulate
	// that ordering.
	s.rpcLifecycleMu.Lock()
	s.rpcSettlingGen = s.rpcGeneration - 1 // the old generation
	oldGen := s.rpcSettlingGen
	s.rpcLifecycleMu.Unlock()

	// New run's agent_start fires — rpcRunStarted=true, gen unchanged.
	if err := s.handleSettledRPCEvent(map[string]any{"type": "agent_start"}); err != true {
		t.Fatal("new agent_start was not consumed")
	}

	// A stale agent_settled from the OLD run arrives after the new
	// agent_start. It must be rejected because rpcSettlingGen (oldGen)
	// does not match the current (bumped) generation.
	s.handleEvent(map[string]any{"type": "agent_settled"})

	select {
	case evt := <-s.events:
		t.Fatalf("stale agent_settled (gen=%d) ended the new run (gen=%d): %+v", oldGen, oldGen+1, evt)
	case <-time.After(200 * time.Millisecond):
		// Good: no premature terminal.
	}

	// Now the new run's agent_end records the current generation.
	s.rpcLifecycleMu.Lock()
	s.rpcSettlingGen = s.rpcGeneration
	s.rpcLifecycleMu.Unlock()

	// The new run's agent_settled does end it.
	s.handleEvent(map[string]any{"type": "agent_settled"})
	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Fatalf("expected EventResult, got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new run's agent_settled did not produce EventResult")
	}
}

// getRPCGeneration reads s.rpcGeneration under the lifecycle lock.
func getRPCGeneration(s *piSession) uint64 {
	s.rpcLifecycleMu.Lock()
	defer s.rpcLifecycleMu.Unlock()
	return s.rpcGeneration
}

// This is a deliberately ordered protocol-contract test. It does not claim
// stock Pi emits a started ACK before the previous run's settled event.
func TestSteerRace_StartedReportsWhetherBridgeTurnContinues(t *testing.T) {
	for _, settledBeforeACK := range []bool{false, true} {
		name := "before_old_settled"
		if settledBeforeACK {
			name = "after_old_settled"
		}
		t.Run(name, func(t *testing.T) {
			s, w := issue2161UnitSession(t)
			s.handleEvent(map[string]any{"type": "agent_start"})
			cmd, resCh, errCh := drainSteer(t, s, w, "new native run")
			s.handleEvent(map[string]any{"type": "agent_end"})
			if settledBeforeACK {
				s.handleEvent(map[string]any{"type": "agent_settled"})
				events := drainEvents(s)
				if len(events) != 1 || events[0].Type != core.EventResult {
					t.Fatalf("old terminal=%+v", events)
				}
			}
			s.handleEvent(issue2161UnitResponse(cmd, true, "started"))
			result, err := awaitSteer(t, resCh, errCh)
			if err != nil || result.Disposition != core.SteerStarted || result.ContinuesTurn == settledBeforeACK {
				t.Fatalf("result=%+v err=%v; ContinuesTurn want %v", result, err, !settledBeforeACK)
			}
			s.handleEvent(map[string]any{"type": "agent_start"})
			if !settledBeforeACK {
				s.handleEvent(map[string]any{"type": "agent_settled"})
				issue2161UnitNoTerminal(t, s, "suppressed old settled after new start")
			}
			s.handleEvent(map[string]any{"type": "agent_end"})
			s.handleEvent(map[string]any{"type": "agent_settled"})
			events := drainEvents(s)
			if len(events) != 1 || events[0].Type != core.EventResult {
				t.Fatalf("new terminal=%+v", events)
			}
		})
	}
}
