package antigravity

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAntigravityEachTransportOwnsItsUsageMeter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Only the print transport reads and differences durable cumulative usage.
	previous := meterSessionForTest(t, "")
	previous.turnUsageEvent("same-session", agyUsage{InputTokens: 80, OutputTokens: 20})
	print := meterSessionForTest(t, "same-session")
	if !print.usageBaselineKnown || print.usageMeter == nil {
		t.Fatal("resumed print session did not restore its cumulative meter")
	}
	if delta := print.turnUsageEvent("same-session", agyUsage{InputTokens: 100, OutputTokens: 25}); delta.InputTokens != 20 || delta.OutputTokens != 5 {
		t.Fatalf("print session did not difference restored history: %+v", delta)
	}

	session := startAgyACPFixture(t)
	acp, ok := session.(*agyACPSession)
	if !ok {
		t.Fatal("fixture did not exercise ACP")
	}
	if acp.usageMeter != nil {
		t.Fatal("ACP initialized the print transport's cumulative meter")
	}
	if err := acp.Send("alive", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, acp, core.EventText)
	result := nextAgyACPEvent(t, acp, core.EventResult)
	if result.InputTokens != 1 || result.CacheReadInputTokens != 2 || result.OutputTokens != 3 || result.Sidechain != nil {
		t.Fatalf("ACP did not preserve native per-prompt usage: %+v", result)
	}
}
