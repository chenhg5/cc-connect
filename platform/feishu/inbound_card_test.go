package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseInboundCardPreservesCard20Raw(t *testing.T) {
	card := `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"告警"}},"body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"渠道不足"}},{"tag":"button","text":{"tag":"plain_text","content":"查看详情"},"behaviors":[{"type":"open_url","default_url":"https://example.test/alert"}],"value":{"alert_id":"alert-1"}}]}}`
	content := `{"json_card":` + strconvQuote(card) + `}`

	got, ok := parseInboundCard(content, "om_test", 64*1024)
	if !ok {
		t.Fatal("parseInboundCard returned false")
	}
	if got.Schema != "2.0" || got.Version != "v2" {
		t.Fatalf("schema/version = %q/%q", got.Schema, got.Version)
	}
	if got.MessageID != "om_test" {
		t.Fatalf("message id = %q", got.MessageID)
	}
	if !strings.Contains(got.Summary, "查看详情") || !strings.Contains(got.Summary, "https://example.test/alert") {
		t.Fatalf("summary lost card text or action URL: %q", got.Summary)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("raw card is not valid JSON: %v", err)
	}
	if decoded["schema"] != "2.0" {
		t.Fatalf("raw schema = %v", decoded["schema"])
	}
}

func TestParseInboundCardBoundsRawPayload(t *testing.T) {
	card := `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"hello"}}]}}`
	got, ok := parseInboundCard(card, "om_test", 16)
	if !ok {
		t.Fatal("parseInboundCard returned false")
	}
	if !got.Truncated || len(got.Raw) != 0 {
		t.Fatalf("expected truncated raw payload, got truncated=%v raw=%d", got.Truncated, len(got.Raw))
	}
}

func TestUnwrapInboundCardJSONAcceptsPlainAndWrapper(t *testing.T) {
	plain := `{"schema":"2.0"}`
	if string(unwrapInboundCardJSON(plain)) != plain {
		t.Fatalf("plain card changed")
	}
	wrapper := `{"json_card":` + strconvQuote(plain) + `}`
	if string(unwrapInboundCardJSON(wrapper)) != plain {
		t.Fatalf("wrapped card was not unwrapped")
	}
	if got := extractInteractiveCardText(`{"json_card":{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"content":"object wrapper"}}]}}}`); !strings.Contains(got, "object wrapper") {
		t.Fatalf("object json_card wrapper was not parsed: %q", got)
	}
	if strings.TrimSpace(string(unwrapInboundCardJSON(""))) != "" {
		t.Fatalf("empty card should remain empty")
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
