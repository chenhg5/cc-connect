package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func init() {
	core.RegisterPlatform("feishu", New)
}

type replyContext struct {
	messageID string
}

type Platform struct {
	appID     string
	appSecret string
	client    *lark.Client
	wsClient  *larkws.Client
	handler   core.MessageHandler
	cancel    context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("feishu: app_id and app_secret are required")
	}
	return &Platform{
		appID:     appID,
		appSecret: appSecret,
		client:    lark.NewClient(appID, appSecret),
	}, nil
}

func (p *Platform) Name() string { return "feishu" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return p.onMessage(event)
		})

	p.wsClient = larkws.NewClient(p.appID, p.appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		if err := p.wsClient.Start(ctx); err != nil {
			slog.Error("feishu: websocket error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) onMessage(event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	sender := event.Event.Sender

	if msg.MessageType == nil || *msg.MessageType != "text" {
		slog.Debug("feishu: ignoring non-text message", "type", msg.MessageType)
		return nil
	}

	var textBody struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &textBody); err != nil {
		slog.Error("feishu: failed to parse message content", "error", err)
		return nil
	}

	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}
	userID := ""
	userName := ""
	if sender.SenderId != nil {
		userID = *sender.SenderId.OpenId
	}
	if sender.SenderType != nil {
		userName = *sender.SenderType
	}

	sessionKey := fmt.Sprintf("feishu:%s:%s", chatID, userID)

	coreMsg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "feishu",
		UserID:     userID,
		UserName:   userName,
		Content:    textBody.Text,
		ReplyCtx:   replyContext{messageID: *msg.MessageId},
	}

	p.handler(p, coreMsg)
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("feishu: invalid reply context type %T", rctx)
	}

	body, _ := json.Marshal(map[string]string{"text": content})

	resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(rc.messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeText).
			Content(string(body)).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu: reply api call: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu: reply failed code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
