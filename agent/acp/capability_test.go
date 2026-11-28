package acp

import (
	"encoding/json"
	"testing"
)

func TestCapabilityAdvertised(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "", false},
		{"null", "null", false},
		{"null with whitespace", "  null\n", false},
		{"empty object", "{}", true},
		{"object with fields", `{"list":true}`, true},
		{"boolean false", "false", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityAdvertised(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Fatalf("capabilityAdvertised(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
