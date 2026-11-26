package acp

import (
	"encoding/json"
	"testing"
)

func TestConfigOptionNonStringCurrentValue(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"bool_false", `{"id":"multitask","type":"boolean","currentValue":false}`, "false"},
		{"bool_true", `{"id":"multitask","type":"boolean","currentValue":true}`, "true"},
		{"string", `{"id":"model","type":"select","currentValue":"hy4flash-free"}`, "hy4flash-free"},
		{"number", `{"id":"x","type":"select","currentValue":3}`, "3"},
		{"null", `{"id":"x","type":"select","currentValue":null}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o acpConfigOption
			if err := json.Unmarshal([]byte(tc.doc), &o); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if o.CurrentValue != tc.want {
				t.Fatalf("currentValue = %q, want %q", o.CurrentValue, tc.want)
			}
		})
	}
}
