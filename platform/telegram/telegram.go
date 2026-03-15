package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

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
	token                 string
	allowFrom             string
	groupReplyAll         bool
	shareSessionInChannel bool
	bot                   *tgbotapi.BotAPI
	httpClient            *http.Client
	handler               core.MessageHandler
	cancel                context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("telegram", allowFrom)

	// Build HTTP client with optional proxy support
	httpClient := &http.Client{Timeout: 60 * time.Second}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("telegram: invalid proxy URL %q: %w", proxyURL, err)
		}
		proxyUser, _ := opts["proxy_username"].(string)
		proxyPass, _ := opts["proxy_password"].(string)
		if proxyUser != "" {
			u.User = url.UserPassword(proxyUser, proxyPass)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("telegram: using proxy", "proxy", u.Host, "auth", proxyUser != "")
	}

	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	return &Platform{token: token, allowFrom: allowFrom, groupReplyAll: groupReplyAll, shareSessionInChannel: shareSessionInChannel, httpClient: httpClient}, nil
}

func (p *Platform) Name() string { return "telegram" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	bot, err := tgbotapi.NewBotAPIWithClient(p.token, tgbotapi.APIEndpoint, p.httpClient)
	if err != nil {
		return fmt.Errorf("telegram: auth failed: %w", err)
	}
	p.bot = bot

	slog.Info("telegram: connected", "bot", bot.Self.UserName)

	// Drain pending updates from previous session to avoid re-processing old messages.
	// offset -1 tells Telegram to mark all pending updates as confirmed, returning only the latest one.
	drain := tgbotapi.NewUpdate(-1)
	drain.Timeout = 0
	if _, err := bot.GetUpdates(drain); err != nil {
		slog.Warn("telegram: failed to drain old updates", "error", err)
	}

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
			case update, ok := <-updates:
				if !ok {
					return
				}
				// Handle inline keyboard button clicks
				if update.CallbackQuery != nil {
					p.handleCallbackQuery(update.CallbackQuery)
					continue
				}

				if update.Message == nil {
					continue
				}

				msg := update.Message
				msgTime := time.Unix(int64(msg.Date), 0)
				if core.IsOldMessage(msgTime) {
					slog.Debug("telegram: ignoring old message after restart", "date", msgTime)
					continue
				}
				userName := msg.From.UserName
				if userName == "" {
					userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
				}
				var sessionKey string
				if p.shareSessionInChannel {
					sessionKey = fmt.Sprintf("telegram:%d", msg.Chat.ID)
				} else {
					sessionKey = fmt.Sprintf("telegram:%d:%d", msg.Chat.ID, msg.From.ID)
				}
				userID := strconv.FormatInt(msg.From.ID, 10)
				if !core.AllowList(p.allowFrom, userID) {
					slog.Debug("telegram: message from unauthorized user", "user", userID)
					continue
				}

				isGroup := msg.Chat.Type == "group" || msg.Chat.Type == "supergroup"
				chatName := ""
				if isGroup {
					chatName = msg.Chat.Title
				}

				// In group chats, filter messages not directed at this bot (unless group_reply_all)
				if isGroup && !p.groupReplyAll {
					slog.Debug("telegram: checking group message", "bot", p.bot.Self.UserName, "text", msg.Text, "is_command", msg.IsCommand())
					if !p.isDirectedAtBot(msg) {
						continue
					}
				}

				rctx := replyContext{chatID: msg.Chat.ID, messageID: msg.MessageID}

				// Handle photo messages
				if msg.Photo != nil && len(msg.Photo) > 0 {
					best := msg.Photo[len(msg.Photo)-1]
					imgData, err := p.downloadFile(best.FileID)
					if err != nil {
						slog.Error("telegram: download photo failed", "error", err)
						continue
					}
					caption := msg.Caption
					if p.bot.Self.UserName != "" {
						caption = strings.ReplaceAll(caption, "@"+p.bot.Self.UserName, "")
						caption = strings.TrimSpace(caption)
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: userID, UserName: userName, ChatName: chatName,
						Content:   caption,
						MessageID: strconv.Itoa(msg.MessageID),
						Images:    []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
						ReplyCtx:  rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				// Handle voice messages
				if msg.Voice != nil {
					slog.Debug("telegram: voice received", "user", userName, "duration", msg.Voice.Duration)
					audioData, err := p.downloadFile(msg.Voice.FileID)
					if err != nil {
						slog.Error("telegram: download voice failed", "error", err)
						continue
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: userID, UserName: userName, ChatName: chatName,
						MessageID: strconv.Itoa(msg.MessageID),
						Audio: &core.AudioAttachment{
							MimeType: msg.Voice.MimeType,
							Data:     audioData,
							Format:   "ogg",
							Duration: msg.Voice.Duration,
						},
						ReplyCtx: rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				// Handle audio file messages
				if msg.Audio != nil {
					slog.Debug("telegram: audio file received", "user", userName)
					audioData, err := p.downloadFile(msg.Audio.FileID)
					if err != nil {
						slog.Error("telegram: download audio failed", "error", err)
						continue
					}
					format := "mp3"
					if msg.Audio.MimeType != "" {
						parts := strings.SplitN(msg.Audio.MimeType, "/", 2)
						if len(parts) == 2 {
							format = parts[1]
						}
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: userID, UserName: userName, ChatName: chatName,
						MessageID: strconv.Itoa(msg.MessageID),
						Audio: &core.AudioAttachment{
							MimeType: msg.Audio.MimeType,
							Data:     audioData,
							Format:   format,
							Duration: msg.Audio.Duration,
						},
						ReplyCtx: rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				// Handle document (file) messages
				if msg.Document != nil {
					slog.Info("telegram: document received", "user", userName, "file_name", msg.Document.FileName, "mime", msg.Document.MimeType, "file_id", msg.Document.FileID)
					fileData, err := p.downloadFile(msg.Document.FileID)
					if err != nil {
						slog.Error("telegram: download document failed", "error", err)
						continue
					}
					caption := msg.Caption
					if p.bot.Self.UserName != "" {
						caption = strings.ReplaceAll(caption, "@"+p.bot.Self.UserName, "")
						caption = strings.TrimSpace(caption)
					}
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: userID, UserName: userName, ChatName: chatName,
						Content:   caption,
						MessageID: strconv.Itoa(msg.MessageID),
						Files:     []core.FileAttachment{{MimeType: msg.Document.MimeType, Data: fileData, FileName: msg.Document.FileName}},
						ReplyCtx:  rctx,
					}
					p.handler(p, coreMsg)
					continue
				}

				if msg.Text == "" {
					continue
				}

				text := msg.Text
				if p.bot.Self.UserName != "" {
					text = strings.ReplaceAll(text, "@"+p.bot.Self.UserName, "")
					text = strings.TrimSpace(text)
				}

				coreMsg := &core.Message{
					SessionKey: sessionKey, Platform: "telegram",
					UserID: userID, UserName: userName, ChatName: chatName,
					Content:   text,
					MessageID: strconv.Itoa(msg.MessageID),
					ReplyCtx:  rctx,
				}

				slog.Debug("telegram: message received", "user", userName, "chat", msg.Chat.ID)
				p.handler(p, coreMsg)
			}
		}
	}()

	return nil
}

func (p *Platform) handleCallbackQuery(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.From == nil {
		return
	}

	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	userID := strconv.FormatInt(cb.From.ID, 10)

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("telegram: callback from unauthorized user", "user", userID)
		return
	}

	// Answer the callback to clear the loading indicator
	answer := tgbotapi.NewCallback(cb.ID, "")
	p.bot.Request(answer)

	userName := cb.From.UserName
	if userName == "" {
		userName = strings.TrimSpace(cb.From.FirstName + " " + cb.From.LastName)
	}
	var sessionKey string
	if p.shareSessionInChannel {
		sessionKey = fmt.Sprintf("telegram:%d", chatID)
	} else {
		sessionKey = fmt.Sprintf("telegram:%d:%d", chatID, cb.From.ID)
	}
	isGroup := cb.Message.Chat.Type == "group" || cb.Message.Chat.Type == "supergroup"
	chatName := ""
	if isGroup {
		chatName = cb.Message.Chat.Title
	}
	rctx := replyContext{chatID: chatID, messageID: msgID}

	// Command callbacks (cmd:/lang en, cmd:/mode yolo, etc.)
	if strings.HasPrefix(data, "cmd:") {
		command := strings.TrimPrefix(data, "cmd:")

		// Edit original message: append the chosen option and remove buttons
		origText := cb.Message.Text
		if origText == "" {
			origText = ""
		}
		edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n> "+command)
		emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
		edit.ReplyMarkup = &emptyMarkup
		p.bot.Send(edit)

		p.handler(p, &core.Message{
			SessionKey: sessionKey,
			Platform:   "telegram",
			UserID:     userID,
			UserName:   userName,
			ChatName:   chatName,
			Content:    command,
			MessageID:  strconv.Itoa(msgID),
			ReplyCtx:   rctx,
		})
		return
	}

	// AskUserQuestion callbacks (askq:qIdx:optIdx)
	if strings.HasPrefix(data, "askq:") {
		// Extract label from after the last colon for display
		parts := strings.SplitN(data, ":", 3)
		choiceLabel := data
		if len(parts) == 3 {
			// Try to find the option label from the original message buttons
			for _, row := range cb.Message.ReplyMarkup.InlineKeyboard {
				for _, btn := range row {
					if btn.CallbackData != nil && *btn.CallbackData == data {
						choiceLabel = "✅ " + btn.Text
					}
				}
			}
		}

		origText := cb.Message.Text
		if origText == "" {
			origText = "(question)"
		}
		edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n"+choiceLabel)
		emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
		edit.ReplyMarkup = &emptyMarkup
		p.bot.Send(edit)

		p.handler(p, &core.Message{
			SessionKey: sessionKey,
			Platform:   "telegram",
			UserID:     userID,
			UserName:   userName,
			ChatName:   chatName,
			Content:    data,
			MessageID:  strconv.Itoa(msgID),
			ReplyCtx:   rctx,
		})
		return
	}

	// Permission callbacks (perm:allow, perm:deny, perm:allow_all)
	var responseText string
	switch data {
	case "perm:allow":
		responseText = "allow"
	case "perm:deny":
		responseText = "deny"
	case "perm:allow_all":
		responseText = "allow all"
	default:
		slog.Debug("telegram: unknown callback data", "data", data)
		return
	}

	choiceLabel := responseText
	switch data {
	case "perm:allow":
		choiceLabel = "✅ Allowed"
	case "perm:deny":
		choiceLabel = "❌ Denied"
	case "perm:allow_all":
		choiceLabel = "✅ Allow All"
	}

	origText := cb.Message.Text
	if origText == "" {
		origText = "(permission request)"
	}
	edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n"+choiceLabel)
	emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
	edit.ReplyMarkup = &emptyMarkup
	p.bot.Send(edit)

	p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "telegram",
		UserID:     userID,
		UserName:   userName,
		ChatName:   chatName,
		Content:    responseText,
		MessageID:  strconv.Itoa(msgID),
		ReplyCtx:   rctx,
	})
}

// isDirectedAtBot checks whether a group message is directed at this bot:
//   - Command with @thisbot suffix (e.g. /help@thisbot)
//   - Command without @suffix (broadcast to all bots — accept it)
//   - Command with @otherbot suffix → reject
//   - Non-command: accept if bot is @mentioned or message is a reply to bot
func (p *Platform) isDirectedAtBot(msg *tgbotapi.Message) bool {
	botName := p.bot.Self.UserName

	// Commands: /cmd or /cmd@botname
	if msg.IsCommand() {
		atIdx := strings.Index(msg.Text, "@")
		spaceIdx := strings.Index(msg.Text, " ")
		cmdEnd := len(msg.Text)
		if spaceIdx > 0 {
			cmdEnd = spaceIdx
		}
		if atIdx > 0 && atIdx < cmdEnd {
			target := msg.Text[atIdx+1 : cmdEnd]
			slog.Debug("telegram: command with @suffix", "bot", botName, "target", target, "match", strings.EqualFold(target, botName))
			return strings.EqualFold(target, botName)
		}
		slog.Debug("telegram: command without @suffix, accepting", "bot", botName, "text", msg.Text)
		return true // /cmd without @suffix — accept
	}

	// Non-command: check @mention
	if msg.Entities != nil {
		for _, e := range msg.Entities {
			if e.Type == "mention" {
				mention := extractEntityText(msg.Text, e.Offset, e.Length)
				slog.Debug("telegram: checking mention", "bot", botName, "mention", mention, "match", strings.EqualFold(mention, "@"+botName))
				if strings.EqualFold(mention, "@"+botName) {
					return true
				}
			}
		}
	}

	// Check if replying to a message from this bot
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		slog.Debug("telegram: checking reply", "bot_id", p.bot.Self.ID, "reply_from_id", msg.ReplyToMessage.From.ID)
		if msg.ReplyToMessage.From.ID == p.bot.Self.ID {
			return true
		}
	}

	// Also check caption entities (for photos with captions)
	if msg.CaptionEntities != nil {
		for _, e := range msg.CaptionEntities {
			if e.Type == "mention" {
				mention := extractEntityText(msg.Caption, e.Offset, e.Length)
				if strings.EqualFold(mention, "@"+botName) {
					return true
				}
			}
		}
	}

	slog.Debug("telegram: ignoring group message not directed at bot", "chat", msg.Chat.ID, "bot", botName, "text", msg.Text, "entities", msg.Entities)
	return false
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	html := core.MarkdownToSimpleHTML(content)
	reply := tgbotapi.NewMessage(rc.chatID, html)
	reply.ReplyToMessageID = rc.messageID
	reply.ParseMode = tgbotapi.ModeHTML

	if _, err := p.bot.Send(reply); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			reply.Text = content
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

	html := core.MarkdownToSimpleHTML(content)
	msg := tgbotapi.NewMessage(rc.chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML

	if _, err := p.bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			_, err = p.bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

// SendWithButtons sends a message with an inline keyboard.
func (p *Platform) SendWithButtons(ctx context.Context, rctx any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, row := range buttons {
		var btns []tgbotapi.InlineKeyboardButton
		for _, b := range row {
			btns = append(btns, tgbotapi.NewInlineKeyboardButtonData(b.Text, b.Data))
		}
		rows = append(rows, btns)
	}

	html := core.MarkdownToSimpleHTML(content)
	msg := tgbotapi.NewMessage(rc.chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)

	if _, err := p.bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			_, err = p.bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: sendWithButtons: %w", err)
		}
	}
	return nil
}

// DeletePreviewMessage deletes a stale preview message so the caller can send a fresh one.
func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	h, ok := previewHandle.(*telegramPreviewHandle)
	if !ok {
		return fmt.Errorf("telegram: invalid preview handle type %T", previewHandle)
	}
	del := tgbotapi.NewDeleteMessage(h.chatID, h.messageID)
	_, err := p.bot.Request(del)
	if err != nil {
		slog.Debug("telegram: delete preview message failed", "error", err)
	}
	return err
}

func (p *Platform) downloadFile(fileID string) ([]byte, error) {
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := p.bot.GetFile(fileConfig)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	link := file.Link(p.bot.Token)

	resp, err := p.httpClient.Get(link)
	if err != nil {
		errMsg := core.RedactToken(err.Error(), p.bot.Token)
		return nil, fmt.Errorf("download file %s: %s", fileID, errMsg)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// telegram:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "telegram" {
		return nil, fmt.Errorf("telegram: invalid session key %q", sessionKey)
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram: invalid chat ID in %q", sessionKey)
	}
	return replyContext{chatID: chatID}, nil
}

// telegramPreviewHandle stores the chat and message IDs for an editable preview message.
type telegramPreviewHandle struct {
	chatID    int64
	messageID int
}

// SendPreviewStart sends a new message and returns a handle for subsequent edits.
// Uses HTML mode to match UpdateMessage formatting, falling back to plain text
// if Telegram rejects the HTML (reduces visible format "jump" during streaming).
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	html := core.MarkdownToSimpleHTML(content)
	msg := tgbotapi.NewMessage(rc.chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML

	sent, err := p.bot.Send(msg)
	if err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			sent, err = p.bot.Send(msg)
		}
		if err != nil {
			return nil, fmt.Errorf("telegram: send preview: %w", err)
		}
	}
	return &telegramPreviewHandle{chatID: rc.chatID, messageID: sent.MessageID}, nil
}

// UpdateMessage edits an existing message identified by previewHandle.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*telegramPreviewHandle)
	if !ok {
		return fmt.Errorf("telegram: invalid preview handle type %T", previewHandle)
	}

	html := core.MarkdownToSimpleHTML(content)
	slog.Debug("telegram: UpdateMessage",
		"content_len", len(content), "html_len", len(html),
		"content_prefix", truncateForLog(content, 80),
		"html_prefix", truncateForLog(html, 80))

	edit := tgbotapi.NewEditMessageText(h.chatID, h.messageID, html)
	edit.ParseMode = tgbotapi.ModeHTML

	if _, err := p.bot.Send(edit); err != nil {
		errMsg := err.Error()
		slog.Debug("telegram: UpdateMessage HTML failed", "error", errMsg)
		if strings.Contains(errMsg, "not modified") {
			return nil
		}
		if strings.Contains(errMsg, "can't parse") {
			slog.Debug("telegram: UpdateMessage falling back to plain text", "full_html", html)
			edit.Text = content
			edit.ParseMode = ""
			if _, err2 := p.bot.Send(edit); err2 != nil {
				if strings.Contains(err2.Error(), "not modified") {
					return nil
				}
				return fmt.Errorf("telegram: edit message: %w", err2)
			}
			return nil
		}
		return fmt.Errorf("telegram: edit message: %w", err)
	}
	slog.Debug("telegram: UpdateMessage HTML success")
	return nil
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// StartTyping sends a "typing…" chat action and repeats every 5 seconds
// until the returned stop function is called.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}

	action := tgbotapi.NewChatAction(rc.chatID, tgbotapi.ChatTyping)
	p.bot.Send(action)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.bot.Send(action)
			}
		}
	}()

	return func() { close(done) }
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

// RegisterCommands registers bot commands with Telegram for the command menu.
func (p *Platform) RegisterCommands(commands []core.BotCommandInfo) error {
	if p.bot == nil {
		return fmt.Errorf("telegram: bot not initialized")
	}

	// Telegram limits: max 100 commands, description max 256 chars
	var tgCommands []tgbotapi.BotCommand
	seen := make(map[string]bool)
	for _, c := range commands {
		cmd := sanitizeTelegramCommand(c.Command)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		desc := c.Description
		if len(desc) > 256 {
			desc = desc[:253] + "..."
		}
		tgCommands = append(tgCommands, tgbotapi.BotCommand{
			Command:     cmd,
			Description: desc,
		})
	}

	// Limit to 100 commands
	if len(tgCommands) > 100 {
		tgCommands = tgCommands[:100]
	}

	if len(tgCommands) == 0 {
		slog.Debug("telegram: no commands to register")
		return nil
	}

	cfg := tgbotapi.NewSetMyCommands(tgCommands...)
	_, err := p.bot.Request(cfg)
	if err != nil {
		return fmt.Errorf("telegram: setMyCommands failed: %w", err)
	}

	slog.Info("telegram: registered bot commands", "count", len(tgCommands))
	return nil
}

// extractEntityText extracts a substring from text using Telegram's UTF-16 code unit
// offset and length. Telegram Bot API entity offsets are measured in UTF-16 code units,
// not bytes or Unicode code points, so direct byte slicing produces wrong results
// when the text contains non-ASCII characters (e.g. Chinese, emoji).
func extractEntityText(text string, offsetUTF16, lengthUTF16 int) string {
	encoded := utf16.Encode([]rune(text))
	endUTF16 := offsetUTF16 + lengthUTF16
	if offsetUTF16 < 0 || lengthUTF16 < 0 || endUTF16 > len(encoded) {
		return ""
	}
	return string(utf16.Decode(encoded[offsetUTF16:endUTF16]))
}

// isValidTelegramCommand validates if a command string meets Telegram's requirements.
// Telegram command rules:
//   - 1-32 characters long
//   - Only lowercase letters, digits, and underscores
//   - Must start with a letter
func isValidTelegramCommand(cmd string) bool {
	if len(cmd) == 0 || len(cmd) > 32 {
		return false
	}
	if cmd[0] < 'a' || cmd[0] > 'z' {
		return false
	}
	for i := 1; i < len(cmd); i++ {
		c := cmd[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// sanitizeTelegramCommand converts a command name to Telegram-compatible format.
// Telegram rules: 1-32 chars, lowercase letters/digits/underscores, must start with a letter.
// Returns "" if the command cannot be sanitized (e.g. empty or no letter to start with).
func sanitizeTelegramCommand(cmd string) string {
	cmd = strings.ToLower(cmd)
	var b strings.Builder
	for _, c := range cmd {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	result := b.String()
	// Collapse consecutive underscores
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	result = strings.Trim(result, "_")
	// Must start with a letter
	if len(result) == 0 || result[0] < 'a' || result[0] > 'z' {
		return ""
	}
	if len(result) > 32 {
		result = result[:32]
	}
	return result
}
