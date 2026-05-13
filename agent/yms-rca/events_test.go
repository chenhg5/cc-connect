package ymsagent

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func drainEvents(t *testing.T, s *session, d time.Duration) []core.Event {
	t.Helper()
	deadline := time.Now().Add(d)
	var out []core.Event
	for time.Now().Before(deadline) {
		select {
		case e, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-time.After(10 * time.Millisecond):
		}
	}
	return out
}

func TestHandleEvent_TextDelta(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": "hello",
		},
	})
	events := drainEvents(t, s, 100*time.Millisecond)
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "hello" {
		t.Fatalf("got: %+v", events)
	}
}

func TestHandleEvent_ThinkingFlushOnEnd(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": "abc"},
	})
	// no event yet
	events := drainEvents(t, s, 50*time.Millisecond)
	if len(events) != 0 {
		t.Fatalf("thinking_delta should not emit immediately; got %+v", events)
	}
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_end"},
	})
	events = drainEvents(t, s, 100*time.Millisecond)
	if len(events) != 1 || events[0].Type != core.EventThinking || events[0].Content != "abc" {
		t.Fatalf("got: %+v", events)
	}
}

func TestHandleEvent_GetStateLearnsSessionID(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "response", "command": "get_state", "success": true,
		"data": map[string]any{"sessionId": "abc-123"},
	})
	if got := s.CurrentSessionID(); got != "abc-123" {
		t.Errorf("session id not learned, got %q", got)
	}
}

func TestHandleEvent_GetSessionStats(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "response", "command": "get_session_stats", "success": true,
		"data": map[string]any{
			"contextUsage": map[string]any{"tokens": 1234.0, "contextWindow": 200000.0, "percent": 0.6},
			"tokens":       map[string]any{"total": 5000.0, "input": 3000.0, "output": 2000.0, "cacheRead": 100.0},
		},
	})
	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("usage was nil")
	}
	if usage.UsedTokens != 1234 || usage.ContextWindow != 200000 {
		t.Errorf("contextUsage map wrong: %+v", usage)
	}
	if usage.InputTokens != 3000 || usage.OutputTokens != 2000 || usage.CachedInputTokens != 100 || usage.TotalTokens != 5000 {
		t.Errorf("tokens map wrong: %+v", usage)
	}
}

func TestHandleEvent_PromptFailure(t *testing.T) {
	// On prompt failure, expect both EventError (with message) AND
	// EventResult{Done:true} so the engine can finalize the turn. busy
	// must also be cleared.
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "success": false,
		"data": map[string]any{"error": "boom"},
	})
	events := drainEvents(t, s, 100*time.Millisecond)
	var gotErr, gotResult bool
	for _, e := range events {
		if e.Type == core.EventError {
			gotErr = true
		}
		if e.Type == core.EventResult && e.Done {
			gotResult = true
		}
	}
	if !gotErr {
		t.Errorf("missing EventError; got %+v", events)
	}
	if !gotResult {
		t.Errorf("missing EventResult{Done:true} on prompt failure; turn would never finalize")
	}
	if s.busy.Load() {
		t.Errorf("busy not cleared after prompt failure")
	}
}

func TestHandleEvent_AgentEnd(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.sessionID.Store("sid-1")
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.handleEvent(map[string]any{
		"type":  "agent_end",
		"usage": map[string]any{"input": 100.0, "output": 50.0},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	var got *core.Event
	for i := range evts {
		if evts[i].Type == core.EventResult {
			got = &evts[i]
		}
	}
	if got == nil {
		t.Fatalf("no EventResult; got=%+v", evts)
	}
	if got.SessionID != "sid-1" || got.InputTokens != 100 || got.OutputTokens != 50 {
		t.Errorf("unexpected: %+v", got)
	}
	// agent_end does NOT clear busy by itself — turn_end is the universal
	// turn boundary. Subsequent turn_end should be the one that clears it.
	if !s.busy.Load() {
		t.Error("busy must remain set after agent_end (cleared on turn_end)")
	}
	s.handleEvent(map[string]any{"type": "turn_end"})
	if s.busy.Load() {
		t.Error("busy not cleared on turn_end after agent_end")
	}
}

func TestHandleEvent_ToolcallEndWithoutStartEmits(t *testing.T) {
	// When tool_execution_start is NOT seen first, toolcall_end should
	// still emit EventToolUse via the message_update path (covers
	// extractToolInput / emitToolFromMessage end-to-end).
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": 0.0,
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type":      "toolCall",
						"id":        "tc-direct",
						"name":      "grep",
						"arguments": map[string]any{"pattern": "TODO"},
					},
				},
			},
		},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventToolUse {
		t.Fatalf("got %+v", evts)
	}
	if evts[0].ToolName != "grep" || evts[0].ToolInput != "TODO" {
		t.Errorf("event payload wrong: %+v", evts[0])
	}
}

func TestHandleEvent_ToolExecutionStartEnd_Dedup(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "tool_execution_start", "toolCallId": "tc-1", "toolName": "bash",
		"arguments": map[string]any{"command": "ls -la"},
	})
	// message_update / toolcall_end carrying same toolCallId should be skipped.
	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": 0.0,
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type":      "toolCall",
						"id":        "tc-1",
						"name":      "bash",
						"arguments": map[string]any{"command": "ls -la"},
					},
				},
			},
		},
	})
	// tool_execution_end
	s.handleEvent(map[string]any{
		"type": "tool_execution_end", "toolCallId": "tc-1", "toolName": "bash",
		"result": "files", "success": true,
	})
	// message_end role=toolResult with same id should be skipped.
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "toolResult",
			"toolName":   "bash",
			"toolCallId": "tc-1",
			"content":    []any{map[string]any{"text": "files"}},
		},
	})

	evts := drainEvents(t, s, 100*time.Millisecond)
	var uses, results int
	for _, e := range evts {
		if e.Type == core.EventToolUse {
			uses++
		}
		if e.Type == core.EventToolResult {
			results++
		}
	}
	if uses != 1 {
		t.Errorf("expected 1 EventToolUse (dedup), got %d", uses)
	}
	if results != 1 {
		t.Errorf("expected 1 EventToolResult (dedup), got %d", results)
	}
}

func TestHandleEvent_CustomYmsCommandStrips(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"display":    true,
			"customType": "yms-command",
			"content":    []any{map[string]any{"text": "\x1b[31mred\x1b[0m\n\n\n\n"}},
		},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventText {
		t.Fatalf("got %+v", evts)
	}
	if strings.Contains(evts[0].Content, "\x1b") {
		t.Errorf("ANSI not stripped: %q", evts[0].Content)
	}
}

func TestHandleEvent_NotifyError(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "extension_ui_request", "id": "n1", "method": "notify",
		"notifyType": "error", "message": "oops",
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventError {
		t.Fatalf("got %+v", evts)
	}
}

func TestHandleEvent_SelectCancels(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "extension_ui_request", "id": "s1", "method": "select",
	})
	frames := enc.framesCopy()
	if len(frames) != 1 || frames[0]["cancelled"] != true || frames[0]["id"] != "s1" {
		t.Fatalf("expected cancelled frame; got %+v", frames)
	}
}

func TestHandleEvent_NonResponseAssistantError(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":         "assistant",
			"errorMessage": "boom",
		},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventError {
		t.Fatalf("got %+v", evts)
	}
}

func TestHandleEvent_ToolExecutionUpdate_ProgressAsThinking(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type":       "tool_execution_update",
		"toolCallId": "tc-u",
		"progress":   "step 2/5",
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventThinking || evts[0].Content != "step 2/5" {
		t.Fatalf("got %+v", evts)
	}
}

func TestHandleEvent_ToolExecutionUpdate_EmptyDropped(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type":       "tool_execution_update",
		"toolCallId": "tc-empty",
	})
	evts := drainEvents(t, s, 50*time.Millisecond)
	if len(evts) != 0 {
		t.Fatalf("empty progress should be dropped; got %+v", evts)
	}
}

func TestHandleEvent_ExtensionError(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type":    "extension_error",
		"message": "extension blew up",
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventError {
		t.Fatalf("got %+v", evts)
	}
	if evts[0].Error == nil || !strings.Contains(evts[0].Error.Error(), "extension blew up") {
		t.Errorf("error text wrong: %v", evts[0].Error)
	}
}

func TestHandleEvent_ContextUsage_NilSafe_KeepsPrevious(t *testing.T) {
	s, _ := newTestSession(t, "default")
	// Prime with a real value.
	s.handleEvent(map[string]any{
		"type": "response", "command": "get_session_stats", "success": true,
		"data": map[string]any{
			"contextUsage": map[string]any{"tokens": 1000.0, "contextWindow": 200000.0},
		},
	})
	first := s.GetContextUsage()
	if first == nil || first.UsedTokens != 1000 {
		t.Fatalf("priming failed: %+v", first)
	}

	// Second event with tokens=nil (compaction in progress) — must NOT clobber.
	s.handleEvent(map[string]any{
		"type": "response", "command": "get_session_stats", "success": true,
		"data": map[string]any{
			"contextUsage": map[string]any{"tokens": nil, "contextWindow": 200000.0},
		},
	})
	second := s.GetContextUsage()
	if second == nil || second.UsedTokens != 1000 {
		t.Errorf("compaction nil clobbered previous usage; got %+v", second)
	}
}

func TestHandleEvent_CustomEnvSwitch_DisplayTrueEmitsText(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"display":    true,
			"customType": "yms-rca.env-switch",
			"content":    []any{map[string]any{"text": "switched to pre"}},
		},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventText {
		t.Fatalf("display=true should emit EventText; got %+v", evts)
	}
}

func TestHandleEvent_CustomEnvSwitch_DisplayFalseEmitsThinking(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"display":    false,
			"customType": "yms-rca.env-switch",
			"content":    []any{map[string]any{"text": "internal"}},
		},
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventThinking {
		t.Fatalf("display=false should emit EventThinking; got %+v", evts)
	}
}

func TestHandleEvent_AgentEnd_NoUsage(t *testing.T) {
	// agent_end without a usage block still emits EventResult (with zero
	// token counts) but does NOT clear busy — that happens at turn_end.
	s, _ := newTestSession(t, "default")
	s.sessionID.Store("sid-no-usage")
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.handleEvent(map[string]any{"type": "agent_end"})
	evts := drainEvents(t, s, 100*time.Millisecond)
	var ev *core.Event
	for i := range evts {
		if evts[i].Type == core.EventResult {
			ev = &evts[i]
		}
	}
	if ev == nil {
		t.Fatalf("missing EventResult: %+v", evts)
	}
	if ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.SessionID != "sid-no-usage" {
		t.Errorf("unexpected: %+v", ev)
	}
	if !s.busy.Load() {
		t.Error("busy must remain set after agent_end (cleared on turn_end)")
	}
}

func TestHandleEvent_UnknownTypeIgnored(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{"type": "totally_made_up"})
	evts := drainEvents(t, s, 50*time.Millisecond)
	if len(evts) != 0 {
		t.Fatalf("unknown type should not emit; got %+v", evts)
	}
}

func TestHandleEvent_ExtensionUI_NotifyPlain(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.handleEvent(map[string]any{
		"type": "extension_ui_request", "id": "n2", "method": "notify",
		"message": "fyi",
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	if len(evts) != 1 || evts[0].Type != core.EventThinking || evts[0].Content != "fyi" {
		t.Fatalf("got %+v", evts)
	}
}

// Regression for code-review HIGH (round 2): a slash-command turn produces:
//
//  1. response/prompt success=true  → set promptAcked, busy STAYS true
//     (clearing busy here would race with a concurrent Send — see
//     TestHandleEvent_RaceWithSendBetweenAckAndMessageEnd below).
//  2. message_end role=custom customType=yms-command → emit EventText
//     then emit EventResult (latched) and clear busy.
//
// pi-rpc does NOT emit turn_end / agent_end for slash commands, so
// without this terminator the engine would never see EventResult.
func TestHandleEvent_SlashCommandTurnTerminates(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.promptAcked.Store(false)
	s.currentPromptID.Store("cc-1")

	// 1. response/prompt success=true arrives FIRST in real protocol.
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "id": "cc-1", "success": true,
	})
	// Busy MUST remain set — clearing here opens the race the reviewer flagged.
	if !s.busy.Load() {
		t.Error("busy cleared too early on response/prompt — would race with a concurrent Send")
	}
	if !s.promptAcked.Load() {
		t.Error("promptAcked not set on response/prompt")
	}
	// Result not yet emitted — trailing text still pending.
	evts := drainEvents(t, s, 50*time.Millisecond)
	for _, e := range evts {
		if e.Type == core.EventResult {
			t.Errorf("Result emitted too early — would arrive before EventText: %+v", e)
		}
	}

	// 2. message_end with the slash command result.
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"display":    true,
			"customType": "yms-command",
			"content":    []any{map[string]any{"text": "confirmed=false"}},
		},
	})
	evts = drainEvents(t, s, 100*time.Millisecond)
	var idxText, idxResult = -1, -1
	for i, e := range evts {
		if e.Type == core.EventText {
			idxText = i
		}
		if e.Type == core.EventResult && e.Done {
			idxResult = i
		}
	}
	if idxText < 0 {
		t.Errorf("missing EventText: %+v", evts)
	}
	if idxResult < 0 {
		t.Errorf("missing EventResult{Done:true}: %+v", evts)
	}
	if idxText >= 0 && idxResult >= 0 && idxText > idxResult {
		t.Errorf("Result emitted before Text — engine would lose the slash-command output. Order: %+v", evts)
	}
	if s.busy.Load() {
		t.Error("busy not cleared at terminator message_end — next Send would be refused")
	}
}

// Regression for code-review HIGH (round 2): if busy were cleared on
// response/prompt success=true, a concurrent Send could pass the CAS
// between the ack and the trailing message_end, resetting promptAcked
// and causing the original turn's terminal EventResult to be dropped.
//
// With the fix, busy stays true until message_end fires, so the
// concurrent Send is correctly refused.
func TestHandleEvent_RaceWithSendBetweenAckAndMessageEnd(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.workDir = t.TempDir()
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.promptAcked.Store(false)
	s.currentPromptID.Store("cc-1")

	// 1. response/prompt success=true (current-turn id) arrives.
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "id": "cc-1", "success": true,
	})

	// 2. Racing Send call BEFORE the terminal message_end — must be refused
	//    because the previous turn isn't fully done yet.
	if err := s.Send("racy", nil, nil); err == nil {
		t.Fatal("racing Send between ack and message_end was NOT refused — race window open, original turn's Result would be lost")
	}

	// 3. promptAcked must still be true — no Send reset it.
	if !s.promptAcked.Load() {
		t.Error("promptAcked was clobbered (by a leaked Send?)")
	}

	// 4. message_end fires the actual terminator.
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "custom",
			"display":    true,
			"customType": "yms-command",
			"content":    []any{map[string]any{"text": "ok"}},
		},
	})

	evts := drainEvents(t, s, 100*time.Millisecond)
	var gotResult bool
	for _, e := range evts {
		if e.Type == core.EventResult && e.Done {
			gotResult = true
		}
	}
	if !gotResult {
		t.Error("EventResult missing after message_end — turn never finalized")
	}
	if s.busy.Load() {
		t.Error("busy not cleared after message_end")
	}
}

// Regression: a late `response command=prompt` from a prior turn must
// not mutate the current turn's promptAcked / busy. Without the
// stale-ack guard, a delayed ack with id="cc-1" could flip promptAcked
// during Turn N+1 (currentPromptID="cc-2"), allowing the next yms-command
// message_end to prematurely finalize.
func TestHandleEvent_StalePromptAckIgnored(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.promptAcked.Store(false)
	s.currentPromptID.Store("cc-2") // we're on turn 2

	// A delayed ack for turn 1 arrives.
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "id": "cc-1", "success": true,
	})
	if s.promptAcked.Load() {
		t.Error("stale prompt ack (id=cc-1) was honored on turn id=cc-2 — guard failed")
	}
	if !s.busy.Load() {
		t.Error("stale ack incorrectly cleared busy")
	}
}

// Regression: an LLM turn (where agent_end fires BEFORE response/prompt)
// must emit Result via agent_end with correct token usage. The later
// response/prompt must be a no-op for Result (already latched).
func TestHandleEvent_LLMTurn_AgentEndBeatsResponsePrompt(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.promptAcked.Store(false)

	s.handleEvent(map[string]any{
		"type":  "agent_end",
		"usage": map[string]any{"input": 200.0, "output": 80.0},
	})
	s.handleEvent(map[string]any{"type": "turn_end"})
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "success": true,
	})

	evts := drainEvents(t, s, 100*time.Millisecond)
	var results []core.Event
	for _, e := range evts {
		if e.Type == core.EventResult {
			results = append(results, e)
		}
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 Result (latch), got %d: %+v", len(results), results)
	}
	if results[0].InputTokens != 200 || results[0].OutputTokens != 80 {
		t.Errorf("token usage from agent_end lost: %+v", results[0])
	}
	if s.busy.Load() {
		t.Error("busy not cleared")
	}
}

// Regression for code-review HIGH: a slash-command turn (no agent_end)
// must still clear busy and emit EventResult on turn_end, otherwise the
// next Send is permanently refused with "previous turn still running".
func TestHandleEvent_TurnEndClearsBusyForSlashCommands(t *testing.T) {
	s, _ := newTestSession(t, "default")
	// Simulate a turn in progress (Send would have set this).
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)

	// No agent_end (slash command) — just turn_end.
	s.handleEvent(map[string]any{"type": "turn_end"})

	if s.busy.Load() {
		t.Error("busy not cleared on turn_end — slash-command turn would wedge the session")
	}
	// And we should see EventResult{Done:true}.
	evts := drainEvents(t, s, 100*time.Millisecond)
	var done bool
	for _, e := range evts {
		if e.Type == core.EventResult && e.Done {
			done = true
		}
	}
	if !done {
		t.Errorf("expected EventResult{Done:true} on turn_end; got %+v", evts)
	}
}

// Regression: an LLM turn (agent_end then turn_end) emits EventResult EXACTLY
// once, with token usage from agent_end. turn_end must not double-emit.
func TestHandleEvent_AgentEndThenTurnEnd_SingleResult(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.turnResultEmitted.Store(false)
	s.sessionID.Store("sid-1")

	s.handleEvent(map[string]any{
		"type":  "agent_end",
		"usage": map[string]any{"input": 100.0, "output": 50.0},
	})
	s.handleEvent(map[string]any{"type": "turn_end"})

	if s.busy.Load() {
		t.Error("busy not cleared")
	}
	evts := drainEvents(t, s, 100*time.Millisecond)
	var results []core.Event
	for _, e := range evts {
		if e.Type == core.EventResult {
			results = append(results, e)
		}
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 EventResult, got %d: %+v", len(results), results)
	}
	if results[0].InputTokens != 100 || results[0].OutputTokens != 50 {
		t.Errorf("token usage lost: %+v", results[0])
	}
}

// Regression: Send resets the turn-emit latch so a second turn after a
// first one (which emitted via agent_end) still emits its own EventResult.
func TestSend_ResetsTurnEmitLatch(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.workDir = t.TempDir()

	// First turn: agent_end + turn_end (latch becomes true).
	if err := s.Send("first", nil, nil); err != nil {
		t.Fatalf("Send first: %v", err)
	}
	s.handleEvent(map[string]any{"type": "agent_end"})
	s.handleEvent(map[string]any{"type": "turn_end"})
	_ = drainEvents(t, s, 100*time.Millisecond)

	// Second turn — Send should reset the latch so result can fire again.
	if err := s.Send("second", nil, nil); err != nil {
		t.Fatalf("Send second: %v", err)
	}
	if s.turnResultEmitted.Load() {
		t.Error("Send did not reset turnResultEmitted for new turn")
	}
	s.handleEvent(map[string]any{"type": "turn_end"})
	evts := drainEvents(t, s, 100*time.Millisecond)
	var done bool
	for _, e := range evts {
		if e.Type == core.EventResult && e.Done {
			done = true
		}
	}
	if !done {
		t.Errorf("second turn's EventResult missing: %+v", evts)
	}
}

func TestHandleEvent_PromptFailureUsesErrorMessage(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.busy.Store(true)
	s.handleEvent(map[string]any{
		"type": "response", "command": "prompt", "success": false,
		"errorMessage": "rate limit",
	})
	evts := drainEvents(t, s, 100*time.Millisecond)
	var errEvt *core.Event
	for i := range evts {
		if evts[i].Type == core.EventError {
			errEvt = &evts[i]
		}
	}
	if errEvt == nil {
		t.Fatalf("missing EventError: %+v", evts)
	}
	if !strings.Contains(errEvt.Error.Error(), "rate limit") {
		t.Errorf("error text: %v", errEvt.Error)
	}
}
