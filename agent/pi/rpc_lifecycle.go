package pi

import (
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"
)

// rpcRequest owns one prompt ACK. ACKs describe acceptance, not completion;
// only agent_settled closes a run, including its compaction and retries.
type rpcRequest struct {
	generation uint64
	startsTurn bool
	ack        chan error
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
			s.rpcTurnPending = false
			s.rpcRunStarted = false
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
		if !s.rpcTurnPending { // unsolicited runs use the same terminal contract
			s.rpcGeneration++
			s.rpcTurnPending = true
		}
		s.rpcRunStarted = true
		s.rpcLifecycleMu.Unlock()
		return true
	case "agent_end":
		// Even willRetry=false can precede overflow recovery or follow-ups.
		s.handleAgentEnd(raw)
		return true
	case "agent_settled":
		// Ignore duplicates before the next run starts. Pi supplies no run ID;
		// extensions must not synthesize lifecycle records on stdout.
		s.finishSettledRPCGeneration(0, true)
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
	invalidProtocol := success && disposition != "started" && disposition != "queued" && disposition != "handled"
	if invalidProtocol {
		success = false
		errMsg = "pi: prompt response requires a valid disposition (Pi >= 0.99.0)"
	}
	if !success && req.startsTurn && req.generation == s.rpcGeneration {
		s.rpcTurnPending = false
		s.rpcRunStarted = false
	}
	s.rpcLifecycleMu.Unlock()
	if !success {
		if errMsg == "" {
			errMsg = "pi: prompt request failed"
		}
		req.ack <- fmt.Errorf("%s", errMsg)
		if invalidProtocol {
			if !req.startsTurn {
				// A malformed supplement invalidates the transport while the
				// foreground reader is still waiting for its original run.
				s.rpcLifecycleMu.Lock()
				s.rpcTurnPending = false
				s.rpcRunStarted = false
				s.rpcLifecycleMu.Unlock()
				select {
				case s.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}:
				case <-s.ctx.Done():
				}
			}
			s.alive.Store(false)
			s.cancel()
		}
		return true
	}
	req.ack <- nil
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
