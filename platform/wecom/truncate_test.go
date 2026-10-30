package wecom

import (
	"testing"
	"unicode/utf8"
)

func TestWecomLogXMLPreviewMultibyte(t *testing.T) {
	got := wecomLogXMLPreview("a界b", 2)
	if want := "a界…"; got != want {
		t.Errorf("wecomLogXMLPreview() = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("wecomLogXMLPreview() produced invalid UTF-8: %q", got)
	}
}
