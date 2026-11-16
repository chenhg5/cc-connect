package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func assistantEnd(usage map[string]any, errMsg string) map[string]any {
	msg := map[string]any{"role": "assistant", "usage": usage}
	if errMsg != "" {
		msg["errorMessage"] = errMsg
	}
	return map[string]any{"type": "message_end", "message": msg}
}

// Every model call of a turn -- tool-call rounds and a retried failure
// alike -- is billed once on the turn's EventResult, and the next turn
// starts from zero.
func TestTurnResultCarriesTheTurnsTokenUsage(t *testing.T) {
	s := newTestSession(true) // rpc=true
	defer s.cancel()

	s.handleEvent(assistantEnd(map[string]any{
		"input": 1468.0, "output": 44.0, "cacheRead": 256.0, "cacheWrite": 0.0,
		"totalTokens": 1768.0, "reasoning": 20.0, "cacheWrite1h": 0.0,
	}, ""))
	s.handleEvent(assistantEnd(map[string]any{"input": 7.0, "output": 0.0}, "429 rate_limit_error"))
	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": true, "messages": []any{}})
	s.handleEvent(map[string]any{"type": "message_end", "message": map[string]any{
		"role": "toolResult", "toolName": "bash", "content": []any{},
		"usage": map[string]any{"input": 999.0},
	}})
	s.handleEvent(assistantEnd(map[string]any{
		"input": 1526.0, "output": 3.0, "cacheRead": 1408.0, "cacheWrite": 100.0,
	}, ""))
	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": false, "messages": []any{}})

	var result *core.Event
	for _, evt := range drainEvents(s) {
		if evt.Type == core.EventResult {
			evt := evt
			result = &evt
		}
	}
	if result == nil {
		t.Fatal("no EventResult")
	}
	got := [4]int{result.InputTokens, result.OutputTokens, result.CacheReadInputTokens, result.CacheCreationInputTokens}
	if want := [4]int{1468 + 7 + 1526, 44 + 3, 256 + 1408, 100}; got != want {
		t.Fatalf("turn usage = %v, want %v", got, want)
	}

	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": false, "messages": []any{}})
	for _, evt := range drainEvents(s) {
		if evt.Type == core.EventResult && evt.InputTokens+evt.OutputTokens != 0 {
			t.Fatalf("next turn must start from zero, got %+v", evt)
		}
	}
}
