package main

import (
	"testing"
	"unicode/utf8"
)

func TestWeixinTruncateBodyMultibyte(t *testing.T) {
	got := weixinTruncateBody([]byte("a界b"), 2)
	if want := "a界…"; got != want {
		t.Errorf("weixinTruncateBody() = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("weixinTruncateBody() produced invalid UTF-8: %q", got)
	}
}
