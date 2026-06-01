package silk

import "testing"

func TestNormalizeWebSocketURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "wss://ai-silk.duckdns.org:13096/ccconnect-bridge",
			want: "wss://ai-silk.duckdns.org:13096/ccconnect-bridge",
		},
		{
			in:   "https://ai-silk.duckdns.org:13096/ccconnect-bridge",
			want: "wss://ai-silk.duckdns.org:13096/ccconnect-bridge",
		},
		{
			in:   "ws://localhost:8006/ccconnect-bridge",
			want: "ws://localhost:8006/ccconnect-bridge",
		},
		{
			in:   "http://localhost:8006/ccconnect-bridge",
			want: "ws://localhost:8006/ccconnect-bridge",
		},
		{
			in:   "localhost:8006/ccconnect-bridge",
			want: "ws://localhost:8006/ccconnect-bridge",
		},
		{
			in:   "https://ai-silk.duckdns.org:13096/ccconnect-bridge/",
			want: "wss://ai-silk.duckdns.org:13096/ccconnect-bridge",
		},
	}
	for _, tc := range tests {
		got, err := normalizeWebSocketURL(tc.in)
		if err != nil {
			t.Fatalf("normalizeWebSocketURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeWebSocketURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseBoolOpt(t *testing.T) {
	if !parseBoolOpt(true) {
		t.Fatal("expected true for bool true")
	}
	if parseBoolOpt(false) {
		t.Fatal("expected false for bool false")
	}
	if !parseBoolOpt("true") || !parseBoolOpt("1") || !parseBoolOpt("yes") {
		t.Fatal("expected true for string truthy values")
	}
	if parseBoolOpt("no") {
		t.Fatal("expected false for no")
	}
}
