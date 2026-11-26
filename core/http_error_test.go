package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHTTPErrorRedactsAndBoundsMessage(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	body := []byte(`{"errmsg":"denied ` + marker + ` ` + strings.Repeat("好", 200) + `","secret":"` + marker + `"}`)
	err := HTTPError("send", 401, body, marker)
	got := err.Error()
	if strings.Contains(got, marker) || !strings.Contains(got, "HTTP 401") || !strings.Contains(got, "body_bytes=") {
		t.Fatalf("unsafe or incomplete HTTP error: %q", got)
	}
	if !utf8.ValidString(got) || len([]rune(got)) > 200 {
		t.Fatalf("HTTP error is not bounded valid UTF-8: %q", got)
	}
}
