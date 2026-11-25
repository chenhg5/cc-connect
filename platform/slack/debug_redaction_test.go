package slack

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

func TestSlashCommandDebugLogOmitsUserText(t *testing.T) {
	const secret = "SLACK-COMMAND-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var got *core.Message
	p := &Platform{handler: func(_ core.Platform, msg *core.Message) { got = msg }}
	p.handleEvent(socketmode.Event{Type: socketmode.EventTypeSlashCommand, Data: slack.SlashCommand{Command: "/ask", Text: secret, UserID: "user-1", ChannelID: "channel-1"}})
	if got == nil || !strings.Contains(got.Content, secret) {
		t.Fatal("slash command text was not delivered")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("Slack Debug log exposed slash command text")
	}
}
