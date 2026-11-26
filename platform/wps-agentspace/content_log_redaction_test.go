package wpsagentspace

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMessageLogsOmitUserContent(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{}
	raw, err := json.Marshal(messageData{Role: "user", Type: "text", Content: marker, ChatID: "chat", MessageID: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.handleFrame(wsFrame{Event: "message", Data: raw}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatal("WPS Agentspace log exposed user content")
	}
}
