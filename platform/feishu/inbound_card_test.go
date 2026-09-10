package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseInboundCardPreservesRawCard20(t *testing.T) {
	card := `{"schema":"2.0","body":{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"渠道不足"}},{"tag":"button","text":{"tag":"plain_text","content":"查看详情"},"behaviors":[{"type":"open_url","default_url":"https://example.test/alert"}],"value":{"alert_id":"alert-1"}}]}}`
	content := `{"json_card":` + quoteInboundJSON(card) + `}`

	got, ok := parseInboundCard(content, "om_test", 64*1024)
	if !ok {
		t.Fatal("parseInboundCard returned false")
	}
	if got.Schema != "2.0" || got.Version != "v2" {
		t.Fatalf("schema/version = %q/%q", got.Schema, got.Version)
	}
	if got.MessageID != "om_test" || !strings.Contains(got.Summary, "渠道不足") {
		t.Fatalf("parsed card metadata/summary = %#v", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("raw card is not valid JSON: %v", err)
	}
	if decoded["schema"] != "2.0" || !strings.Contains(string(got.Raw), `"alert_id":"alert-1"`) {
		t.Fatalf("raw card lost alert payload: %s", got.Raw)
	}
}

func TestUnwrapInboundCardJSONAcceptsPlainAndWrapper(t *testing.T) {
	plain := `{"schema":"2.0"}`
	if string(unwrapInboundCardJSON(plain)) != plain {
		t.Fatalf("plain card changed")
	}
	wrapper := `{"json_card":` + quoteInboundJSON(plain) + `}`
	if string(unwrapInboundCardJSON(wrapper)) != plain {
		t.Fatalf("wrapped card was not unwrapped")
	}
}

func quoteInboundJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
