package max

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestCallbackDebugLogOmitsPayload(t *testing.T) {
	const secret = "MAX-CALLBACK-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{}
	p.handleCallback(context.Background(), &maxCallback{CallbackID: "cb-1", Payload: secret, User: maxUser{UserID: 123, Name: "Tester"}})
	if strings.Contains(logs.String(), secret) {
		t.Fatal("MAX Debug log exposed callback payload")
	}
}
