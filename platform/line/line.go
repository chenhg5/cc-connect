package line

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/chenhg5/cc-connect/core"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
)

func init() {
	core.RegisterPlatform("line", New)
}

// replyContext stores the user/group ID for push messages.
// We use PushMessage instead of ReplyMessage because reply tokens
// expire in ~1 minute, which is too short for AI agent processing.
type replyContext struct {
	targetID   string
	targetType string // "user" or "group" or "room"
}

type Platform struct {
	channelSecret string
	channelToken  string
	port          string
	callbackPath  string
	bot           *messaging_api.MessagingApiAPI
	server        *http.Server
	handler       core.MessageHandler
}

func New(opts map[string]any) (core.Platform, error) {
	secret, _ := opts["channel_secret"].(string)
	token, _ := opts["channel_token"].(string)
	if secret == "" || token == "" {
		return nil, fmt.Errorf("line: channel_secret and channel_token are required")
	}

	port, _ := opts["port"].(string)
	if port == "" {
		port = "8080"
	}
	path, _ := opts["callback_path"].(string)
	if path == "" {
		path = "/callback"
	}

	return &Platform{
		channelSecret: secret,
		channelToken:  token,
		port:          port,
		callbackPath:  path,
	}, nil
}

func (p *Platform) Name() string { return "line" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	bot, err := messaging_api.NewMessagingApiAPI(p.channelToken)
	if err != nil {
		return fmt.Errorf("line: create api client: %w", err)
	}
	p.bot = bot

	mux := http.NewServeMux()
	mux.HandleFunc(p.callbackPath, p.webhookHandler)

	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: mux,
	}

	go func() {
		slog.Info("line: webhook server listening", "port", p.port, "path", p.callbackPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("line: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) webhookHandler(w http.ResponseWriter, r *http.Request) {
	cb, err := webhook.ParseRequest(p.channelSecret, r)
	if err != nil {
		slog.Error("line: parse webhook failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	for _, event := range cb.Events {
		e, ok := event.(webhook.MessageEvent)
		if !ok {
			continue
		}
		textMsg, ok := e.Message.(webhook.TextMessageContent)
		if !ok {
			continue
		}

		targetID, targetType, userID := extractSource(e.Source)
		sessionKey := fmt.Sprintf("line:%s", targetID)

		slog.Debug("line: message received", "user", userID, "target", targetID, "text_len", len(textMsg.Text))

		msg := &core.Message{
			SessionKey: sessionKey,
			Platform:   "line",
			UserID:     userID,
			UserName:   userID,
			Content:    textMsg.Text,
			ReplyCtx:   replyContext{targetID: targetID, targetType: targetType},
		}

		p.handler(p, msg)
	}
}

func extractSource(src webhook.SourceInterface) (targetID, targetType, userID string) {
	switch s := src.(type) {
	case webhook.UserSource:
		return s.UserId, "user", s.UserId
	case webhook.GroupSource:
		return s.GroupId, "group", s.UserId
	case webhook.RoomSource:
		return s.RoomId, "room", s.UserId
	default:
		return "unknown", "unknown", "unknown"
	}
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("line: invalid reply context type %T", rctx)
	}

	if content == "" {
		return nil
	}

	// LINE text message limit is 5000 characters
	messages := splitMessage(content, 5000)
	for _, text := range messages {
		_, err := p.bot.PushMessage(
			&messaging_api.PushMessageRequest{
				To: rc.targetID,
				Messages: []messaging_api.MessageInterface{
					messaging_api.TextMessage{
						Text: text,
					},
				},
			}, "",
		)
		if err != nil {
			return fmt.Errorf("line: push message: %w", err)
		}
	}
	return nil
}

// Send sends a new message (same as Reply for LINE)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.Reply(ctx, rctx, content)
}

func splitMessage(s string, maxLen int) []string {
	if len(s) <= maxLen {
		return []string{s}
	}
	var parts []string
	runes := []rune(s)
	for len(runes) > 0 {
		end := maxLen
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

func (p *Platform) Stop() error {
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}
