package config

import (
	"strings"
	"testing"
)

func TestDocumentParseErrorsOmitRawInput(t *testing.T) {
	const sentinel = "SENTINEL_CONFIG_CONTENT"
	_, _, headerErr := parseDocumentHeader("[" + sentinel + "!")
	_, nameErr := parseDocumentName("bad!" + sentinel)
	for _, err := range []error{headerErr, nameErr} {
		if err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("unsafe config parse error: %v", err)
		}
	}
}
