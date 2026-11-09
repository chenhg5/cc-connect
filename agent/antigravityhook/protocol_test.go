package antigravityhook

import (
	"bytes"
	"strings"
	"testing"
)

func TestRelayRejectsInvalidInputBeforeConnecting(t *testing.T) {
	var output bytes.Buffer
	err := Relay(strings.NewReader("not-json"), &output, "127.0.0.1:1", "token")
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("Relay() error = %v, want invalid JSON error", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

func TestRelayRequiresBridgeEnvironment(t *testing.T) {
	err := Relay(strings.NewReader("{}"), &bytes.Buffer{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "environment is missing") {
		t.Fatalf("Relay() error = %v, want missing environment error", err)
	}
}
