package pi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// rpcRequest correlates command responses; successful prompt responses only
// acknowledge preflight. They do not end the logical agent turn.
type rpcRequest struct {
	command    string
	generation uint64
	startsTurn bool
	ack        chan error
	// disposition is Pi's own verdict on what it did with a prompt:
	// "started" (new run), "queued" (held for the in-flight run),
	// "handled" (extension consumed it). Captured from the prompt
	// response's data.disposition and read after ack succeeds.
	disposition   string
	continuesTurn bool // adapter will coalesce native runs into one bridge terminal
	// requestID is the id sent in the JSONL command, exposed to callers
	// that want to correlate the ACK with their own bookkeeping.
	requestID   string
	plainPrompt bool // native compaction rejection precedes all input handlers for plain text
}

// steerResultFor maps a completed prompt request onto the engine-facing
// SteerResult. Pi's disposition values were verified against a live 1.5.0 RPC
// capture: "started" for an idle prompt, "queued" for a prompt injected while
// a run was in flight. Anything else (including a missing field) is reported as
// unconfirmed so the user is never told more than Pi actually promised.
func (s *piSession) steerResultFor(req *rpcRequest) core.SteerResult {
	s.rpcLifecycleMu.Lock()
	disp := req.disposition
	continuesTurn := req.continuesTurn
	s.rpcLifecycleMu.Unlock()

	res := core.SteerResult{RequestID: req.requestID, ContinuesTurn: continuesTurn}
	switch disp {
	case "queued":
		res.Disposition = core.SteerQueued
	case "started":
		res.Disposition = core.SteerStarted
	case "handled":
		res.Disposition = core.SteerHandled
	default:
		res.Disposition = core.SteerUnconfirmed
	}
	return res
}

// sendSettledRPC records ownership before writing: Pi can compact before its
// first agent_start. Prompt ACK failures return through Send's existing error
// path, including rejected supplements, without failing a still-running turn.
func (s *piSession) sendSettledRPC(cmd map[string]any) error {
	// Pi can perform automatic compaction, input hooks and image normalization
	// before acknowledging an idle prompt. Preserve the session/EOF boundary;
	// an arbitrary ACK deadline would terminate valid preflight work.
	_, err := s.sendSettledRPCResult(s.ctx, cmd)
	return err
}

// sendSettledRPCResult is sendSettledRPC that also reports the request id and
// Pi's own disposition for the prompt ("" when the command was not a prompt,
// or when Pi omitted data.disposition). Only prompt responses carry it.
func (s *piSession) sendSettledRPCResult(ctx context.Context, cmd map[string]any) (core.SteerResult, error) {
	command, _ := cmd["type"].(string)
	if err := ctx.Err(); err != nil {
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: err}
	}
	if err := s.ctx.Err(); err != nil {
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: err}
	}
	s.rpcSendMu.Lock()
	if err := ctx.Err(); err != nil {
		s.rpcSendMu.Unlock()
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: err}
	}
	s.rpcLifecycleMu.Lock()
	if !s.alive.Load() || s.ctx.Err() != nil {
		s.rpcLifecycleMu.Unlock()
		s.rpcSendMu.Unlock()
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: session is closed")}
	}
	if command == "compact" && s.rpcTurnPending {
		s.rpcLifecycleMu.Unlock()
		s.rpcSendMu.Unlock()
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: cannot compact while a turn is pending")}
	}
	s.rpcNextRequest++
	id := fmt.Sprintf("cc-connect-%d", s.rpcNextRequest)
	startsTurn := !s.rpcTurnPending
	if startsTurn {
		s.rpcGeneration++
		s.rpcTurnPending = true
		s.rpcRunStarted = false
	}
	req := &rpcRequest{command: command, generation: s.rpcGeneration, startsTurn: startsTurn, requestID: id}
	if command == "prompt" {
		text, _ := cmd["message"].(string)
		req.plainPrompt = !strings.HasPrefix(text, "/")
		req.ack = make(chan error, 1)
	} else {
		s.rpcCompactID = id
	}
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
		if s.rpcCompactID == id {
			s.rpcCompactID = ""
		}
		s.rpcLifecycleMu.Unlock()
		s.rpcSendMu.Unlock()
		return core.SteerResult{}, err
	}
	s.rpcSendMu.Unlock()
	if req.ack == nil {
		// native compact completes on its correlated response
		return core.SteerResult{RequestID: id}, nil
	}
	// Prefer a non-blocking read of req.ack first. The reader may have
	// already delivered the ACK (and then triggered rpcDone by exiting);
	// a straight select{req.ack, rpcDone} could pseudo-randomly pick the
	// rpcDone branch and wrongly report EOF for a prompt that succeeded.
	select {
	case err := <-req.ack:
		return s.steerResultFor(req), err
	default:
	}
	select {
	case err := <-req.ack:
		return s.steerResultFor(req), err
	case <-s.rpcDone:
		// The reader may have delivered an ACK and then exited before Send
		// was scheduled. Do not replace an already-known result with EOF.
		select {
		case err := <-req.ack:
			return s.steerResultFor(req), err
		default:
		}
		return core.SteerResult{}, fmt.Errorf("pi: RPC process exited before prompt acknowledgement")
	case <-ctx.Done():
		// A canceled waiter is not a rejected request. Keep the buffered ACK slot:
		// the stdout reader must still apply a late disposition without blocking.
		select {
		case err := <-req.ack:
			return s.steerResultFor(req), err
		default:
		}
		return core.SteerResult{RequestID: id, Disposition: core.SteerUnconfirmed}, ctx.Err()
	case <-s.ctx.Done():
		// As with caller cancellation/EOF, preserve a known ACK rather than
		// letting randomized select turn accepted/rejected into unconfirmed.
		select {
		case err := <-req.ack:
			return s.steerResultFor(req), err
		default:
		}
		return core.SteerResult{RequestID: id, Disposition: core.SteerUnconfirmed}, s.ctx.Err()
	}
}

func (s *piSession) handleSettledRPCEvent(raw map[string]any) bool {
	switch raw["type"] {
	case "response":
		return s.handleSettledRPCResponse(raw)
	case "agent_start":
		s.rpcLifecycleMu.Lock()
		if !s.alive.Load() {
			s.rpcLifecycleMu.Unlock()
			return true
		}
		if !s.rpcTurnPending { // unsolicited runs use the same terminal contract
			s.rpcGeneration++
			s.rpcTurnPending = true
			// Record the generation this NEW run belongs to so the paired
			// agent_end → agent_settled only ends this run, not a newer one
			// started by a steer in the meantime. Only set when
			// starting a new generation; a continuation agent_start (same
			// generation, e.g. after a steer upgrade) must not overwrite
			// the settling gen recorded by the previous run's agent_end.
			s.rpcSettlingGen = s.rpcGeneration
		}
		s.rpcRunStarted = true
		s.rpcLifecycleMu.Unlock()
		return true
	case "agent_end":
		// Even willRetry=false can precede overflow recovery or follow-ups.
		// Record the generation this agent_end belongs to so the paired
		// agent_settled (which carries no generation) only ends this run,
		// not a newer one started by a steer in the meantime.
		s.rpcLifecycleMu.Lock()
		s.rpcSettlingGen = s.rpcGeneration
		s.rpcLifecycleMu.Unlock()
		s.handleAgentEnd(raw)
		return true
	case "agent_settled":
		// A stale duplicate between a new Send and agent_start cannot end
		// that new prompt. There is no remote run ID; arbitrary synthetic
		// events after a later agent_start require an extension contract.
		// Use the generation recorded by the preceding agent_end so a
		// stale agent_settled from the old run is rejected when a steer
		// with disposition=started has bumped the generation.
		s.rpcLifecycleMu.Lock()
		gen := s.rpcSettlingGen
		if gen == 0 {
			// No agent_end was seen (e.g. unsolicited run that skips
			// agent_end). Fall back to the current generation so normal
			// turns still end; the generation guard only matters when a
			// steer has bumped the generation mid-run.
			gen = s.rpcGeneration
		}
		// Clear after reading so a duplicate agent_settled (network jitter,
		// log buffering) cannot reuse the recorded generation. The
		// !rpcTurnPending guard in finishSettledRPCGeneration is the primary
		// defense; this is belt-and-suspenders.
		s.rpcSettlingGen = 0
		s.rpcLifecycleMu.Unlock()
		s.finishSettledRPCGeneration("", gen, true)
		return true
	case "compaction_start", "compaction_end", "auto_compaction_start", "auto_compaction_end":
		// A compaction is an internal operation, not a prompt boundary.
		// Recoverable compaction failures must not abort the engine's reader.
		// Native manual compact is completed by response(command=compact).
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
	command, _ := raw["command"].(string)
	s.rpcLifecycleMu.Lock()
	req := s.rpcRequests[id]
	if req == nil || req.command != command {
		s.rpcLifecycleMu.Unlock()
		return false // includes the existing get_state startup probe
	}
	delete(s.rpcRequests, id)
	success, _ := raw["success"].(bool)
	errMsg, _ := raw["error"].(string)
	data, _ := raw["data"].(map[string]any)
	disposition, _ := data["disposition"].(string)
	invalidProtocol := command == "prompt" && success && disposition != "started" && disposition != "queued" && disposition != "handled"
	if invalidProtocol {
		success = false
		errMsg = "pi: prompt response requires a valid disposition (Pi >= 0.99.0)"
	}
	if !success && errMsg == "" {
		errMsg = fmt.Sprintf("pi: %s request failed", command)
	}
	if command == "compact" {
		s.rpcCompactID = ""
	} else if !success && req.startsTurn && req.generation == s.rpcGeneration {
		s.rpcTurnPending = false
		s.rpcRunStarted = false
	}
	s.rpcLifecycleMu.Unlock()
	if command == "compact" {
		s.finishSettledRPCGeneration(errMsg, req.generation, false)
		return true
	}
	if !success {
		err := fmt.Errorf("%s", errMsg)
		// Pi 1.5.0 checks this before input handlers for plain prompts. Generic
		// success:false can follow extension side effects and is unsafe to replay.
		if req.plainPrompt && errMsg == "Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry." {
			req.ack <- &core.SteerNotAcceptedError{Err: err}
		} else {
			req.ack <- err
		}
		if invalidProtocol {
			// Preserve the #2196 ownership rule: a primary fails via Send;
			// a supplement must also release its acknowledged foreground run.
			s.alive.Store(false)
			s.failSettledRPCRequests(err)
			s.cancel()
		}
		return true
	}
	// success: true — record Pi's own verdict before releasing the waiter so
	// SendBusySteer can report "queued" vs "started" vs "handled".
	// Verified against a live Pi 1.5.0 RPC capture (2026-11-03):
	//   idle prompt  → data.disposition == "started"
	//   busy steer   → data.disposition == "queued"
	s.rpcLifecycleMu.Lock()
	req.disposition = disposition
	// Race fix: a steer sent while rpcTurnPending was
	// true gets startsTurn=false, but Pi may have already finished the old
	// run and reports disposition="started" — meaning it began a *new* run
	// for our input. Without this correction, the old run's agent_settled
	// (which may arrive right after this ACK) would clear rpcTurnPending and
	// emit EventResult, prematurely ending the new run the user just
	// started. Upgrade the request to a new generation so the stale
	// agent_settled's generation check (or the rpcRunStarted guard) rejects
	// it, and mark the run as not-yet-started so agent_settled waits for
	// the real agent_start.
	if disposition == "started" && !req.startsTurn {
		// If the old terminal was already emitted, core must consume a separate
		// turn. Otherwise this adapter suppresses that old terminal, so core must
		// keep reading the current turn rather than wait for an extra result.
		req.continuesTurn = s.rpcTurnPending && req.generation == s.rpcGeneration
		s.rpcGeneration++
		req.generation = s.rpcGeneration
		req.startsTurn = true
		s.rpcRunStarted = false
	}
	s.rpcLifecycleMu.Unlock()
	req.ack <- nil
	// Synchronous extensions/input handlers can consume a prompt without
	// starting a run. Fire-and-forget extensions need their own contract;
	// use native /compact rather than synthesizing agent_end on stdout.
	if disposition == "handled" && req.startsTurn {
		s.finishSettledRPCGeneration("", req.generation, false)
	}
	return true
}

// finishSettledRPCGeneration is called only by the stdout reader. pendingErr shares
// that owner; rpcTurnPending also protects against duplicate settled events.
// A terminal error is sufficient: core returns on EventError, so appending an
// EventResult would leave a stale terminal event for a queued request.
func (s *piSession) finishSettledRPCGeneration(errMsg string, generation uint64, requireRun bool) {
	s.rpcLifecycleMu.Lock()
	if !s.alive.Load() || !s.rpcTurnPending || s.rpcCompactID != "" ||
		(generation != 0 && generation != s.rpcGeneration) || (requireRun != s.rpcRunStarted) {
		s.rpcLifecycleMu.Unlock()
		return
	}
	s.rpcTurnPending = false
	s.rpcRunStarted = false
	s.rpcLifecycleMu.Unlock()
	if errMsg == "" {
		errMsg = s.pendingErr
	}
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

// Natural EOF does not cancel the session context. Release ACK waiters before
// reporting process death; otherwise core can block waiting for Send even after
// it has read a final event. An unacknowledged primary prompt fails via Send;
// an acknowledged run or manual compact fails via exactly one EventError.
func (s *piSession) failSettledRPCRequests(err error) {
	s.rpcLifecycleMu.Lock()
	pending := s.rpcTurnPending
	requests := s.rpcRequests
	s.rpcRequests = nil
	primaryACKPending := false
	for _, req := range requests {
		if req.ack != nil && req.startsTurn && req.generation == s.rpcGeneration {
			primaryACKPending = true
		}
	}
	s.rpcTurnPending = false
	s.rpcRunStarted = false
	s.rpcCompactID = ""
	s.rpcLifecycleMu.Unlock()
	for _, req := range requests {
		if req.ack != nil {
			req.ack <- err
		}
	}
	if pending && !primaryACKPending {
		select {
		case s.events <- core.Event{Type: core.EventError, Error: err}:
		case <-s.ctx.Done():
		}
	}
}
