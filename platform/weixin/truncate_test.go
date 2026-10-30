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
