package pi

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// rpcStaleTurnWarnAfter is the log-only bound after which a prompted turn
// that has seen neither a run settlement nor EOF is called stale.
const rpcStaleTurnWarnAfter = 5 * time.Minute

// rpcRequest owns one prompt ACK. ACKs describe acceptance, not completion;
// only agent_settled closes a run, including its compaction and retries.
type rpcRequest struct {
	generation uint64
	startsTurn bool
	ack        chan error
}

// resetRPCTurnLocked clears the turn state shared by every path that closes a
// turn before its agent_settled: rejected acknowledgements, EOF, and terminal
// emission. The unsolicited-run flag is not cleared here — a run that Pi is
// still streaming survives the failure of a prompt that never reached it.
func (s *piSession) resetRPCTurnLocked() {
	s.rpcTurnPending = false
	s.rpcRunStarted = false
	if s.rpcStaleTimer != nil {
		s.rpcStaleTimer.Stop()
		s.rpcStaleTimer = nil
	}
}

// armRPCTurnStaleWatchLocked warns when a prompted turn stays open for minutes
// without a settled event. It never closes the turn itself: the operator
// bounds a turn end-to-end with max_turn_time_mins, while this warning is the
// diagnostic that points there.
func (s *piSession) armRPCTurnStaleWatchLocked() {
	if s.rpcStaleTimer != nil {
		s.rpcStaleTimer.Stop()
	}
	s.rpcStaleTimer = time.AfterFunc(rpcStaleTurnWarnAfter, func() {
		s.rpcLifecycleMu.Lock()
		pending := s.rpcTurnPending
		s.rpcLifecycleMu.Unlock()
		if pending {
			slog.Warn("piSession: prompt pending without a settled event; check the peer or set max_turn_time_mins to bound the turn",
				"elapsed", rpcStaleTurnWarnAfter)
		}
	})
}

// Record ownership before writing: automatic compaction can happen before
// agent_start or even the prompt ACK. Supplements share the current run.
func (s *piSession) sendSettledRPC(cmd map[string]any) error {
	s.rpcSendMu.Lock()
	s.rpcLifecycleMu.Lock()
	s.rpcNextRequest++
	id := fmt.Sprintf("cc-connect-%d", s.rpcNextRequest)
	startsTurn := !s.rpcTurnPending
	if startsTurn {
		s.rpcGeneration++
		s.rpcTurnPending = true
		s.rpcRunStarted = false
		s.armRPCTurnStaleWatchLocked()
	}
	req := &rpcRequest{generation: s.rpcGeneration, startsTurn: startsTurn, ack: make(chan error, 1)}
	if s.rpcRequests == nil {
		s.rpcRequests = make(map[string]*rpcRequest)
	}
	s.rpcRequests[id] = req
	s.rpcLifecycleMu.Unlock()
	cmd["id"] = id
	if err := s.writeRPCCommand(cmd); err != nil {
		s.rpcLifecycleMu.Lock()
		delete(s.rpcRequests, id)
		if req.startsTurn && req.generation == s.rpcGeneration {
			s.resetRPCTurnLocked()
		}
		s.rpcLifecycleMu.Unlock()
		s.rpcSendMu.Unlock()
		return err
	}
	s.rpcSendMu.Unlock()
	select {
	case err := <-req.ack:
		return err
	case <-s.rpcDone:
		// A successful ACK may already be buffered when the reader exits.
		select {
		case err := <-req.ack:
			return err
		default:
		}
		return fmt.Errorf("pi: RPC process exited before prompt acknowledgement")
	case <-s.ctx.Done():
		select {
		case err := <-req.ack:
			return err
		default:
		}
		return s.ctx.Err()
	}
}

func (s *piSession) handleSettledRPCEvent(raw map[string]any) bool {
	switch raw["type"] {
	case "response":
		return s.handleSettledRPCResponse(raw)
	case "agent_start":
		s.rpcLifecycleMu.Lock()
		if s.rpcTurnPending {
			s.rpcRunStarted = true
		} else {
			// Unsolicited (extension-initiated) runs stay outside prompted-turn
			// ownership: a following user prompt must open its own generation
			// instead of joining as a supplement the older run's settled event
			// would close before that prompt produced anything.
			s.rpcUnsolicitedRun = true
		}
		s.rpcLifecycleMu.Unlock()
		return true
	case "agent_end":
		// Even willRetry=false can precede overflow recovery or follow-ups.
		s.handleAgentEnd(raw)
		return true
	case "agent_settled":
		// Ignore duplicates before the next run starts. Pi supplies no run ID;
		// extensions must not synthesize lifecycle records on stdout. A run
		// that no prompted turn ever joined is unsolicited activity and emits
		// no terminal event.
		s.rpcLifecycleMu.Lock()
		if s.rpcTurnPending {
			s.rpcLifecycleMu.Unlock()
			s.finishSettledRPCGeneration(0, true)
		} else {
			s.rpcUnsolicitedRun = false
			s.rpcLifecycleMu.Unlock()
		}
		return true
	case "compaction_start", "compaction_end", "auto_compaction_start", "auto_compaction_end":
		if errMsg, _ := raw["errorMessage"].(string); errMsg != "" {
			slog.Warn("piSession: compaction reported an error; waiting for terminal outcome", "error", errMsg)
		}
		return true
	default:
		return false
	}
}

func (s *piSession) handleSettledRPCResponse(raw map[string]any) bool {
	id, _ := raw["id"].(string)
	if raw["command"] != "prompt" {
		return false // includes the existing get_state startup probe
	}
	s.rpcLifecycleMu.Lock()
	req := s.rpcRequests[id]
	if req == nil {
		s.rpcLifecycleMu.Unlock()
		return true
	}
	delete(s.rpcRequests, id)
	success, _ := raw["success"].(bool)
	errMsg, _ := raw["error"].(string)
	data, _ := raw["data"].(map[string]any)
	disposition, _ := data["disposition"].(string)
	// Tolerate unknown dispositions by treating them like "started": a newer
	// Pi, or a wrapper that omits the data object, must not tear the session
	// down — the version gate only proves the field exists, not its value.
	if success && disposition != "started" && disposition != "queued" && disposition != "handled" {
		slog.Warn("piSession: unknown prompt disposition; treating it as accepted",
			"disposition", disposition, "id", id)
	}
	if !success && req.startsTurn && req.generation == s.rpcGeneration {
		s.resetRPCTurnLocked()
		s.pendingErr = ""
	}
	s.rpcLifecycleMu.Unlock()
	if !success {
		if errMsg == "" {
			errMsg = "pi: prompt request failed"
		}
		req.ack <- fmt.Errorf("%s", errMsg)
		return true
	}
	req.ack <- nil
	// "started" begins a run and "queued" steers the input into the run that
	// is already streaming, so either way a run covers the input. Record it
	// even before the corresponding agent_start arrives: a prompt sent while
	// an unsolicited extension run was streaming is covered by that run, and
	// its settled event must close the turn rather than fall off the
	// requireRun guard.
	if disposition == "started" || disposition == "queued" {
		s.rpcLifecycleMu.Lock()
		if s.rpcTurnPending {
			s.rpcRunStarted = true
		}
		s.rpcLifecycleMu.Unlock()
	}
	// A synchronous extension/input handler may consume a prompt without
	// starting a run; it then has no corresponding agent_settled event.
	if disposition == "handled" && req.startsTurn {
		s.finishSettledRPCGeneration(req.generation, false)
	}
	return true
}

// Only the stdout reader calls this function and accesses pendingErr.
// A handled ACK cannot finish an already started extension run.
// Emit a single terminal: core returns on EventError, so a trailing result
// would otherwise be left in the stream for the next queued request.
func (s *piSession) finishSettledRPCGeneration(generation uint64, requireRun bool) {
	s.rpcLifecycleMu.Lock()
	if !s.rpcTurnPending || (generation != 0 && generation != s.rpcGeneration) ||
		(requireRun != s.rpcRunStarted) {
		s.rpcLifecycleMu.Unlock()
		return
	}
	s.rpcTurnPending = false
	s.rpcRunStarted = false
	s.rpcUnsolicitedRun = false
	if s.rpcStaleTimer != nil {
		s.rpcStaleTimer.Stop()
		s.rpcStaleTimer = nil
	}
	s.rpcLifecycleMu.Unlock()
	errMsg := s.pendingErr
	s.pendingErr = ""
	evt := core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true}
	if errMsg != "" {
		evt = core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// Release unacknowledged Sends on EOF. An unacknowledged primary fails via
// Send; an acknowledged running turn fails via exactly one EventError.
func (s *piSession) failSettledRPCRequests(err error) {
	s.pendingErr = ""
	s.rpcLifecycleMu.Lock()
	pending := s.rpcTurnPending
	requests := s.rpcRequests
	s.rpcRequests = nil
	primaryACKPending := false
	for _, req := range requests {
		if req.startsTurn && req.generation == s.rpcGeneration {
			primaryACKPending = true
		}
	}
	s.rpcTurnPending = false
	s.rpcRunStarted = false
	s.rpcUnsolicitedRun = false
	if s.rpcStaleTimer != nil {
		s.rpcStaleTimer.Stop()
		s.rpcStaleTimer = nil
	}
	s.rpcLifecycleMu.Unlock()
	for _, req := range requests {
		req.ack <- err
	}
	if pending && !primaryACKPending {
		select {
		case s.events <- core.Event{Type: core.EventError, Error: err}:
		case <-s.ctx.Done():
		}
	}
}
