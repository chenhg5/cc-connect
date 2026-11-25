package telegram

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestGroupRoutingDebugLogOmitsMessageText(t *testing.T) {
	const secret = "TELEGRAM-MESSAGE-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{selfUser: &models.User{ID: 42, Username: "bot"}}
	msg := &models.Message{Text: secret, Chat: models.Chat{ID: 7, Type: models.ChatTypeGroup}}
	if p.isDirectedAtBot(msg) {
		t.Fatal("unaddressed group message was accepted")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("Telegram Debug log exposed message text")
	}
}
