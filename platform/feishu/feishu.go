package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func init() {
	core.RegisterPlatform("feishu", func(opts map[string]any) (core.Platform, error) {
		return newPlatform("feishu", lark.FeishuBaseUrl, opts)
	})
	core.RegisterPlatform("lark", func(opts map[string]any) (core.Platform, error) {
		return newPlatform("lark", lark.LarkBaseUrl, opts)
	})
}

type replyContext struct {
	messageID string
	chatID    string
}

type Platform struct {
	platformName          string
	domain                string
	appID                 string
	appSecret             string
	useInteractiveCard    bool
	self                  core.Platform
	reactionEmoji         string
	allowFrom             string
	groupReplyAll         bool
	shareSessionInChannel bool
	replyInThread         bool
	client                *lark.Client
	wsClient              *larkws.Client
	handler               core.MessageHandler
	cardNavHandler        core.CardNavigationHandler
	cancel                context.CancelFunc
	dedup                 core.MessageDedup
	botOpenID             string
}

type interactivePlatform struct {
	*Platform
}

func (p *Platform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	p.cardNavHandler = h
}

func New(opts map[string]any) (core.Platform, error) {
	return newPlatform("feishu", lark.FeishuBaseUrl, opts)
}

func newPlatform(name, domain string, opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("%s: app_id and app_secret are required", name)
	}
	reactionEmoji, _ := opts["reaction_emoji"].(string)
	if reactionEmoji == "" {
		reactionEmoji = "OnIt"
	}
	if v, ok := opts["reaction_emoji"].(string); ok && v == "none" {
		reactionEmoji = ""
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom(name, allowFrom)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	replyInThread, _ := opts["reply_in_thread"].(bool)
	useInteractiveCard := true
	if v, ok := opts["enable_feishu_card"].(bool); ok {
		useInteractiveCard = v
	}

	var clientOpts []lark.ClientOptionFunc
	if domain != lark.FeishuBaseUrl {
		clientOpts = append(clientOpts, lark.WithOpenBaseUrl(domain))
	}

	base := &Platform{
		platformName:          name,
		domain:                domain,
		appID:                 appID,
		appSecret:             appSecret,
		useInteractiveCard:    useInteractiveCard,
		reactionEmoji:         reactionEmoji,
		allowFrom:             allowFrom,
		groupReplyAll:         groupReplyAll,
		shareSessionInChannel: shareSessionInChannel,
		replyInThread:         replyInThread,
		client:                lark.NewClient(appID, appSecret, clientOpts...),
	}
	if !useInteractiveCard {
		base.self = base
		return base, nil
	}
	wrapped := &interactivePlatform{Platform: base}
	base.self = wrapped
	return wrapped, nil
}

func (p *Platform) Name() string { return p.platformName }

func (p *Platform) tag() string { return p.platformName }

func (p *Platform) dispatchPlatform() core.Platform {
	if p.self != nil {
		return p.self
	}
	return p
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	if openID, err := p.fetchBotOpenID(); err != nil {
		slog.Warn(p.platformName+": failed to get bot open_id, group chat filtering disabled", "error", err)
	} else {
		p.botOpenID = openID
		slog.Info(p.platformName+": bot identified", "open_id", openID)
	}

	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			slog.Debug(p.platformName+": message received", "app_id", p.appID)
			return p.onMessage(event)
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return nil // ignore read receipts
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			slog.Debug(p.platformName+": user opened bot chat", "app_id", p.appID)
			return nil
		}).
		OnP1P2PChatCreatedV1(func(ctx context.Context, event *larkim.P1P2PChatCreatedV1) error {
			slog.Debug(p.platformName+": p2p chat created", "app_id", p.appID)
			return nil
		}).
		OnP2MessageReactionCreatedV1(func(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return nil // ignore reaction events (triggered by our own addReaction)
		}).
		OnP2MessageReactionDeletedV1(func(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
			return nil // ignore reaction removal events (triggered by our own removeReaction)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return p.onCardAction(event)
		})

	wsOpts := []larkws.ClientOption{
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	}
	if p.domain != lark.FeishuBaseUrl {
		wsOpts = append(wsOpts, larkws.WithDomain(p.domain))
	}
	p.wsClient = larkws.NewClient(p.appID, p.appSecret, wsOpts...)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		if err := p.wsClient.Start(ctx); err != nil {
			slog.Error(p.tag()+": websocket error", "error", err)
		}
	}()

	return nil
}

// onCardAction handles card.action.trigger callbacks via the official SDK event dispatcher.
// Three prefixes are supported:
//   - nav:/xxx   — render a card page and update the original card in-place
//   - act:/xxx   — execute an action, then render and update the card in-place
//   - cmd:/xxx   — legacy: dispatch as a user command (sends a new message)
func (p *Platform) onCardAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event.Event == nil || event.Event.Action == nil {
		return nil, nil
	}

	actionVal, _ := event.Event.Action.Value["action"].(string)

	// select_static callbacks put the chosen value in event.Event.Action.Option
	if actionVal == "" && event.Event.Action.Option != "" {
		actionVal = event.Event.Action.Option
	}

	userID := ""
	if event.Event.Operator != nil {
		userID = event.Event.Operator.OpenID
	}
	chatID := ""
	messageID := ""
	if event.Event.Context != nil {
		chatID = event.Event.Context.OpenChatID
		messageID = event.Event.Context.OpenMessageID
	}
	if chatID == "" {
		chatID = userID
	}
	sessionKey := fmt.Sprintf("%s:%s:%s", p.tag(), chatID, userID)

	// nav: / act: — synchronous card update
	if strings.HasPrefix(actionVal, "nav:") || strings.HasPrefix(actionVal, "act:") {
		if p.cardNavHandler != nil {
			card := p.cardNavHandler(actionVal, sessionKey)
			if card != nil {
				return &callback.CardActionTriggerResponse{
					Card: &callback.Card{
						Type: "raw",
						Data: renderCardMap(card),
					},
				}, nil
			}
		}
		slog.Warn(p.tag()+": card nav returned nil, ignoring", "action", actionVal)
		return nil, nil
	}

	// perm: — permission response with in-place card update
	if strings.HasPrefix(actionVal, "perm:") {
		var responseText string
		switch actionVal {
		case "perm:allow":
			responseText = "allow"
		case "perm:deny":
			responseText = "deny"
		case "perm:allow_all":
			responseText = "allow all"
		default:
			return nil, nil
		}

		rctx := replyContext{messageID: messageID, chatID: chatID}
		go p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			Content:    responseText,
			ReplyCtx:   rctx,
		})

		permLabel, _ := event.Event.Action.Value["perm_label"].(string)
		permColor, _ := event.Event.Action.Value["perm_color"].(string)
		permBody, _ := event.Event.Action.Value["perm_body"].(string)
		if permColor == "" {
			permColor = "green"
		}
		cb := core.NewCard().Title(permLabel, permColor)
		if permBody != "" {
			cb.Markdown(permBody)
		}
		return &callback.CardActionTriggerResponse{
			Card: &callback.Card{
				Type: "raw",
				Data: renderCardMap(cb.Build()),
			},
		}, nil
	}

	// cmd: — async command dispatch
	if strings.HasPrefix(actionVal, "cmd:") {
		cmdText := strings.TrimPrefix(actionVal, "cmd:")
		rctx := replyContext{messageID: messageID, chatID: chatID}

		slog.Info(p.tag()+": card action dispatched as command", "cmd", cmdText, "user", userID)

		go p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			Content:    cmdText,
			ReplyCtx:   rctx,
		})
	}

	return nil, nil
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
		slog.Debug(p.tag()+": add reaction failed", "error", err)
		return ""
	}
	if !resp.Success() {
		slog.Debug(p.tag()+": add reaction failed", "code", resp.Code, "msg", resp.Msg)
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
		slog.Debug(p.tag()+": remove reaction failed", "error", err)
		return
	}
	if !resp.Success() {
		slog.Debug(p.tag()+": remove reaction failed", "code", resp.Code, "msg", resp.Msg)
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
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
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
		slog.Debug(p.tag()+": duplicate message ignored", "message_id", messageID)
		return nil
	}

	if msg.CreateTime != nil {
		if ms, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			msgTime := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if core.IsOldMessage(msgTime) {
				slog.Debug(p.tag()+": ignoring old message after restart", "create_time", *msg.CreateTime)
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
			slog.Debug(p.tag()+": ignoring group message without bot mention", "chat_id", chatID)
			return nil
		}
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug(p.tag()+": message from unauthorized user", "user", userID)
		return nil
	}

	if msg.Content == nil {
		slog.Debug(p.tag()+": message content is nil", "message_id", messageID, "type", msgType)
		return nil
	}

	var sessionKey string
	if p.shareSessionInChannel {
		sessionKey = fmt.Sprintf("%s:%s", p.tag(), chatID)
	} else {
		sessionKey = fmt.Sprintf("%s:%s:%s", p.tag(), chatID, userID)
	}
	rctx := replyContext{messageID: messageID, chatID: chatID}

	switch msgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(*msg.Content), &textBody); err != nil {
			slog.Error(p.tag()+": failed to parse text content", "error", err)
			return nil
		}
		text := stripMentions(textBody.Text, msg.Mentions)
		if text == "" {
			return nil
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName,
			Content: text, ReplyCtx: rctx,
		})

	case "image":
		var imgBody struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(*msg.Content), &imgBody); err != nil {
			slog.Error(p.tag()+": failed to parse image content", "error", err)
			return nil
		}
		imgData, mimeType, err := p.downloadImage(messageID, imgBody.ImageKey)
		if err != nil {
			slog.Error(p.tag()+": download image failed", "error", err)
			return nil
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName,
			Images:   []core.ImageAttachment{{MimeType: mimeType, Data: imgData}},
			ReplyCtx: rctx,
		})

	case "audio":
		var audioBody struct {
			FileKey  string `json:"file_key"`
			Duration int    `json:"duration"` // milliseconds
		}
		if err := json.Unmarshal([]byte(*msg.Content), &audioBody); err != nil {
			slog.Error(p.tag()+": failed to parse audio content", "error", err)
			return nil
		}
		slog.Debug(p.tag()+": audio received", "user", userID, "file_key", audioBody.FileKey)
		audioData, err := p.downloadResource(messageID, audioBody.FileKey, "file")
		if err != nil {
			slog.Error(p.tag()+": download audio failed", "error", err)
			return nil
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName,
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
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName,
			Content: text, Images: images,
			ReplyCtx: rctx,
		})

	default:
		slog.Debug(p.tag()+": ignoring unsupported message type", "type", msgType)
	}

	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
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
		return fmt.Errorf("%s: reply api call: %w", p.tag(), err)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: reply failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	return nil
}

// Send sends a new message to the same chat (not a reply to original message).
// When reply_in_thread is enabled, threads the message to the original message instead.
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	if p.replyInThread && rc.messageID != "" {
		return p.Reply(ctx, rctx, content)
	}

	if rc.chatID == "" {
		return fmt.Errorf("%s: chatID is empty, cannot send new message", p.tag())
	}

	msgType, msgBody := buildReplyContent(content)

	resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(rc.chatID).
			MsgType(msgType).
			Content(msgBody).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("%s: send api call: %w", p.tag(), err)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: send failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	return nil
}

func (p *Platform) downloadImage(messageID, imageKey string) ([]byte, string, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build())
	if err != nil {
		return nil, "", fmt.Errorf("%s: image API: %w", p.tag(), err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("%s: image API code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("%s: read image: %w", p.tag(), err)
	}

	mimeType := detectMimeType(data)
	slog.Debug(p.tag()+": downloaded image", "key", imageKey, "size", len(data), "mime", mimeType)
	return data, mimeType, nil
}

func (p *Platform) downloadResource(messageID, fileKey, resType string) ([]byte, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type(resType).
			Build())
	if err != nil {
		return nil, fmt.Errorf("%s: resource API: %w", p.tag(), err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("%s: resource API code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, fmt.Errorf("%s: read resource: %w", p.tag(), err)
	}
	slog.Debug(p.tag()+": downloaded resource", "key", fileKey, "type", resType, "size", len(data))
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
	if !containsMarkdown(content) {
		b, _ := json.Marshal(map[string]string{"text": content})
		return larkim.MsgTypeText, string(b)
	}
	// Three-tier rendering strategy:
	// 1. Code blocks / tables → card (schema 2.0 markdown)
	// 2. Many \n\n paragraphs (help, status, etc.) → post rich-text (preserves blank lines)
	// 3. Other markdown → post md tag (best native rendering)
	if hasComplexMarkdown(content) {
		return larkim.MsgTypeInteractive, buildCardJSON(sanitizeMarkdownURLs(preprocessFeishuMarkdown(content)))
	}
	if strings.Count(content, "\n\n") >= 2 {
		return larkim.MsgTypePost, buildPostJSON(content)
	}
	return larkim.MsgTypePost, buildPostMdJSON(content)
}

// hasComplexMarkdown detects code blocks or tables that require card rendering.
func hasComplexMarkdown(s string) bool {
	if strings.Contains(s, "```") {
		return true
	}
	// Table: line starting and ending with |
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 1 && trimmed[0] == '|' && trimmed[len(trimmed)-1] == '|' {
			return true
		}
	}
	return false
}

// buildPostMdJSON builds a Feishu post message using the md tag,
// which renders markdown at normal chat font size.
func buildPostMdJSON(content string) string {
	content = sanitizeMarkdownURLs(content)
	post := map[string]any{
		"zh_cn": map[string]any{
			"content": [][]map[string]any{
				{
					{"tag": "md", "text": content},
				},
			},
		},
	}
	b, _ := json.Marshal(post)
	return string(b)
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

// isValidFeishuHref checks whether a URL is acceptable as a Feishu post href.
// Feishu rejects non-HTTP(S) URLs with "invalid href" (code 230001).
func isValidFeishuHref(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// sanitizeMarkdownURLs rewrites markdown links with non-HTTP(S) schemes
// to plain text, preventing Feishu API rejection (code 230001).
func sanitizeMarkdownURLs(md string) string {
	return mdLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		if isValidFeishuHref(parts[2]) {
			return match
		}
		// Convert invalid-scheme link to "text (url)" plain text
		return parts[1] + " (" + parts[2] + ")"
	})
}

// parseInlineMarkdown parses a single line of markdown into Feishu post elements.
// Supports **bold** and `code` inline formatting.
func parseInlineMarkdown(line string) []map[string]any {
	type markerDef struct {
		pattern string
		tag     string
		style   string // for text elements with style
		isLink  bool
	}
	markers := []markerDef{
		{pattern: "**", tag: "text", style: "bold"},
		{pattern: "~~", tag: "text", style: "lineThrough"},
		{pattern: "`", tag: "text", style: "code"},
		{pattern: "*", tag: "text", style: "italic"},
	}

	var elements []map[string]any
	remaining := line

	for len(remaining) > 0 {
		// Check for link [text](url)
		linkIdx := strings.Index(remaining, "[")
		if linkIdx >= 0 {
			parenClose := -1
			bracketClose := strings.Index(remaining[linkIdx:], "](")
			if bracketClose >= 0 {
				bracketClose += linkIdx
				parenClose = strings.Index(remaining[bracketClose+2:], ")")
				if parenClose >= 0 {
					parenClose += bracketClose + 2
				}
			}
			if parenClose >= 0 {
				// Check if any marker comes before this link
				foundEarlierMarker := false
				for _, m := range markers {
					idx := strings.Index(remaining, m.pattern)
					if idx >= 0 && idx < linkIdx {
						foundEarlierMarker = true
						break
					}
				}
				if !foundEarlierMarker {
					linkText := remaining[linkIdx+1 : bracketClose]
					linkURL := remaining[bracketClose+2 : parenClose]
					if isValidFeishuHref(linkURL) {
						if linkIdx > 0 {
							elements = append(elements, map[string]any{"tag": "text", "text": remaining[:linkIdx]})
						}
						elements = append(elements, map[string]any{
							"tag":  "a",
							"text": linkText,
							"href": linkURL,
						})
						remaining = remaining[parenClose+1:]
						continue
					}
				}
			}
		}

		// Find the earliest formatting marker
		bestIdx := -1
		var bestMarker markerDef
		for _, m := range markers {
			idx := strings.Index(remaining, m.pattern)
			if idx < 0 {
				continue
			}
			// For single * marker, skip if it's actually ** (bold)
			if m.pattern == "*" && idx+1 < len(remaining) && remaining[idx+1] == '*' {
				idx = findSingleAsterisk(remaining)
				if idx < 0 {
					continue
				}
			}
			if bestIdx < 0 || idx < bestIdx {
				bestIdx = idx
				bestMarker = m
			}
		}

		if bestIdx < 0 {
			if remaining != "" {
				elements = append(elements, map[string]any{"tag": "text", "text": remaining})
			}
			break
		}

		if bestIdx > 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": remaining[:bestIdx]})
		}
		remaining = remaining[bestIdx+len(bestMarker.pattern):]

		closeIdx := strings.Index(remaining, bestMarker.pattern)
		// For single *, make sure we don't match ** as close
		if bestMarker.pattern == "*" {
			closeIdx = findSingleAsterisk(remaining)
		}
		if closeIdx < 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": bestMarker.pattern + remaining})
			remaining = ""
			break
		}

		inner := remaining[:closeIdx]
		remaining = remaining[closeIdx+len(bestMarker.pattern):]

		elements = append(elements, map[string]any{
			"tag":   bestMarker.tag,
			"text":  inner,
			"style": []string{bestMarker.style},
		})
	}

	return elements
}

// findSingleAsterisk finds the index of a single '*' not part of '**' in s.
func findSingleAsterisk(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			if i+1 < len(s) && s[i+1] == '*' {
				i++ // skip **
				continue
			}
			return i
		}
	}
	return -1
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
	// {platformName}:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != p.platformName {
		return nil, fmt.Errorf("%s: invalid session key %q", p.tag(), sessionKey)
	}
	return replyContext{chatID: parts[1]}, nil
}

// feishuPreviewHandle stores the message ID for an editable preview message.
type feishuPreviewHandle struct {
	messageID string
	chatID    string
}

// buildCardJSON builds a Feishu interactive card JSON string with a markdown element.
// Uses schema 2.0 which supports code blocks, tables, and inline formatting.
// Card font is inherently smaller than Post/Text — this is a Feishu platform limitation.
func buildCardJSON(content string) string {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
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
		return nil, fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	chatID := rc.chatID
	if chatID == "" {
		return nil, fmt.Errorf("%s: chatID is empty", p.tag())
	}

	cardJSON := buildCardJSON(sanitizeMarkdownURLs(content))

	var msgID string
	if p.replyInThread && rc.messageID != "" {
		resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(rc.messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				Build()).
			Build())
		if err != nil {
			return nil, fmt.Errorf("%s: send preview (reply): %w", p.tag(), err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("%s: send preview (reply) code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	} else {
		resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				Build()).
			Build())
		if err != nil {
			return nil, fmt.Errorf("%s: send preview: %w", p.tag(), err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("%s: send preview code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	}

	if msgID == "" {
		return nil, fmt.Errorf("%s: send preview: no message ID returned", p.tag())
	}

	return &feishuPreviewHandle{messageID: msgID, chatID: chatID}, nil
}

// UpdateMessage edits an existing card message identified by previewHandle.
// Uses the Patch API (HTTP PATCH) which is required for interactive card messages.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("%s: invalid preview handle type %T", p.tag(), previewHandle)
	}

	processed := content
	if containsMarkdown(content) {
		processed = preprocessFeishuMarkdown(content)
	}
	cardJSON := buildCardJSON(sanitizeMarkdownURLs(processed))
	resp, err := p.client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
		MessageId(h.messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("%s: patch message: %w", p.tag(), err)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: patch message code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

// SendAudio uploads audio bytes to Feishu and sends a voice message.
// Implements core.AudioSender interface.
// Feishu audio messages require opus format; non-opus input is converted via ffmpeg.
func (p *Platform) SendAudio(ctx context.Context, rctx any, audio []byte, format string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendAudio: invalid reply context type %T", p.tag(), rctx)
	}

	if format != "opus" {
		converted, err := core.ConvertAudioToOpus(ctx, audio, format)
		if err != nil {
			return fmt.Errorf("%s: convert to opus: %w", p.tag(), err)
		}
		audio = converted
		format = "opus"
	}

	uploadResp, err := p.client.Im.File.Create(ctx,
		larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(larkim.FileTypeOpus).
				FileName("tts_audio.opus").
				File(bytes.NewReader(audio)).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("%s: upload audio: %w", p.tag(), err)
	}
	if !uploadResp.Success() {
		return fmt.Errorf("%s: upload audio code=%d msg=%s", p.tag(), uploadResp.Code, uploadResp.Msg)
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("%s: upload audio: no file_key returned", p.tag())
	}
	fileKey := *uploadResp.Data.FileKey

	slog.Debug(p.tag()+": audio uploaded", "file_key", fileKey, "format", format, "size", len(audio))

	audioMsg := larkim.MessageAudio{FileKey: fileKey}
	audioContent, err := audioMsg.String()
	if err != nil {
		return fmt.Errorf("%s: build audio message: %w", p.tag(), err)
	}

	// Send audio message to chat
	sendResp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(rc.chatID).
			MsgType(larkim.MsgTypeAudio).
			Content(audioContent).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("%s: send audio message: %w", p.tag(), err)
	}
	if !sendResp.Success() {
		return fmt.Errorf("%s: send audio message code=%d msg=%s", p.tag(), sendResp.Code, sendResp.Msg)
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
	slog.Error(p.tag()+": failed to parse post content", "raw", raw)
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
						slog.Error(p.tag()+": download post image failed", "error", err, "key", elem.ImageKey)
						continue
					}
					images = append(images, core.ImageAttachment{MimeType: mimeType, Data: imgData})
				}
			}
		}
	}
	return textParts, images
}
