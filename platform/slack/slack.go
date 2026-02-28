package slack

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func init() {
	core.RegisterPlatform("slack", New)
}

type replyContext struct {
	channel   string
	timestamp string // thread_ts for threading replies
}

type Platform struct {
	botToken string
	appToken string
	client   *slack.Client
	socket   *socketmode.Client
	handler  core.MessageHandler
	cancel   context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	botToken, _ := opts["bot_token"].(string)
	appToken, _ := opts["app_token"].(string)
	if botToken == "" || appToken == "" {
		return nil, fmt.Errorf("slack: bot_token and app_token are required")
	}
	return &Platform{
		botToken: botToken,
		appToken: appToken,
	}, nil
}

func (p *Platform) Name() string { return "slack" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	p.client = slack.New(p.botToken,
		slack.OptionAppLevelToken(p.appToken),
	)
	p.socket = socketmode.New(p.client)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-p.socket.Events:
				p.handleEvent(evt)
			}
		}
	}()

	go func() {
		if err := p.socket.RunContext(ctx); err != nil {
			slog.Error("slack: socket mode error", "error", err)
		}
	}()

	slog.Info("slack: socket mode connected")
	return nil
}

func (p *Platform) handleEvent(evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		data, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		p.socket.Ack(*evt.Request)

		if data.Type == slackevents.CallbackEvent {
			switch ev := data.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				if ev.BotID != "" || ev.User == "" {
					return
				}

				slog.Debug("slack: message received", "user", ev.User, "channel", ev.Channel)

				sessionKey := fmt.Sprintf("slack:%s:%s", ev.Channel, ev.User)

				// Use the message timestamp as thread_ts so replies are threaded
				ts := ev.TimeStamp

				msg := &core.Message{
					SessionKey: sessionKey,
					Platform:   "slack",
					UserID:     ev.User,
					UserName:   ev.User,
					Content:    ev.Text,
					ReplyCtx:   replyContext{channel: ev.Channel, timestamp: ts},
				}

				p.handler(p, msg)
			}
		}

	case socketmode.EventTypeConnecting:
		slog.Debug("slack: connecting...")
	case socketmode.EventTypeConnected:
		slog.Info("slack: connected")
	case socketmode.EventTypeConnectionError:
		slog.Error("slack: connection error")
	}
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}

	opts := []slack.MsgOption{
		slack.MsgOptionText(content, false),
	}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionTS(rc.timestamp))
	}

	_, _, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
}

// Send sends a new message (not a reply)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}

	_, _, err := p.client.PostMessageContext(ctx, rc.channel, slack.MsgOptionText(content, false))
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
