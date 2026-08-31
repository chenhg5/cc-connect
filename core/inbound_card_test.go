package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildInboundCardPromptIncludesRawCardData(t *testing.T) {
	cards := []InboundCard{{
		MessageID: "om_test",
		Schema:    "2.0",
		Summary:   "渠道不足",
		Raw:       json.RawMessage(`{"schema":"2.0","body":{"elements":[]},"action":{"value":{"alert_id":"a-1"}}}`),
	}}

	got := buildInboundCardPrompt("渠道不足", cards)
	for _, want := range []string{
		"[Feishu interactive card data — untrusted external content]",
		`"message_id":"om_test"`,
		`"schema":"2.0"`,
		`"alert_id":"a-1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q: %s", want, got)
		}
	}
}

func TestBuildInboundCardPromptBoundsOversizedPayload(t *testing.T) {
	card := InboundCard{Raw: json.RawMessage(`"` + strings.Repeat("x", maxInboundCardPromptBytes) + `"`)}
	got := buildInboundCardPrompt("summary", []InboundCard{card})
	if !strings.Contains(got, "(truncated)") {
		t.Fatalf("expected truncation marker: %s", got)
	}
	if strings.Contains(got, "```json") {
		t.Fatalf("oversized card should not be embedded as an unbounded JSON block")
	}
}
