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
	appID          string
	appSecret      string
	reactionEmoji  string
	allowFrom      string
	groupReplyAll  bool
	client         *lark.Client
	wsClient       *larkws.Client
	handler        core.MessageHandler
	cancel         context.CancelFunc
	dedup          core.MessageDedup
	botOpenID      string
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
	allowFrom, _ := opts["allow_from"].(string)
	groupReplyAll, _ := opts["group_reply_all"].(bool)

	return &Platform{
		appID:         appID,
		appSecret:     appSecret,
		reactionEmoji: reactionEmoji,
		allowFrom:     allowFrom,
		groupReplyAll: groupReplyAll,
		client:        lark.NewClient(appID, appSecret),
	}, nil
}

func (p *Platform) Name() string { return "feishu" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	if openID, err := p.fetchBotOpenID(); err != nil {
		slog.Warn("feishu: failed to get bot open_id, group chat filtering disabled", "error", err)
	} else {
		p.botOpenID = openID
		slog.Info("feishu: bot identified", "open_id", openID)
	}

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
		}).
		OnP2MessageReactionCreatedV1(func(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return nil // ignore reaction events (triggered by our own addReaction)
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

	if p.dedup.IsDuplicate(messageID) {
		slog.Debug("feishu: duplicate message ignored", "message_id", messageID)
		return nil
	}

	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}

	if chatType == "group" && !p.groupReplyAll && p.botOpenID != "" {
		if !isBotMentioned(msg.Mentions, p.botOpenID) {
			slog.Debug("feishu: ignoring group message without bot mention", "chat_id", chatID)
			return nil
		}
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("feishu: message from unauthorized user", "user", userID)
		return nil
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
		text := stripMentions(textBody.Text, msg.Mentions)
		if text == "" {
			return nil
		}
		p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "feishu",
			UserID: userID, UserName: userName,
			Content: text, ReplyCtx: rctx,
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

	case "post":
		textParts, images := p.parsePostContent(messageID, *msg.Content)
		text := stripMentions(strings.Join(textParts, "\n"), msg.Mentions)
		if text == "" && len(images) == 0 {
			return nil
		}
		p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "feishu",
			UserID: userID, UserName: userName,
			Content: text, Images: images,
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

// buildReplyContent converts content to a Feishu message payload.
// Uses "post" (rich text) for markdown content — renders at normal font
// size unlike interactive cards which display smaller.
func buildReplyContent(content string) (msgType string, body string) {
	if !containsMarkdown(content) {
		b, _ := json.Marshal(map[string]string{"text": content})
		return larkim.MsgTypeText, string(b)
	}
	return larkim.MsgTypePost, buildPostJSON(content)
}

var markdownIndicators = []string{
	"```", "**", "~~", "`", "\n- ", "\n* ", "\n1. ", "\n# ", "---",
}

func containsMarkdown(s string) bool {
	for _, ind := range markdownIndicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

// buildPostJSON converts markdown content to Feishu post (rich text) format.
func buildPostJSON(content string) string {
	lines := strings.Split(content, "\n")
	var postLines [][]map[string]any
	inCodeBlock := false
	var codeLines []string
	codeLang := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeLang = strings.TrimPrefix(trimmed, "```")
				codeLines = nil
			} else {
				inCodeBlock = false
				postLines = append(postLines, []map[string]any{{
					"tag":      "code_block",
					"language": codeLang,
					"text":     strings.Join(codeLines, "\n"),
				}})
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Convert # headers to bold
		headerLine := line
		for level := 6; level >= 1; level-- {
			prefix := strings.Repeat("#", level) + " "
			if strings.HasPrefix(line, prefix) {
				headerLine = "**" + strings.TrimPrefix(line, prefix) + "**"
				break
			}
		}

		elements := parseInlineMarkdown(headerLine)
		if len(elements) > 0 {
			postLines = append(postLines, elements)
		} else {
			postLines = append(postLines, []map[string]any{{"tag": "text", "text": ""}})
		}
	}

	// Handle unclosed code block
	if inCodeBlock && len(codeLines) > 0 {
		postLines = append(postLines, []map[string]any{{
			"tag":      "code_block",
			"language": codeLang,
			"text":     strings.Join(codeLines, "\n"),
		}})
	}

	post := map[string]any{
		"zh_cn": map[string]any{
			"content": postLines,
		},
	}
	b, _ := json.Marshal(post)
	return string(b)
}

// parseInlineMarkdown parses a single line of markdown into Feishu post elements.
// Supports **bold** and `code` inline formatting.
func parseInlineMarkdown(line string) []map[string]any {
	var elements []map[string]any
	remaining := line

	for len(remaining) > 0 {
		// Find the next formatting marker
		boldIdx := strings.Index(remaining, "**")
		codeIdx := strings.Index(remaining, "`")

		// No more markers
		if boldIdx < 0 && codeIdx < 0 {
			if remaining != "" {
				elements = append(elements, map[string]any{"tag": "text", "text": remaining})
			}
			break
		}

		// Determine which marker comes first
		nextIdx := boldIdx
		marker := "**"
		if boldIdx < 0 || (codeIdx >= 0 && codeIdx < boldIdx) {
			nextIdx = codeIdx
			marker = "`"
		}

		// Add text before the marker
		if nextIdx > 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": remaining[:nextIdx]})
		}
		remaining = remaining[nextIdx+len(marker):]

		// Find closing marker
		closeIdx := strings.Index(remaining, marker)
		if closeIdx < 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": marker + remaining})
			remaining = ""
			break
		}

		inner := remaining[:closeIdx]
		remaining = remaining[closeIdx+len(marker):]

		if marker == "**" {
			elements = append(elements, map[string]any{
				"tag":   "text",
				"text":  inner,
				"style": []string{"bold"},
			})
		} else {
			elements = append(elements, map[string]any{
				"tag":   "text",
				"text":  inner,
				"style": []string{"code"},
			})
		}
	}

	return elements
}

// fetchBotOpenID retrieves the bot's open_id via the Feishu bot info API.
func (p *Platform) fetchBotOpenID() (string, error) {
	resp, err := p.client.Get(context.Background(),
		"/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("api code=%d", result.Code)
	}
	return result.Bot.OpenID, nil
}

func isBotMentioned(mentions []*larkim.MentionEvent, botOpenID string) bool {
	for _, m := range mentions {
		if m.Id != nil && m.Id.OpenId != nil && *m.Id.OpenId == botOpenID {
			return true
		}
	}
	return false
}

// stripMentions removes @mention placeholders (e.g. @_user_1) from text
// so that group-chat messages like "@Bot /help" become "/help".
func stripMentions(text string, mentions []*larkim.MentionEvent) string {
	if len(mentions) == 0 {
		return text
	}
	for _, m := range mentions {
		if m.Key != nil {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	return strings.TrimSpace(text)
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

type postElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
	Href     string `json:"href,omitempty"`
}

type postLang struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

// parsePostContent handles both formats of feishu post content:
// 1. {"title":"...", "content":[[...]]}  (receive event)
// 2. {"zh_cn":{"title":"...", "content":[[...]]}}  (some SDK versions)
func (p *Platform) parsePostContent(messageID, raw string) ([]string, []core.ImageAttachment) {
	// try flat format first
	var flat postLang
	if err := json.Unmarshal([]byte(raw), &flat); err == nil && flat.Content != nil {
		return p.extractPostParts(messageID, &flat)
	}
	// try language-keyed format
	var langMap map[string]postLang
	if err := json.Unmarshal([]byte(raw), &langMap); err == nil {
		for _, lang := range langMap {
			return p.extractPostParts(messageID, &lang)
		}
	}
	slog.Error("feishu: failed to parse post content", "raw", raw)
	return nil, nil
}

func (p *Platform) extractPostParts(messageID string, post *postLang) ([]string, []core.ImageAttachment) {
	var textParts []string
	var images []core.ImageAttachment
	if post.Title != "" {
		textParts = append(textParts, post.Title)
	}
	for _, line := range post.Content {
		for _, elem := range line {
			switch elem.Tag {
			case "text":
				if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "a":
				if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "img":
				if elem.ImageKey != "" {
					imgData, mimeType, err := p.downloadImage(messageID, elem.ImageKey)
					if err != nil {
						slog.Error("feishu: download post image failed", "error", err, "key", elem.ImageKey)
						continue
					}
					images = append(images, core.ImageAttachment{MimeType: mimeType, Data: imgData})
				}
			}
		}
	}
	return textParts, images
}
