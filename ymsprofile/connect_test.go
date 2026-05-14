package ymsprofile

import "testing"

func TestParseConnectTarget(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"plain", "/connect yms-dev", "yms-dev", true},
		{"leading spaces", "  /connect yms-dev", "yms-dev", true},
		{"trailing spaces", "/connect yms-dev   ", "yms-dev", true},
		{"tab separator", "/connect\tyms-dev", "yms-dev", true},
		{"quoted name", "/connect \"yms-dev\"", "yms-dev", true},
		{"with inject header", "[cc-connect sender_id=ou_abc platform=feishu chat_id=oc_xyz]\n/connect yms-stage", "yms-stage", true},
		{"with inject header and sender_name", "[cc-connect sender_id=ou_abc sender_name=\"John Doe\" platform=feishu]\n/connect yms-dev", "yms-dev", true},
		{"header followed by leading spaces", "[cc-connect platform=youzone]\n   /connect yms-dev   ", "yms-dev", true},
		{"empty", "", "", false},
		{"non-command", "hello", "", false},
		{"slash but not connect", "/status", "", false},
		{"connect prefix only", "/connectfoo bar", "", false},
		{"connect with no arg", "/connect", "", false},
		{"connect with only spaces", "/connect    ", "", false},
		{"only header", "[cc-connect platform=youzone]\n", "", false},
		{"header then non-connect", "[cc-connect platform=youzone]\nshow me logs", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseConnectTarget(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ParseConnectTarget(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseConnectTarget_HeaderWithoutClosingBracket(t *testing.T) {
	// If the first line starts with `[cc-connect` but never closes the bracket,
	// we should not consume it as a header — fall back to evaluating the whole
	// prompt verbatim (which won't match /connect).
	got, ok := ParseConnectTarget("[cc-connect platform=youzone\n/connect yms-dev")
	if ok || got != "" {
		t.Fatalf("expected no match for malformed header, got (%q, %v)", got, ok)
	}
}
