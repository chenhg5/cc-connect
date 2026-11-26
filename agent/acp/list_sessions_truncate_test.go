package acp

import (
	"testing"
	"unicode/utf8"
)

func TestSessionListTruncateForLogPreservesUTF8Boundary(t *testing.T) {
	got := truncateForLog("a界b", 2)
	if got != "a…" || !utf8.ValidString(got) {
		t.Fatalf("truncateForLog = %q, want valid UTF-8 a…", got)
	}
}
