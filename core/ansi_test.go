package core

import "testing"

func TestNormalizeOutgoingContent_StripsANSISequences(t *testing.T) {
	input := "warn: \x1b[31;1mfailed\x1b[0m\nlink \x1b]8;;https://example.com\x07docs\x1b]8;;\x07"
	got := NormalizeOutgoingContent(input)
	want := "warn: failed\nlink docs"
	if got != want {
		t.Fatalf("NormalizeOutgoingContent() = %q, want %q", got, want)
	}
}
