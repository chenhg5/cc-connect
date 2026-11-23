package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
		sensitive := rand.Text()
		requestID := operationalLogRequestID(slog.StringValue("abcdefabcdefabcd"))
		logger.Debug("message received", "content", "BODY_MARKER", "token", sensitive, "code", 987654321, "user", "PERSON_MARKER", "path", "/home/PATH_MARKER", "error", errors.New("UPSTREAM_MARKER"), "unknown", "UNKNOWN_MARKER", "request_id", "abcdefabcdefabcd", "count", 3)
		logger.WithGroup("detail").Error("SDK_BODY_MARKER", "response", "RESPONSE_MARKER")
		got := out.String()
		if strings.Contains(got, "MARKER") || strings.Contains(got, "987654321") || strings.Contains(got, sensitive) || strings.Contains(got, "abcdefabcdefabcd") {
			t.Fatal("sensitive attributes reached log")
		}
		for _, want := range []string{requestID, "TestOperationalLogsExcludeSensitiveAttributesAtDebug", "count", "3", "[redacted]"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing operational field %q: %s", want, got)
			}
		}
	}
}

func TestOperationalLogRequestIDCorrelatesWithoutExposingInput(t *testing.T) {
	requestIDs := []string{
		"toolu_" + rand.Text(),
		"cc-connect-get-settings-7",
		"interrupt-1700000000000000000",
		"429",
		"vendor request with a private attribute",
		"abcdefabcdefabcd",
	}
	seen := make(map[string]bool)
	for _, id := range requestIDs {
		var out bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{ReplaceAttr: OperationalLogAttr}))
		logger.Info("request", "request_id", id)
		logger.With("request_id", id).Info("resolved")
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected two log lines, got %d", len(lines))
		}
		_, first, firstOK := strings.Cut(lines[0], "request_id=")
		_, second, secondOK := strings.Cut(lines[1], "request_id=")
		b, err := hex.DecodeString(first)
		if !firstOK || !secondOK || first != second || first == id || err != nil || len(b) != 16 || seen[first] {
			t.Fatal("request ID was not represented by a distinct, repeatable opaque token")
		}
		seen[first] = true
	}
	// Number and string producers can refer to the same JSON-RPC ID.
	if operationalLogRequestID(slog.Int64Value(429)) != operationalLogRequestID(slog.StringValue("429")) {
		t.Error("numeric and string representations do not correlate")
	}
	for _, v := range []slog.Value{slog.StringValue(""), slog.StringValue(strings.Repeat("a", 257)), slog.BoolValue(true)} {
		attr := OperationalLogAttr(nil, slog.Attr{Key: "request_id", Value: v})
		if attr.Value.String() != "[redacted]" {
			t.Error("out-of-policy request ID was retained")
		}
	}
}

func TestOperationalLogStartupCauseRejectsArbitraryText(t *testing.T) {
	private := rand.Text()
	for _, test := range []struct{ value, want string }{
		{"config_parse_error", "config_parse_error"},
		{"lock_held", "lock_held"},
		{private, "[redacted]"},
	} {
		got := OperationalLogAttr(nil, slog.String("startup_cause", test.value))
		if got.Value.String() != test.want {
			t.Fatal("startup cause was not retained or redacted as expected")
		}
	}
}
