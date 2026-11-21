package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// These tests replay source-derived Pi RPC records against the real adapter.
// Channels fix the command/ACK/event ordering; timeouts only detect a hang.
// No provider, installed Pi executable, or wall-clock delay is required.
type issue2161UnitWire struct {
	commands chan map[string]any
	writeErr error
}

func (w *issue2161UnitWire) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	var command map[string]any
	if err := json.Unmarshal(p, &command); err != nil {
		return 0, err
	}
	w.commands <- command
	return len(p), nil
}

func (w *issue2161UnitWire) Close() error { return nil }

func issue2161UnitSession(t *testing.T) (*piSession, *issue2161UnitWire) {
	t.Helper()
	s := newTestSession(true)
	s.rpcDone = make(chan struct{})
	s.sessionID.Store("issue2161-session")
	w := &issue2161UnitWire{commands: make(chan map[string]any, 8)}
	s.rpcStdin = w
	t.Cleanup(s.cancel)
	return s, w
}

func issue2161UnitStartSend(t *testing.T, s *piSession, w *issue2161UnitWire, prompt string) (map[string]any, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- s.sendRPC(prompt, nil, nil)
	}()
	t.Cleanup(func() {
		s.cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("RPC Send goroutine did not stop after cancellation")
		}
	})
	select {
	case command := <-w.commands:
		if id, _ := command["id"].(string); id == "" {
			t.Fatal("modern RPC command has no request ID")
		}
		return command, done
	case <-time.After(5 * time.Second):
		t.Fatal("RPC command was not written")
		return nil, nil
	}
}

func issue2161UnitResponse(command map[string]any, success bool, detail string) map[string]any {
	raw := map[string]any{"type": "response", "id": command["id"], "command": command["type"], "success": success}
	if success {
		raw["data"] = map[string]any{"disposition": detail}
	} else {
		raw["error"] = detail
	}
	return raw
}

func issue2161UnitWaitSend(t *testing.T, done <-chan error, wantError string) {
	t.Helper()
	select {
	case err := <-done:
		if wantError == "" {
			if err != nil {
				t.Fatalf("Send returned unexpected error: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("Send error = %v, want substring %q", err, wantError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after its correlated response")
	}
}

func issue2161UnitNoTerminal(t *testing.T, s *piSession, at string) []core.Event {
	t.Helper()
	events := drainEvents(s)
	for _, event := range events {
		if event.Type == core.EventResult || event.Type == core.EventError {
			t.Fatalf("premature terminal at %s: %+v", at, event)
		}
	}
	return events
}

func issue2161UnitText(text string) map[string]any {
	return map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": text}}
}

func issue2161UnitAssistant(errMsg string) map[string]any {
	return map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "errorMessage": errMsg}}
}

func TestIssue2161Lifecycle_SourceDerivedSequences(t *testing.T) {
	start := map[string]any{"type": "agent_start"}
	end := map[string]any{"type": "agent_end", "willRetry": false}
	settled := map[string]any{"type": "agent_settled"}
	tests := []struct {
		name      string
		beforeACK []map[string]any
		events    []map[string]any
		wantText  string
		wantError string
	}{
		{
			name:     "normal_agent_end_is_not_terminal",
			events:   []map[string]any{start, issue2161UnitText("done"), issue2161UnitAssistant(""), end, settled},
			wantText: "done",
		},
		{
			name: "automatic_compaction_before_first_agent_start",
			beforeACK: []map[string]any{
				{"type": "compaction_start", "reason": "threshold"},
				{"type": "compaction_end", "reason": "threshold", "willRetry": false},
			},
			events:   []map[string]any{start, issue2161UnitText("real answer"), issue2161UnitAssistant(""), end, settled},
			wantText: "real answer",
		},
		{
			name: "overflow_agent_end_false_then_compact_and_recover",
			events: []map[string]any{
				start, issue2161UnitAssistant("context overflow"), end,
				{"type": "compaction_start", "reason": "overflow"},
				{"type": "compaction_end", "reason": "overflow", "willRetry": true},
				start, issue2161UnitText("recovered"), issue2161UnitAssistant(""), end, settled,
			},
			wantText: "recovered",
		},
		{
			name: "threshold_compaction_false_still_has_queued_continuation",
			events: []map[string]any{
				start, issue2161UnitText("first"), issue2161UnitAssistant(""), end,
				{"type": "compaction_start", "reason": "threshold"},
				{"type": "compaction_end", "reason": "threshold", "willRetry": false},
				start, issue2161UnitText("second"), issue2161UnitAssistant(""), end, settled,
			},
			wantText: "firstsecond",
		},
		{
			name: "provider_retry_success_discards_transient_error",
			events: []map[string]any{
				start, issue2161UnitAssistant("429 overloaded"),
				{"type": "agent_end", "willRetry": true},
				{"type": "auto_retry_start", "attempt": 1, "maxAttempts": 3},
				start, issue2161UnitText("recovered"), issue2161UnitAssistant(""),
				{"type": "auto_retry_end", "success": true, "attempt": 1}, end, settled,
			},
			wantText: "recovered",
		},
		{
			name: "provider_retry_exhausted_emits_one_terminal_error",
			events: []map[string]any{
				start, issue2161UnitAssistant("429 overloaded"),
				{"type": "agent_end", "willRetry": true},
				{"type": "auto_retry_start", "attempt": 1, "maxAttempts": 1},
				start, issue2161UnitAssistant("429 retry exhausted"), end,
				{"type": "auto_retry_end", "success": false, "finalError": "429 retry exhausted"}, settled,
			},
			wantError: "429 retry exhausted",
		},
		{
			name: "compaction_failure_does_not_abort_a_recovering_prompt",
			beforeACK: []map[string]any{
				{"type": "compaction_start", "reason": "threshold"},
				{"type": "compaction_end", "reason": "threshold", "errorMessage": "Auto-compaction failed: overloaded", "willRetry": false},
			},
			events:   []map[string]any{start, issue2161UnitText("continued"), issue2161UnitAssistant(""), end, settled},
			wantText: "continued",
		},
		{
			name: "legacy_named_auto_compaction_is_not_terminal_in_modern_mode",
			beforeACK: []map[string]any{
				{"type": "auto_compaction_start", "reason": "threshold"},
				{"type": "auto_compaction_end", "reason": "threshold", "willRetry": false},
			},
			events:   []map[string]any{start, issue2161UnitText("done"), issue2161UnitAssistant(""), end, settled},
			wantText: "done",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, wire := issue2161UnitSession(t)
			command, sendDone := issue2161UnitStartSend(t, s, wire, "work")
			var all []core.Event
			for _, raw := range tc.beforeACK {
				s.handleEvent(raw)
				all = append(all, issue2161UnitNoTerminal(t, s, "before prompt ACK")...)
			}
			s.handleEvent(issue2161UnitResponse(command, true, "started"))
			issue2161UnitWaitSend(t, sendDone, "")
			all = append(all, issue2161UnitNoTerminal(t, s, "prompt ACK")...)
			for i, raw := range tc.events {
				s.handleEvent(raw)
				if i+1 < len(tc.events) {
					all = append(all, issue2161UnitNoTerminal(t, s, raw["type"].(string))...)
				} else {
					all = append(all, drainEvents(s)...)
				}
			}
			var text strings.Builder
			terminals := 0
			for _, event := range all {
				if event.Type == core.EventText {
					text.WriteString(event.Content)
				}
				if event.Type == core.EventResult || event.Type == core.EventError {
					terminals++
					if tc.wantError == "" {
						if event.Type != core.EventResult || !event.Done || event.SessionID != "issue2161-session" {
							t.Fatalf("terminal = %+v, want successful result with session ID", event)
						}
					} else if event.Type != core.EventError || event.Error == nil || event.Error.Error() != tc.wantError {
						t.Fatalf("terminal = %+v, want error %q", event, tc.wantError)
					}
				}
			}
			if terminals != 1 || text.String() != tc.wantText {
				t.Fatalf("terminals=%d text=%q, want one terminal and %q; events=%+v", terminals, text.String(), tc.wantText, all)
			}
			// Late duplicate notifications must not leak an empty result into
			// the idle reader or the next queued request.
			s.handleEvent(settled)
			s.handleEvent(end)
			s.handleEvent(map[string]any{"type": "compaction_end", "reason": "threshold"})
			issue2161UnitNoTerminal(t, s, "duplicate completion")
		})
	}
}

func TestIssue2161Lifecycle_PromptFailuresAreCorrelated(t *testing.T) {
	t.Run("initial_preflight_failure_is_Send_error_and_does_not_leave_a_terminal", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		command, done := issue2161UnitStartSend(t, s, wire, "work")
		s.handleEvent(issue2161UnitResponse(command, false, "No API key found"))
		issue2161UnitWaitSend(t, done, "No API key found")
		s.handleEvent(map[string]any{"type": "agent_settled"})
		issue2161UnitNoTerminal(t, s, "failed prompt has no active run")
		// A subsequent valid request must still complete independently.
		next, nextDone := issue2161UnitStartSend(t, s, wire, "retry")
		if next["id"] == command["id"] {
			t.Fatal("request ID reused")
		}
		s.handleEvent(issue2161UnitResponse(next, true, "started"))
		issue2161UnitWaitSend(t, nextDone, "")
		s.handleEvent(map[string]any{"type": "agent_start"})
		s.handleEvent(map[string]any{"type": "agent_settled"})
		events := drainEvents(s)
		if len(events) != 1 || events[0].Type != core.EventResult {
			t.Fatalf("new request did not complete: %+v", events)
		}
	})
	t.Run("rejected_supplement_does_not_kill_current_run", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		command, done := issue2161UnitStartSend(t, s, wire, "first")
		s.handleEvent(issue2161UnitResponse(command, true, "started"))
		issue2161UnitWaitSend(t, done, "")
		s.handleEvent(map[string]any{"type": "agent_start"})
		supplement, supplementDone := issue2161UnitStartSend(t, s, wire, "follow-up")
		s.handleEvent(issue2161UnitResponse(supplement, false, "input rejected"))
		issue2161UnitWaitSend(t, supplementDone, "input rejected")
		issue2161UnitNoTerminal(t, s, "rejected supplement")
		s.handleEvent(issue2161UnitText("original result"))
		s.handleEvent(issue2161UnitAssistant(""))
		s.handleEvent(map[string]any{"type": "agent_end", "willRetry": false})
		issue2161UnitNoTerminal(t, s, "original agent_end")
		s.handleEvent(map[string]any{"type": "agent_settled"})
		events := drainEvents(s)
		if len(events) != 1 || events[0].Type != core.EventResult {
			t.Fatalf("original run lost terminal after supplement rejection: %+v", events)
		}
	})
	t.Run("handled_disposition_does_not_wait_for_nonexistent_agent_start", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		command, done := issue2161UnitStartSend(t, s, wire, "/synchronous-command")
		s.handleEvent(issue2161UnitText("command result"))
		s.handleEvent(issue2161UnitResponse(command, true, "handled"))
		issue2161UnitWaitSend(t, done, "")
		events := drainEvents(s)
		if len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult {
			t.Fatalf("handled command did not complete exactly once: %+v", events)
		}
		s.handleEvent(issue2161UnitResponse(command, true, "handled"))
		issue2161UnitNoTerminal(t, s, "duplicate handled ACK")
	})
	t.Run("handled_supplement_keeps_original_run_open", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		s.handleEvent(map[string]any{"type": "agent_start"})
		command, done := issue2161UnitStartSend(t, s, wire, "/synchronous-command")
		s.handleEvent(issue2161UnitResponse(command, true, "handled"))
		issue2161UnitWaitSend(t, done, "")
		issue2161UnitNoTerminal(t, s, "handled supplement")
		s.handleEvent(map[string]any{"type": "agent_settled"})
		if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
			t.Fatalf("active run lost its terminal: %+v", events)
		}
	})
}

func TestIssue2161Lifecycle_SequentialTurnsAndUnsolicited(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	for _, prompt := range []string{"first", "second", "third"} {
		command, done := issue2161UnitStartSend(t, s, wire, prompt)
		s.handleEvent(issue2161UnitResponse(command, true, "started"))
		issue2161UnitWaitSend(t, done, "")
		s.handleEvent(map[string]any{"type": "agent_start"})
		s.handleEvent(issue2161UnitText(prompt))
		s.handleEvent(issue2161UnitAssistant(""))
		s.handleEvent(map[string]any{"type": "agent_end"})
		issue2161UnitNoTerminal(t, s, prompt+" agent_end")
		s.handleEvent(map[string]any{"type": "agent_settled"})
		if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
			t.Fatalf("turn %q: terminal=%+v", prompt, events)
		}
	}
	// Idle extension-triggered runs remain observable, once each.
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(issue2161UnitText("unsolicited"))
	s.handleEvent(issue2161UnitAssistant(""))
	s.handleEvent(map[string]any{"type": "agent_settled"})
	events := drainEvents(s)
	if len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult {
		t.Fatalf("unsolicited run events=%+v", events)
	}
}

func TestIssue2161Lifecycle_LateHandledACKCannotCloseNewGeneration(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	oldCommand, oldDone := issue2161UnitStartSend(t, s, wire, "old request")
	// Explicitly exercise an out-of-order response from a wrapper/extension:
	// old run completion is observed before its command ACK is delivered.
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("old generation did not finish: %+v", events)
	}
	newCommand, newDone := issue2161UnitStartSend(t, s, wire, "new request")
	s.handleEvent(issue2161UnitResponse(newCommand, true, "started"))
	issue2161UnitWaitSend(t, newDone, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	// The late handled ACK belongs only to the earlier request generation.
	s.handleEvent(issue2161UnitResponse(oldCommand, true, "handled"))
	issue2161UnitWaitSend(t, oldDone, "")
	issue2161UnitNoTerminal(t, s, "late handled ACK from old generation")
	s.handleEvent(issue2161UnitText("new result"))
	s.handleEvent(issue2161UnitAssistant(""))
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult {
		t.Fatalf("new generation lost ownership: %+v", events)
	}
}

func TestIssue2161Lifecycle_DuplicateSettledBeforeNextAgentStart(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	first, firstDone := issue2161UnitStartSend(t, s, wire, "first")
	s.handleEvent(issue2161UnitResponse(first, true, "started"))
	issue2161UnitWaitSend(t, firstDone, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
		t.Fatalf("first turn did not finish: %+v", events)
	}
	second, secondDone := issue2161UnitStartSend(t, s, wire, "second")
	// A queued Send has reserved ownership, but its run has not started.
	// A duplicated previous terminal must not manufacture an empty reply.
	s.handleEvent(map[string]any{"type": "agent_settled"})
	issue2161UnitNoTerminal(t, s, "stale settled before second agent_start")
	s.handleEvent(issue2161UnitResponse(second, true, "started"))
	issue2161UnitWaitSend(t, secondDone, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(issue2161UnitText("second result"))
	s.handleEvent(issue2161UnitAssistant(""))
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 2 || events[0].Type != core.EventText || events[1].Type != core.EventResult {
		t.Fatalf("second turn did not finish exactly once: %+v", events)
	}
}

func TestIssue2161Lifecycle_ReadLoopEOFResolvesPendingWork(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prompt       string
		acknowledged bool
		settled      bool
		wantTerminal bool
	}{
		{"prompt_ack_missing", "work", false, false, false},
		{"accepted_prompt_exits_mid_run", "work", true, false, true},
		{"already_settled_no_duplicate_error", "work", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, wire := issue2161UnitSession(t)
			command, sendDone := issue2161UnitStartSend(t, s, wire, tc.prompt)
			if tc.acknowledged {
				s.handleEvent(issue2161UnitResponse(command, true, "started"))
				issue2161UnitWaitSend(t, sendDone, "")
				s.handleEvent(map[string]any{"type": "agent_start"})
			}
			if tc.settled {
				s.handleEvent(map[string]any{"type": "agent_settled"})
				if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
					t.Fatalf("initial completion=%+v", events)
				}
			}
			issue2161UnitNoTerminal(t, s, "before natural EOF")
			// This is the real RPC stdout reader, not a direct call to the
			// lifecycle failure helper. EOF occurs without cancelling ctx.
			s.wg.Add(1)
			go s.readLoopRPC(io.NopCloser(strings.NewReader("")))
			select {
			case <-s.rpcDone:
			case <-time.After(5 * time.Second):
				t.Fatal("readLoopRPC did not finish on natural EOF")
			}
			s.wg.Wait()
			if !tc.acknowledged {
				issue2161UnitWaitSend(t, sendDone, "RPC process exited")
			}
			if s.Alive() {
				t.Fatal("EOF left the session alive")
			}
			if s.ctx.Err() != nil {
				t.Fatalf("test must exercise natural EOF, not cancellation: %v", s.ctx.Err())
			}
			events := drainEvents(s)
			if !tc.wantTerminal {
				if len(events) != 0 {
					t.Fatalf("EOF appended a stale/duplicate terminal: %+v", events)
				}
			} else if len(events) != 1 || events[0].Type != core.EventError || events[0].Error == nil || !strings.Contains(events[0].Error.Error(), "RPC process exited") {
				t.Fatalf("EOF must emit exactly one terminal error, got %+v", events)
			}
		})
	}
}

func TestIssue2161Lifecycle_JSONModeKeepsProcessExitBoundary(t *testing.T) {
	s := newTestSession(false)
	t.Cleanup(s.cancel)
	for _, raw := range []map[string]any{
		{"type": "compaction_start", "reason": "threshold"},
		{"type": "compaction_end", "reason": "threshold"},
		{"type": "agent_start"},
		{"type": "agent_end", "willRetry": false},
		{"type": "agent_settled"},
	} {
		s.handleEvent(raw)
		issue2161UnitNoTerminal(t, s, "JSON event before process exit")
	}
}

func TestIssue2161Lifecycle_WriteFailureAndCancellationUnblockSend(t *testing.T) {
	t.Run("write_failure_does_not_poison_next_send", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		wire.writeErr = errors.New("closed stdin")
		if err := s.sendRPC("first", nil, nil); err == nil || !strings.Contains(err.Error(), "closed stdin") {
			t.Fatalf("write failure=%v", err)
		}
		wire.writeErr = nil
		command, done := issue2161UnitStartSend(t, s, wire, "retry")
		s.handleEvent(issue2161UnitResponse(command, true, "started"))
		issue2161UnitWaitSend(t, done, "")
		s.handleEvent(map[string]any{"type": "agent_start"})
		s.handleEvent(map[string]any{"type": "agent_settled"})
		if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult {
			t.Fatalf("turn after write error=%+v", events)
		}
	})
	t.Run("cancel_waiting_for_ack", func(t *testing.T) {
		s, wire := issue2161UnitSession(t)
		_, done := issue2161UnitStartSend(t, s, wire, "work")
		s.cancel()
		issue2161UnitWaitSend(t, done, "context canceled")
	})
}

func TestIssue2161Lifecycle_HandledACKKeepsAnAlreadyStartedRunOpen(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	command, done := issue2161UnitStartSend(t, s, wire, "/ask")
	// An extension can start a run before acknowledging its outer command.
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(issue2161UnitResponse(command, true, "handled"))
	issue2161UnitWaitSend(t, done, "")
	issue2161UnitNoTerminal(t, s, "handled ACK after agent_start")
	s.handleEvent(issue2161UnitText("answer"))
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 2 || events[1].Type != core.EventResult {
		t.Fatalf("extension run did not finish at settled: %+v", events)
	}
}

func TestIssue2161Lifecycle_InvalidDispositionFailsWithoutHanging(t *testing.T) {
	for _, supplement := range []bool{false, true} {
		t.Run(fmt.Sprintf("supplement_%t", supplement), func(t *testing.T) {
			s, wire := issue2161UnitSession(t)
			if supplement {
				s.handleEvent(map[string]any{"type": "agent_start"})
			}
			command, done := issue2161UnitStartSend(t, s, wire, "work")
			s.handleEvent(issue2161UnitResponse(command, true, ""))
			issue2161UnitWaitSend(t, done, "disposition")
			if s.Alive() || s.ctx.Err() == nil {
				t.Fatal("invalid protocol must stop the incompatible peer")
			}
			events := drainEvents(s)
			if supplement {
				if len(events) != 1 || events[0].Type != core.EventError {
					t.Fatalf("foreground turn must be released by one error: %+v", events)
				}
			} else if len(events) != 0 {
				t.Fatalf("primary Send failure must not leave a stale terminal: %+v", events)
			}
		})
	}
}
