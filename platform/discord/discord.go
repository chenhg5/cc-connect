package discord

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"

	"github.com/bwmarrin/discordgo"
)

func init() {
	core.RegisterPlatform("discord", New)
}

const maxDiscordLen = 2000

type replyContext struct {
	channelID string
	messageID string
}

type Platform struct {
	token   string
	session *discordgo.Session
	handler core.MessageHandler
	botID   string
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("discord: token is required")
	}
	return &Platform{token: token}, nil
}

func (p *Platform) Name() string { return "discord" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	p.session = session

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		p.botID = r.User.ID
		slog.Info("discord: connected", "bot", r.User.Username+"#"+r.User.Discriminator)
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || m.Author.ID == p.botID {
			return
		}

		slog.Debug("discord: message received", "user", m.Author.Username, "channel", m.ChannelID)

		sessionKey := fmt.Sprintf("discord:%s:%s", m.ChannelID, m.Author.ID)

		msg := &core.Message{
			SessionKey: sessionKey,
			Platform:   "discord",
			UserID:     m.Author.ID,
			UserName:   m.Author.Username,
			Content:    m.Content,
			ReplyCtx:   replyContext{channelID: m.ChannelID, messageID: m.ID},
		}

		p.handler(p, msg)
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord: open gateway: %w", err)
	}

	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}

	// Discord has a 2000 char limit per message
	for len(content) > 0 {
		chunk := content
		if len(chunk) > maxDiscordLen {
			// Try to split at a newline
			cut := maxDiscordLen
			if idx := lastIndexBefore(content, '\n', cut); idx > 0 {
				cut = idx + 1
			}
			chunk = content[:cut]
			content = content[cut:]
		} else {
			content = ""
		}

		ref := &discordgo.MessageReference{MessageID: rc.messageID}
		_, err := p.session.ChannelMessageSendReply(rc.channelID, chunk, ref)
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.session != nil {
		return p.session.Close()
	}
	return nil
}

func lastIndexBefore(s string, b byte, before int) int {
	for i := before - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
