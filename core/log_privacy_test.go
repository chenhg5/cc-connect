package core

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestOperationalLogsExcludeSensitiveAttributesAtDebug(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		var out bytes.Buffer
		options := &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: OperationalLogAttr, AddSource: true}
		var handler slog.Handler = slog.NewTextHandler(&out, options)
		if jsonOutput {
			handler = slog.NewJSONHandler(&out, options)
		}
		logger := slog.New(handler).With("session", "SESSION_MARKER")
		logger.Debug("message received", "content", "BODY_MARKER", "token", "TOKEN_MARKER", "code", 987654321, "user", "PERSON_MARKER", "path", "/home/PATH_MARKER", "error", errors.New("UPSTREAM_MARKER"), "unknown", "UNKNOWN_MARKER", "request_id", "abcdefabcdefabcd", "count", 3)
		logger.WithGroup("detail").Error("SDK_BODY_MARKER", "response", "RESPONSE_MARKER")
		got := out.String()
		if strings.Contains(got, "MARKER") || strings.Contains(got, "987654321") {
			t.Fatalf("sensitive attributes reached log: %s", got)
		}
		for _, want := range []string{"abcdefabcdefabcd", "TestOperationalLogsExcludeSensitiveAttributesAtDebug", "count", "3", "[redacted]"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing operational field %q: %s", want, got)
			}
		}
	}
}
