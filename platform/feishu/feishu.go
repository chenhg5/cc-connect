package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

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
	chatID    string
}

type Platform struct {
	appID         string
	appSecret     string
	reactionEmoji string
	client        *lark.Client
	wsClient      *larkws.Client
	handler       core.MessageHandler
	cancel        context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("feishu: app_id and app_secret are required")
	}
	reactionEmoji, _ := opts["reaction_emoji"].(string)
	if reactionEmoji == "" {
		reactionEmoji = "OnIt"
	}
	if v, ok := opts["reaction_emoji"].(string); ok && v == "none" {
		reactionEmoji = ""
	}

	return &Platform{
		appID:         appID,
		appSecret:     appSecret,
		reactionEmoji: reactionEmoji,
		client:        lark.NewClient(appID, appSecret),
	}, nil
}

func (p *Platform) Name() string { return "feishu" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			slog.Debug("feishu: message received", "app_id", p.appID)
			return p.onMessage(event)
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return nil // ignore read receipts
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			slog.Debug("feishu: user opened bot chat", "app_id", p.appID)
			return nil
		}).
		OnP1P2PChatCreatedV1(func(ctx context.Context, event *larkim.P1P2PChatCreatedV1) error {
			slog.Debug("feishu: p2p chat created", "app_id", p.appID)
			return nil
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

func (p *Platform) addReaction(messageID string) {
	if p.reactionEmoji == "" {
		return
	}
	emojiType := p.reactionEmoji
	go func() {
		resp, err := p.client.Im.MessageReaction.Create(context.Background(),
			larkim.NewCreateMessageReactionReqBuilder().
				MessageId(messageID).
				Body(larkim.NewCreateMessageReactionReqBodyBuilder().
					ReactionType(&larkim.Emoji{EmojiType: &emojiType}).
					Build()).
				Build())
		if err != nil {
			slog.Debug("feishu: add reaction failed", "error", err)
			return
		}
		if !resp.Success() {
			slog.Debug("feishu: add reaction failed", "code", resp.Code, "msg", resp.Msg)
		}
	}()
}

func (p *Platform) onMessage(event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	sender := event.Event.Sender

	msgType := ""
	if msg.MessageType != nil {
		msgType = *msg.MessageType
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

	messageID := ""
	if msg.MessageId != nil {
		messageID = *msg.MessageId
	}

	if msgType != "" && messageID != "" {
		p.addReaction(messageID)
	}

	sessionKey := fmt.Sprintf("feishu:%s:%s", chatID, userID)
	rctx := replyContext{messageID: messageID, chatID: chatID}

	switch msgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(*msg.Content), &textBody); err != nil {
			slog.Error("feishu: failed to parse text content", "error", err)
			return nil
		}
		p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "feishu",
			UserID: userID, UserName: userName,
			Content: textBody.Text, ReplyCtx: rctx,
		})

	case "image":
		var imgBody struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(*msg.Content), &imgBody); err != nil {
			slog.Error("feishu: failed to parse image content", "error", err)
			return nil
		}
		imgData, mimeType, err := p.downloadImage(messageID, imgBody.ImageKey)
		if err != nil {
			slog.Error("feishu: download image failed", "error", err)
			return nil
		}
		p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "feishu",
			UserID: userID, UserName: userName,
			Images:  []core.ImageAttachment{{MimeType: mimeType, Data: imgData}},
			ReplyCtx: rctx,
		})

	case "audio":
		var audioBody struct {
			FileKey  string `json:"file_key"`
			Duration int    `json:"duration"` // milliseconds
		}
		if err := json.Unmarshal([]byte(*msg.Content), &audioBody); err != nil {
			slog.Error("feishu: failed to parse audio content", "error", err)
			return nil
		}
		slog.Debug("feishu: audio received", "user", userID, "file_key", audioBody.FileKey)
		audioData, err := p.downloadResource(messageID, audioBody.FileKey, "file")
		if err != nil {
			slog.Error("feishu: download audio failed", "error", err)
			return nil
		}
		p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "feishu",
			UserID: userID, UserName: userName,
			Audio: &core.AudioAttachment{
				MimeType: "audio/opus",
				Data:     audioData,
				Format:   "ogg",
				Duration: audioBody.Duration / 1000,
			},
			ReplyCtx: rctx,
		})

	default:
		slog.Debug("feishu: ignoring unsupported message type", "type", msgType)
	}

	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("feishu: invalid reply context type %T", rctx)
	}

	msgType, msgBody := buildReplyContent(content)

	resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(rc.messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(msgBody).
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

// Send sends a new message to the same chat (not a reply to original message)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("feishu: invalid reply context type %T", rctx)
	}

	if rc.chatID == "" {
		return fmt.Errorf("feishu: chatID is empty, cannot send new message")
	}

	msgType, msgBody := buildReplyContent(content)

	// Send a new message to the chat (not a reply)
	resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(rc.chatID).
			MsgType(msgType).
			Content(msgBody).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu: send api call: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu: send failed code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// downloadImage fetches an image from Feishu by message_id and image_key.
func (p *Platform) downloadImage(messageID, imageKey string) ([]byte, string, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build())
	if err != nil {
		return nil, "", fmt.Errorf("feishu: image API: %w", err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("feishu: image API code=%d msg=%s", resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("feishu: read image: %w", err)
	}

	mimeType := detectMimeType(data)
	slog.Debug("feishu: downloaded image", "key", imageKey, "size", len(data), "mime", mimeType)
	return data, mimeType, nil
}

// downloadResource fetches a file resource (audio, etc.) from Feishu by message_id and file_key.
func (p *Platform) downloadResource(messageID, fileKey, resType string) ([]byte, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type(resType).
			Build())
	if err != nil {
		return nil, fmt.Errorf("feishu: resource API: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("feishu: resource API code=%d msg=%s", resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, fmt.Errorf("feishu: read resource: %w", err)
	}
	slog.Debug("feishu: downloaded resource", "key", fileKey, "type", resType, "size", len(data))
	return data, nil
}

func detectMimeType(data []byte) string {
	if len(data) >= 8 {
		if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
			return "image/png"
		}
		if data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
		if string(data[:4]) == "GIF8" {
			return "image/gif"
		}
		if string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			return "image/webp"
		}
	}
	return "image/png"
}

// buildReplyContent decides between plain text and interactive card based on content.
func buildReplyContent(content string) (msgType string, body string) {
	if !containsMarkdown(content) {
		b, _ := json.Marshal(map[string]string{"text": content})
		return larkim.MsgTypeText, string(b)
	}
	return larkim.MsgTypeInteractive, buildCardJSON(adaptMarkdown(content))
}

var markdownIndicators = []string{
	"```", "**", "~~", "\n- ", "\n* ", "\n1. ", "\n# ", "---",
}

func containsMarkdown(s string) bool {
	for _, ind := range markdownIndicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

// adaptMarkdown converts standard markdown to Feishu card-compatible markdown.
// Feishu card markdown elements do NOT support # headers or > blockquotes,
// so we convert them to bold text and indented text respectively.
func adaptMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	inCodeBlock := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		for level := 6; level >= 1; level-- {
			prefix := strings.Repeat("#", level) + " "
			if strings.HasPrefix(line, prefix) {
				lines[i] = "**" + strings.TrimPrefix(line, prefix) + "**"
				break
			}
		}

		if strings.HasPrefix(line, "> ") {
			lines[i] = "  " + strings.TrimPrefix(line, "> ")
		}
	}

	return strings.Join(lines, "\n")
}

func buildCardJSON(content string) string {
	card := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": content,
				},
			},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// feishu:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "feishu" {
		return nil, fmt.Errorf("feishu: invalid session key %q", sessionKey)
	}
	return replyContext{chatID: parts[1]}, nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
