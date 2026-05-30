package daxiang

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("daxiang", New)
}

type Platform struct {
	appID          string
	appSecret      string
	botID          int64
	audience       string
	callbackAddr       string
	callbackListenAddr string
	cardTemplateID     int64
	allowFrom      string
	progressStyle  string
	handler        core.MessageHandler
	thriftServer   interface{ Stop() error }
	dedup          core.MessageDedup
}

func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	audience, _ := opts["audience"].(string)
	callbackAddr, _ := opts["callback_addr"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("daxiang", allowFrom)
	progressStyle, _ := opts["progress_style"].(string)

	botID := int64FromOption(opts["bot_id"])
	cardTemplateID := int64FromOption(opts["card_template_id"])

	if appID == "" || appSecret == "" || botID == 0 || callbackAddr == "" || cardTemplateID == 0 {
		return nil, fmt.Errorf("daxiang: app_id, app_secret, bot_id, callback_addr and card_template_id are required")
	}
	if strings.TrimSpace(audience) == "" {
		return nil, fmt.Errorf("daxiang: audience is required")
	}
	if audience != "xm-xai" {
		return nil, fmt.Errorf("daxiang: audience must be xm-xai")
	}
	if progressStyle == "" {
		progressStyle = "legacy"
	}
	progressStyle = strings.ToLower(strings.TrimSpace(progressStyle))
	if progressStyle != "legacy" && progressStyle != "compact" && progressStyle != "card" {
		return nil, fmt.Errorf("daxiang: invalid progress_style %q (want legacy, compact, or card)", progressStyle)
	}

	return &Platform{
		appID:          appID,
		appSecret:      appSecret,
		botID:          botID,
		audience:       audience,
		callbackAddr:   callbackAddr,
		cardTemplateID: cardTemplateID,
		allowFrom:      allowFrom,
		progressStyle:  progressStyle,
	}, nil
}

func int64FromOption(v any) int64 {
	switch value := v.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func (p *Platform) Name() string { return "daxiang" }

func (p *Platform) ProgressStyle() string { return p.progressStyle }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	return p.startCallbackServer()
}

func (p *Platform) Stop() error {
	if p.thriftServer == nil {
		return nil
	}
	server := p.thriftServer
	p.thriftServer = nil
	p.callbackListenAddr = ""
	return server.Stop()
}

func (p *Platform) handleCallbackEvent(evt callbackEvent) error {
	if evt.AppID != p.appID || evt.BotID != p.botID {
		return fmt.Errorf("daxiang: unexpected callback target app_id=%q bot_id=%d", evt.AppID, evt.BotID)
	}
	if core.IsOldMessage(callbackMessageTime(evt)) {
		return nil
	}
	if !core.AllowList(p.allowFrom, strconv.FormatInt(evt.Data.FromUID, 10)) {
		return nil
	}
	if p.dedup.IsDuplicate(strconv.FormatInt(evt.Data.MsgID, 10)) {
		return nil
	}
	msg, err := normalizeInboundMessage(evt)
	if err != nil {
		if errors.Is(err, errUnsupportedEventType) || errors.Is(err, errUnsupportedMessageType) {
			return nil
		}
		return err
	}
	if p.handler != nil {
		p.handler(p, msg)
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return core.ErrNotSupported
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return core.ErrNotSupported
}

func (p *Platform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	return nil, core.ErrNotSupported
}

func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	return core.ErrNotSupported
}
