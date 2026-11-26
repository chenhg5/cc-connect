package qqbot

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestInvalidInteractionLogOmitsButtonData(t *testing.T) {
	const secret = "QQBOT-BUTTON-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{allowFrom: "*"}
	payload, err := json.Marshal(map[string]any{"id": "interaction-1", "data": map[string]any{"resolved": map[string]any{"button_data": "perm:" + secret}}})
	if err != nil {
		t.Fatal(err)
	}
	p.handleInteractionCreate(payload)
	if strings.Contains(logs.String(), secret) {
		t.Fatal("QQBot log exposed raw button data")
	}
}
