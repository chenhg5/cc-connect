package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
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

func TestDocumentScanErrorsIncludeLineWithoutContent(t *testing.T) {
	const sentinel = "SENTINEL_CONFIG_CONTENT"
	for _, tc := range []struct {
		name, source, line string
	}{
		{"header", "# comment\n\n[" + sentinel + "!\n", "line 3"},
		{"assignment", "# comment\nbad!" + sentinel + " = 1\n", "line 2"},
		{"value", "# comment\nvalue = [1, " + sentinel + "\n", "line 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := scanConfigDocument(tc.source)
			if err == nil || !strings.Contains(err.Error(), tc.line) || strings.Contains(err.Error(), sentinel) {
				t.Fatalf("scan error = %v, want %s without source content", err, tc.line)
			}
		})
	}
}

func TestPatchedDocumentTOMLErrorIncludesLineWithoutContent(t *testing.T) {
	const sentinel = "SENTINEL_CONFIG_CONTENT"
	var decoded Config
	err := toml.Unmarshal([]byte("# comment\nvalue = ["+sentinel+"\n"), &decoded)
	if err == nil {
		t.Fatal("expected invalid TOML")
	}
	safe := safePatchedDocumentError(err).Error()
	if !strings.Contains(safe, "line 2") || strings.Contains(safe, sentinel) {
		t.Fatalf("unsafe or unlocated parse error: %s", safe)
	}
}
