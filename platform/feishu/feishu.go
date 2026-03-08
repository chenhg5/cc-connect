package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

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
	threadID  string // rootId if message is in a thread; empty for top-level
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
		}).
		OnP2MessageReactionDeletedV1(func(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
			return nil // ignore reaction removal events (triggered by our own removeReaction)
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

func (p *Platform) addReaction(messageID string) string {
	if p.reactionEmoji == "" {
		return ""
	}
	emojiType := p.reactionEmoji
	resp, err := p.client.Im.MessageReaction.Create(context.Background(),
		larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(&larkim.Emoji{EmojiType: &emojiType}).
				Build()).
			Build())
	if err != nil {
		slog.Debug("feishu: add reaction failed", "error", err)
		return ""
	}
	if !resp.Success() {
		slog.Debug("feishu: add reaction failed", "code", resp.Code, "msg", resp.Msg)
		return ""
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		return *resp.Data.ReactionId
	}
	return ""
}

func (p *Platform) removeReaction(messageID, reactionID string) {
	if reactionID == "" || messageID == "" {
		return
	}
	resp, err := p.client.Im.MessageReaction.Delete(context.Background(),
		larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build())
	if err != nil {
		slog.Debug("feishu: remove reaction failed", "error", err)
		return
	}
	if !resp.Success() {
		slog.Debug("feishu: remove reaction failed", "code", resp.Code, "msg", resp.Msg)
	}
}

// StartTyping adds an emoji reaction to the user's message and returns a stop
// function that removes the reaction when processing is complete.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.messageID == "" {
		return func() {}
	}
	reactionID := p.addReaction(rc.messageID)
	return func() {
		go p.removeReaction(rc.messageID, reactionID)
	}
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

	if msg.CreateTime != nil {
		if ms, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			msgTime := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if core.IsOldMessage(msgTime) {
				slog.Debug("feishu: ignoring old message after restart", "create_time", *msg.CreateTime)
				return nil
			}
		}
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

	rootID := ""
	if msg.RootId != nil {
		rootID = *msg.RootId
	}

	var sessionKey string
	if rootID != "" {
		sessionKey = fmt.Sprintf("feishu:thread:%s:%s", rootID, userID)
	} else {
		sessionKey = fmt.Sprintf("feishu:msg:%s:%s", messageID, userID)
	}
	rctx := replyContext{messageID: messageID, chatID: chatID, threadID: rootID}

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
			ReplyInThread(true).
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

	msgType, msgBody := buildReplyContent(content)

	if rc.messageID != "" {
		// Reply in thread when we have a message ID
		resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(rc.messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(msgType).
				Content(msgBody).
				ReplyInThread(true).
				Build()).
			Build())
		if err != nil {
			return fmt.Errorf("feishu: send reply api call: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("feishu: send reply failed code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	if rc.chatID == "" {
		return fmt.Errorf("feishu: chatID is empty, cannot send new message")
	}

	// Fallback: send a new message to the chat
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

func buildReplyContent(content string) (msgType string, body string) {
	return larkim.MsgTypeInteractive, buildCardJSON(preprocessFeishuMarkdown(content), core.CardStatusDone)
}

// preprocessFeishuMarkdown ensures code fences have a newline before them,
// which prevents rendering issues in Feishu card markdown.
// Tables, headings, blockquotes, etc. are rendered natively by the card markdown element.
func preprocessFeishuMarkdown(md string) string {
	// Ensure ``` has a newline before it (unless at start of text)
	var b strings.Builder
	b.Grow(len(md) + 32)
	for i := 0; i < len(md); i++ {
		if i > 0 && md[i] == '`' && i+2 < len(md) && md[i+1] == '`' && md[i+2] == '`' && md[i-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteByte(md[i])
	}
	return b.String()
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
	parts := strings.SplitN(sessionKey, ":", 4)
	if len(parts) < 2 || parts[0] != "feishu" {
		return nil, fmt.Errorf("feishu: invalid session key %q", sessionKey)
	}
	switch parts[1] {
	case "msg":
		// Per-message sessions cannot be reconstructed for cron jobs.
		// Each top-level message spawns its own independent session.
		return nil, fmt.Errorf("feishu: cannot reconstruct reply context for per-message session %q (cron not supported)", sessionKey)
	case "thread":
		// Thread sessions cannot be reconstructed for cron jobs without an active thread.
		return nil, fmt.Errorf("feishu: cannot reconstruct reply context for thread session %q (cron not supported)", sessionKey)
	case "chat":
		// Legacy: feishu:chat:{chatID}:{userID}
		if len(parts) < 3 {
			return nil, fmt.Errorf("feishu: invalid chat session key %q", sessionKey)
		}
		return replyContext{chatID: parts[2]}, nil
	default:
		// Oldest legacy: feishu:{chatID}:{userID}
		return replyContext{chatID: parts[1]}, nil
	}
}

// feishuPreviewHandle stores the message ID for an editable preview message.
type feishuPreviewHandle struct {
	messageID   string
	chatID      string
	status      core.CardStatus
	lastContent string
}

// buildCardJSON builds a Feishu interactive card JSON string with a markdown element.
// Uses schema 2.0 which supports code blocks, tables, and inline formatting.
// Card font is inherently smaller than Post/Text — this is a Feishu platform limitation.
func buildCardJSON(content string, status core.CardStatus) string {
	template := "grey"
	switch status {
	case core.CardStatusWorking:
		template = "blue"
	case core.CardStatusDone:
		template = "green"
	case core.CardStatusError:
		template = "red"
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag": "plain_text", "content": "",
			},
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

// SendPreviewStart sends a new card message and returns a handle for subsequent edits.
// Using card (interactive) type for both preview and final message so updates
// are in-place without needing to delete and resend.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("feishu: invalid reply context type %T", rctx)
	}

	processed := preprocessFeishuMarkdown(content)
	cardJSON := buildCardJSON(processed, core.CardStatusThinking)

	var msgID string
	if rc.messageID != "" {
		resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(rc.messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				ReplyInThread(true).
				Build()).
			Build())
		if err != nil {
			return nil, fmt.Errorf("feishu: send preview reply: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu: send preview reply code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	} else {
		chatID := rc.chatID
		if chatID == "" {
			return nil, fmt.Errorf("feishu: chatID is empty")
		}
		resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				Build()).
			Build())
		if err != nil {
			return nil, fmt.Errorf("feishu: send preview: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu: send preview code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	}
	if msgID == "" {
		return nil, fmt.Errorf("feishu: send preview: no message ID returned")
	}

	return &feishuPreviewHandle{messageID: msgID, chatID: rc.chatID, lastContent: processed}, nil
}

// UpdateMessage edits an existing card message identified by previewHandle.
// Uses the Patch API (HTTP PATCH) which is required for interactive card messages.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("feishu: invalid preview handle type %T", previewHandle)
	}

	processed := preprocessFeishuMarkdown(content)
	status := h.status
	if status == "" {
		status = core.CardStatusThinking
	}
	cardJSON := buildCardJSON(processed, status)
	resp, err := p.client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
		MessageId(h.messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu: patch message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu: patch message code=%d msg=%s", resp.Code, resp.Msg)
	}
	h.lastContent = processed
	return nil
}

// SetPreviewStatus updates the card header color of a preview message.
// Implements core.PreviewStatusUpdater.
func (p *Platform) SetPreviewStatus(previewHandle any, status core.CardStatus) {
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return
	}
	h.status = status
	if h.lastContent == "" {
		return
	}
	cardJSON := buildCardJSON(h.lastContent, status)
	resp, err := p.client.Im.Message.Patch(context.Background(), larkim.NewPatchMessageReqBuilder().
		MessageId(h.messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build())
	if err != nil {
		slog.Debug("feishu: set preview status patch failed", "error", err)
		return
	}
	if !resp.Success() {
		slog.Debug("feishu: set preview status patch failed", "code", resp.Code, "msg", resp.Msg)
	}
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

