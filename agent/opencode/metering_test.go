package opencode

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestStepUsageIncludesEveryRequestOnce(t *testing.T) {
	s := &opencodeSession{ctx: context.Background(), events: make(chan core.Event, 8)}
	for _, raw := range []string{
		`{"part":{"id":"step1","reason":"tool-calls","tokens":{"input":100,"output":10,"reasoning":4,"cache":{"read":200,"write":50}}}}`,
		`{"part":{"id":"step1","reason":"tool-calls","tokens":{"input":100,"output":10,"reasoning":4,"cache":{"read":200,"write":50}}}}`,
		`{"part":{"id":"step2","reason":"stop","tokens":{"input":200,"output":20,"reasoning":5,"cache":{"read":300,"write":60}}}}`,
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		s.handleStepFinish(event)
	}
	result := <-s.events
	if result.InputTokens != 300 || result.OutputTokens != 39 || result.CacheReadInputTokens != 500 || result.CacheCreationInputTokens != 110 || result.ReasoningOutputTokens != 9 {
		t.Fatalf("step usage lost or counted twice: %+v", result)
	}
}
