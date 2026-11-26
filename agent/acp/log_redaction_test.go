package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPToolInputAndRawUpdatesStayOutOfLogs(t *testing.T) {
	const secret = "ACP-TOOL-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s := &acpSession{ctx: context.Background(), events: make(chan core.Event, 8), toolInputByID: make(map[string]string)}
	s.onNotification("session/update", json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","kind":"execute","rawInput":{"command":"`+secret+`"}}}`))
	mapSessionUpdate("s1", json.RawMessage(`{"invalid":"`+secret+`"`))
	if strings.Contains(logs.String(), secret) {
		t.Fatal("ACP diagnostic log exposed tool input or raw update")
	}
	if !strings.Contains(logs.String(), "input_bytes=") {
		t.Fatal("ACP diagnostic log lost tool input size")
	}
}
