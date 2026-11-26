package weixin

import (
	"testing"
	"unicode/utf8"
)

func TestTruncatePreviewMultibyte(t *testing.T) {
	got := truncatePreview("a界b", 2)
	if want := "a界..."; got != want {
		t.Errorf("truncatePreview() = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncatePreview() produced invalid UTF-8: %q", got)
	}
}

func TestTruncateForLogPreservesUTF8Boundary(t *testing.T) {
	for _, tc := range []struct {
		max  int
		want string
	}{{2, "a…"}, {4, "a界…"}} {
		got := truncateForLog([]byte("a界b"), tc.max)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("truncateForLog(max=%d) = %q, want %q valid UTF-8", tc.max, got, tc.want)
		}
	}
}
