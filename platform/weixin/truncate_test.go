package weixin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateForLogKeepsValidUTF8 verifies that truncating a body that
// contains multi-byte characters never leaves a half character behind
// (Issue #2103).
func TestTruncateForLogKeepsValidUTF8(t *testing.T) {
	const body = "微信 API 中文 abc 🙂"

	for max := 0; max <= utf8.RuneCountInString(body)+2; max++ {
		got := truncateForLog([]byte(body), max)
		if !utf8.ValidString(got) {
			t.Errorf("max=%d: result is not valid UTF-8: %q", max, got)
		}
	}
}

// TestTruncateForLogCutsOnCharacterBoundary verifies the cut keeps whole
// characters and counts characters, not bytes.
func TestTruncateForLogCutsOnCharacterBoundary(t *testing.T) {
	got := truncateForLog([]byte("中文abc"), 2)
	if want := "中文…"; got != want {
		t.Fatalf("truncateForLog = %q, want %q", got, want)
	}
}

// TestTruncateForLogKeepsShortBody verifies a body that already fits is
// returned unchanged.
func TestTruncateForLogKeepsShortBody(t *testing.T) {
	const body = "短"
	if got := truncateForLog([]byte(body), 8); got != body {
		t.Fatalf("truncateForLog = %q, want %q", got, body)
	}
}

// TestTruncateForLogNonPositiveLimit verifies a non-positive limit returns an
// empty string instead of an ellipsis.
func TestTruncateForLogNonPositiveLimit(t *testing.T) {
	for _, max := range []int{0, -1} {
		if got := truncateForLog([]byte("微信 API"), max); got != "" {
			t.Fatalf("truncateForLog(max=%d) = %q, want %q", max, got, "")
		}
	}
}

// TestTruncateForLogPassesThroughInvalidUTF8 documents that a body that
// already fits is returned byte for byte, so a body that is not valid UTF-8
// stays as it is, while a truncated body is cut on a character boundary.
func TestTruncateForLogPassesThroughInvalidUTF8(t *testing.T) {
	short := []byte("ab\xffcd")
	if got := truncateForLog(short, 8); got != string(short) {
		t.Fatalf("truncateForLog = %q, want %q", got, short)
	}

	long := []byte("\xff" + strings.Repeat("中", 8))
	got := truncateForLog(long, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated body is not valid UTF-8: %q", got)
	}
}
