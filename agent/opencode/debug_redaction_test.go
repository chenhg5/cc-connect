package opencode

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestUnknownEventDebugLogOmitsRawPayload(t *testing.T) {
	const secret = "OPENCODE-EVENT-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &opencodeSession{}
	s.handleEvent(map[string]any{"type": "unknown", "content": secret})
	if strings.Contains(logs.String(), secret) {
		t.Fatal("OpenCode Debug log exposed unknown event body")
	}
}
