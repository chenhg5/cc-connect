package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func init() {
	core.RegisterPlatform("telegram", New)
}

type replyContext struct {
	chatID    int64
	messageID int
}

type Platform struct {
	token   string
	bot     *tgbotapi.BotAPI
	handler core.MessageHandler
	cancel  context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	return &Platform{token: token}, nil
}

func (p *Platform) Name() string { return "telegram" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	bot, err := tgbotapi.NewBotAPI(p.token)
	if err != nil {
		return fmt.Errorf("telegram: auth failed: %w", err)
	}
	p.bot = bot

	slog.Info("telegram: connected", "bot", bot.Self.UserName)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update := <-updates:
				if update.Message == nil || update.Message.Text == "" {
					continue
				}

				msg := update.Message
				text := msg.Text

				// Strip bot mention in group chats: "/cmd@botname args" → "/cmd args"
				if p.bot.Self.UserName != "" {
					text = strings.Replace(text, "@"+p.bot.Self.UserName, "", 1)
				}

				userName := msg.From.UserName
				if userName == "" {
					userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
				}

				sessionKey := fmt.Sprintf("telegram:%d:%d", msg.Chat.ID, msg.From.ID)

				coreMsg := &core.Message{
					SessionKey: sessionKey,
					Platform:   "telegram",
					UserID:     strconv.FormatInt(msg.From.ID, 10),
					UserName:   userName,
					Content:    text,
					ReplyCtx:   replyContext{chatID: msg.Chat.ID, messageID: msg.MessageID},
				}

				slog.Debug("telegram: message received", "user", userName, "chat", msg.Chat.ID)
				p.handler(p, coreMsg)
			}
		}
	}()

	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	reply := tgbotapi.NewMessage(rc.chatID, content)
	reply.ReplyToMessageID = rc.messageID
	reply.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(reply); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
			reply.ParseMode = ""
			_, err = p.bot.Send(reply)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

// Send sends a new message (not a reply)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	msg := tgbotapi.NewMessage(rc.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(msg); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
			msg.ParseMode = ""
			_, err = p.bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.bot != nil {
		p.bot.StopReceivingUpdates()
	}
	return nil
}
