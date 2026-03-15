package slack

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

func TestStripAppMentionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips bot mention prefix",
			in:   "<@U0BOT123> run tests",
			want: "run tests",
		},
		{
			name: "empty mention becomes empty text",
			in:   "<@U0BOT123> ",
			want: "",
		},
		{
			name: "plain text remains unchanged",
			in:   "run tests",
			want: "run tests",
		},
		{
			name: "strips bot mention prefix followed by non-breaking space",
			in:   "<@U0BOT123>\u00a0/model",
			want: "/model",
		},
		{
			name: "strips bot mention prefix followed by newline",
			in:   "<@U0BOT123>\n/model",
			want: "/model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAppMentionText(tt.in); got != tt.want {
				t.Fatalf("stripAppMentionText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeSlashCommandText(t *testing.T) {
	tests := []struct {
		name string
		cmd  slack.SlashCommand
		want string
		ok   bool
	}{
		{
			name: "empty cc command falls back to help",
			cmd:  slack.SlashCommand{Command: "/cc", Text: ""},
			want: "/help",
			ok:   true,
		},
		{
			name: "plain text command gets slash prefix",
			cmd:  slack.SlashCommand{Command: "/cc", Text: "model"},
			want: "/model",
			ok:   true,
		},
		{
			name: "existing slash command is preserved",
			cmd:  slack.SlashCommand{Command: "/cc", Text: "/model"},
			want: "/model",
			ok:   true,
		},
		{
			name: "unsupported slash command is ignored",
			cmd:  slack.SlashCommand{Command: "/model", Text: ""},
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeSlashCommandText(tt.cmd)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("normalizeSlashCommandText(%+v) = (%q, %v), want (%q, %v)", tt.cmd, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestHandleEventSlashCommand(t *testing.T) {
	var got *core.Message
	p := &Platform{
		allowFrom: "*",
		handler: func(_ core.Platform, msg *core.Message) {
			got = msg
		},
	}

	p.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeSlashCommand,
		Data: slack.SlashCommand{
			Command:   "/cc",
			Text:      "model",
			ChannelID: "C1",
			UserID:    "U1",
			UserName:  "son",
		},
	})

	if got == nil {
		t.Fatal("expected slash command to dispatch a message")
	}
	if got.SessionKey != "slack:C1:U1" {
		t.Fatalf("session key = %q, want %q", got.SessionKey, "slack:C1:U1")
	}
	if got.Content != "/model" {
		t.Fatalf("content = %q, want %q", got.Content, "/model")
	}
	if got.UserID != "U1" || got.UserName != "son" {
		t.Fatalf("user = (%q, %q), want (%q, %q)", got.UserID, got.UserName, "U1", "son")
	}
	rctx, ok := got.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("reply ctx type = %T, want replyContext", got.ReplyCtx)
	}
	if rctx.channel != "C1" {
		t.Fatalf("reply channel = %q, want %q", rctx.channel, "C1")
	}
}
