package acp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateForLogKeepsValidUTF8 verifies that truncating a string that
// contains multi-byte characters never leaves a half character behind
// (Issue #2103).
func TestTruncateForLogKeepsValidUTF8(t *testing.T) {
	const s = "acp stderr 中文 abc 🙂"

	for n := 0; n <= utf8.RuneCountInString(s)+2; n++ {
		got := truncateForLog(s, n)
		if !utf8.ValidString(got) {
			t.Errorf("n=%d: result is not valid UTF-8: %q", n, got)
		}
	}
}

// TestTruncateForLogCutsOnCharacterBoundary verifies the cut keeps whole
// characters and counts characters, not bytes.
func TestTruncateForLogCutsOnCharacterBoundary(t *testing.T) {
	got := truncateForLog("中文abc", 2)
	if want := "中文…"; got != want {
		t.Fatalf("truncateForLog = %q, want %q", got, want)
	}
}

// TestTruncateForLogKeepsShortString verifies a string that already fits is
// returned unchanged.
func TestTruncateForLogKeepsShortString(t *testing.T) {
	const s = "短"
	if got := truncateForLog(s, 8); got != s {
		t.Fatalf("truncateForLog = %q, want %q", got, s)
	}
}

// TestTruncateForLogNonPositiveLimit verifies a non-positive limit returns an
// empty string instead of an ellipsis.
func TestTruncateForLogNonPositiveLimit(t *testing.T) {
	for _, n := range []int{0, -1} {
		if got := truncateForLog("acp stderr", n); got != "" {
			t.Fatalf("truncateForLog(n=%d) = %q, want %q", n, got, "")
		}
	}
}

// TestTruncateForLogPassesThroughInvalidUTF8 documents that a string that
// already fits is returned byte for byte, so bytes that are not valid UTF-8
// are passed through as-is, while a truncated string is cut on a character
// boundary.
func TestTruncateForLogPassesThroughInvalidUTF8(t *testing.T) {
	const short = "ab\xffcd"
	if got := truncateForLog(short, 8); got != short {
		t.Fatalf("truncateForLog = %q, want %q", got, short)
	}

	long := "\xff" + strings.Repeat("中", 8)
	got := truncateForLog(long, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
}
