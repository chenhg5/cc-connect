package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/chenhg5/cc-connect/core"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var telegramConvertAudioToOpus = core.ConvertAudioToOpus

func init() {
	core.RegisterPlatform("telegram", New)
}

type replyContext struct {
	chatID    int64
	messageID int
}

type telegramBot interface {
	SelfUser() tgbotapi.User
	Token() string
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetUpdates(config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error)
	GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
	GetFile(config tgbotapi.FileConfig) (tgbotapi.File, error)
}

type backoffTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type typingTicker interface {
	C() <-chan time.Time
	Stop()
}

type retryCause int

const (
	retryCauseInitialConnectFailure retryCause = iota
	retryCauseReconnectFailure
	retryCauseConnectionLost
)

type retryLoopError struct {
	cause retryCause
	err   error
}

func (e *retryLoopError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *retryLoopError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type botAPIWrapper struct {
	*tgbotapi.BotAPI
}

func (b *botAPIWrapper) SelfUser() tgbotapi.User { return b.BotAPI.Self }
func (b *botAPIWrapper) Token() string           { return b.BotAPI.Token }

type stdlibBackoffTimer struct {
	*time.Timer
}

func (t *stdlibBackoffTimer) C() <-chan time.Time { return t.Timer.C }

type stdlibTypingTicker struct {
	*time.Ticker
}

func (t *stdlibTypingTicker) C() <-chan time.Time { return t.Ticker.C }

type Platform struct {
	token                 string
	allowFrom             string
	groupReplyAll         bool
	shareSessionInChannel bool
	httpClient            *http.Client

	mu                  sync.RWMutex
	bot                 telegramBot
	handler             core.MessageHandler
	lifecycleHandler    core.PlatformLifecycleHandler
	cancel              context.CancelFunc
	stopping            bool
	generation          uint64
	unavailableNotified bool
	everConnected       bool
	newBot              func(string, *http.Client) (telegramBot, error)
	newBackoffTimer     func(time.Duration) backoffTimer
	newTypingTicker     func(time.Duration) typingTicker
}

const (
	initialReconnectBackoff = time.Second
	maxReconnectBackoff     = 30 * time.Second
	stableConnectionWindow  = 10 * time.Second
)

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
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return fmt.Errorf("telegram: platform stopped")
	}
	if p.newBot == nil {
		p.newBot = func(token string, client *http.Client) (telegramBot, error) {
			bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, client)
			if err != nil {
				return nil, err
			}
			return &botAPIWrapper{BotAPI: bot}, nil
		}
	}
	if p.newBackoffTimer == nil {
		p.newBackoffTimer = func(d time.Duration) backoffTimer {
			return &stdlibBackoffTimer{Timer: time.NewTimer(d)}
		}
	}
	if p.newTypingTicker == nil {
		p.newTypingTicker = func(d time.Duration) typingTicker {
			return &stdlibTypingTicker{Ticker: time.NewTicker(d)}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.handler = handler
	p.cancel = cancel
	p.bot = nil

	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialReconnectBackoff

	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		startedAt := time.Now()
		err := p.runConnection(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}

		wait := backoff
		if time.Since(startedAt) >= stableConnectionWindow {
			wait = initialReconnectBackoff
			backoff = initialReconnectBackoff
		} else if backoff < maxReconnectBackoff {
			backoff *= 2
			if backoff > maxReconnectBackoff {
				backoff = maxReconnectBackoff
			}
		}

		if err != nil {
			cause := retryCauseReconnectFailure
			if retryErr, ok := err.(*retryLoopError); ok {
				cause = retryErr.cause
			}
			slog.Warn(retryLogMessage(cause), "error", err, "backoff", wait)
			if cause == retryCauseInitialConnectFailure || cause == retryCauseReconnectFailure {
				p.notifyUnavailable(err)
			}
		}

		timer := p.makeBackoffTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}

func (p *Platform) runConnection(ctx context.Context) error {
	bot, err := p.makeBot()
	if err != nil {
		cause := retryCauseInitialConnectFailure
		if p.hasEverConnected() {
			cause = retryCauseReconnectFailure
		}
		return &retryLoopError{
			cause: cause,
			err:   fmt.Errorf("telegram: connect failed: %w", err),
		}
	}
	if ctx.Err() != nil || p.isStopping() {
		bot.StopReceivingUpdates()
		return nil
	}

	gen, ok := p.publishConnectedBot(bot)
	if !ok {
		bot.StopReceivingUpdates()
		return nil
	}

	self := bot.SelfUser()
	slog.Info("telegram: connected", "bot", self.UserName)

	drain := tgbotapi.NewUpdate(-1)
	drain.Timeout = 0
	if _, err := bot.GetUpdates(drain); err != nil {
		slog.Warn("telegram: failed to drain old updates", "error", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	p.emitReady(gen)

	for {
		select {
		case <-ctx.Done():
			p.finishConnection(gen, bot, ctx.Err(), true)
			return nil
		case update, ok := <-updates:
			if !ok {
				err := fmt.Errorf("telegram: updates channel closed")
				p.finishConnection(gen, bot, err, false)
				return &retryLoopError{cause: retryCauseConnectionLost, err: err}
			}
			p.handleUpdate(bot, update)
		}
	}
}

func (p *Platform) handleUpdate(bot telegramBot, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		p.handleCallbackQuery(bot, update.CallbackQuery)
		return
	}

	if update.Message == nil {
		return
	}

	msg := update.Message
	msgTime := time.Unix(int64(msg.Date), 0)
	if core.IsOldMessage(msgTime) {
		slog.Debug("telegram: ignoring old message after restart", "date", msgTime)
		return
	}
	if msg.From == nil {
		return
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
		return
	}

	isGroup := msg.Chat.Type == "group" || msg.Chat.Type == "supergroup"
	chatName := ""
	if isGroup {
		chatName = msg.Chat.Title
	}

	if isGroup && !p.groupReplyAll {
		slog.Debug("telegram: checking group message", "bot", bot.SelfUser().UserName, "text", msg.Text, "is_command", msg.IsCommand())
		if !p.isDirectedAtBot(bot, msg) {
			return
		}
	}

	rctx := replyContext{chatID: msg.Chat.ID, messageID: msg.MessageID}
	botName := bot.SelfUser().UserName

	if len(msg.Photo) > 0 {
		best := msg.Photo[len(msg.Photo)-1]
		imgData, err := p.downloadFile(bot, best.FileID)
		if err != nil {
			slog.Error("telegram: download photo failed", "error", err)
			return
		}
		caption := stripBotMention(msg.Caption, botName)
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			Content:   caption,
			MessageID: strconv.Itoa(msg.MessageID),
			Images:    []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
			ReplyCtx:  rctx,
		})
		return
	}

	if msg.Voice != nil {
		slog.Debug("telegram: voice received", "user", userName, "duration", msg.Voice.Duration)
		audioData, err := p.downloadFile(bot, msg.Voice.FileID)
		if err != nil {
			slog.Error("telegram: download voice failed", "error", err)
			return
		}
		p.dispatchMessage(&core.Message{
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
		})
		return
	}

	if msg.Audio != nil {
		slog.Debug("telegram: audio file received", "user", userName)
		audioData, err := p.downloadFile(bot, msg.Audio.FileID)
		if err != nil {
			slog.Error("telegram: download audio failed", "error", err)
			return
		}
		format := "mp3"
		if msg.Audio.MimeType != "" {
			parts := strings.SplitN(msg.Audio.MimeType, "/", 2)
			if len(parts) == 2 {
				format = parts[1]
			}
		}
		p.dispatchMessage(&core.Message{
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
		})
		return
	}

	if msg.Document != nil {
		slog.Info("telegram: document received", "user", userName, "file_name", msg.Document.FileName, "mime", msg.Document.MimeType, "file_id", msg.Document.FileID)
		fileData, err := p.downloadFile(bot, msg.Document.FileID)
		if err != nil {
			slog.Error("telegram: download document failed", "error", err)
			return
		}
		caption := stripBotMention(msg.Caption, botName)
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			Content:   caption,
			MessageID: strconv.Itoa(msg.MessageID),
			Files:     []core.FileAttachment{{MimeType: msg.Document.MimeType, Data: fileData, FileName: msg.Document.FileName}},
			ReplyCtx:  rctx,
		})
		return
	}

	if msg.Text == "" {
		return
	}

	text := stripBotMention(msg.Text, botName)
	slog.Debug("telegram: message received", "user", userName, "chat", msg.Chat.ID)
	p.dispatchMessage(&core.Message{
		SessionKey: sessionKey, Platform: "telegram",
		UserID: userID, UserName: userName, ChatName: chatName,
		Content:   text,
		MessageID: strconv.Itoa(msg.MessageID),
		ReplyCtx:  rctx,
	})
}

func (p *Platform) dispatchMessage(msg *core.Message) {
	handler := p.messageHandler()
	if handler == nil {
		return
	}
	handler(p, msg)
}

func (p *Platform) messageHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func stripBotMention(text, botName string) string {
	if botName == "" {
		return text
	}
	text = strings.ReplaceAll(text, "@"+botName, "")
	return strings.TrimSpace(text)
}

func (p *Platform) makeBot() (telegramBot, error) {
	p.mu.RLock()
	newBot := p.newBot
	p.mu.RUnlock()
	return newBot(p.token, p.httpClient)
}

func (p *Platform) makeBackoffTimer(d time.Duration) backoffTimer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.newBackoffTimer(d)
}

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

func (p *Platform) publishConnectedBot(bot telegramBot) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return 0, false
	}
	p.generation++
	p.bot = bot
	return p.generation, true
}

func (p *Platform) emitReady(gen uint64) {
	p.mu.RLock()
	if p.stopping || p.generation != gen || p.bot == nil {
		p.mu.RUnlock()
		return
	}
	handler := p.lifecycleHandler
	p.mu.RUnlock()
	p.markReady()

	if handler != nil {
		handler.OnPlatformReady(p)
	}
}

func (p *Platform) finishConnection(gen uint64, bot telegramBot, err error, dueToStop bool) {
	notify := false

	p.mu.Lock()
	if p.bot == bot && p.generation == gen {
		p.bot = nil
		notify = !p.stopping && !dueToStop && err != nil
	}
	p.mu.Unlock()

	if notify {
		p.notifyUnavailable(err)
	}
}

func (p *Platform) currentBot() (telegramBot, uint64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.bot == nil {
		return nil, 0, fmt.Errorf("telegram: bot not connected")
	}
	return p.bot, p.generation, nil
}

func (p *Platform) currentBotFor(action string, candidate telegramBot) (telegramBot, uint64, error) {
	bot, gen, err := p.currentBot()
	if err != nil {
		return nil, 0, fmt.Errorf("telegram: %s: bot not connected", action)
	}
	if candidate != nil && bot != candidate {
		return nil, 0, fmt.Errorf("telegram: %s: bot not connected", action)
	}
	return bot, gen, nil
}

func (p *Platform) connectedBot(action string) (telegramBot, error) {
	bot, _, err := p.currentBot()
	if err != nil {
		return nil, fmt.Errorf("telegram: %s: bot not connected", action)
	}
	return bot, nil
}

func (p *Platform) hasEverConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.everConnected
}

func (p *Platform) markReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.everConnected = true
	p.unavailableNotified = false
}

func (p *Platform) notifyUnavailable(err error) {
	var handler core.PlatformLifecycleHandler

	p.mu.Lock()
	if p.stopping || err == nil || p.unavailableNotified {
		p.mu.Unlock()
		return
	}
	p.unavailableNotified = true
	handler = p.lifecycleHandler
	p.mu.Unlock()

	if handler != nil {
		handler.OnPlatformUnavailable(p, err)
	}
}

func retryLogMessage(cause retryCause) string {
	switch cause {
	case retryCauseInitialConnectFailure:
		return "telegram: initial connection failed, retrying"
	case retryCauseConnectionLost:
		return "telegram: connection lost, retrying"
	default:
		return "telegram: reconnect failed, retrying"
	}
}

func (p *Platform) handleCallbackQuery(bot telegramBot, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.From == nil {
		return
	}
	currentBot, _, err := p.currentBotFor("callback query", bot)
	if err != nil {
		slog.Debug("telegram: ignoring callback for stale bot", "error", err)
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
	if _, err := currentBot.Request(answer); err != nil {
		slog.Debug("telegram: answer callback failed", "error", err)
	}

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
		if _, err := currentBot.Send(edit); err != nil {
			slog.Debug("telegram: callback edit failed", "error", err)
		}

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
		if _, err := currentBot.Send(edit); err != nil {
			slog.Debug("telegram: callback edit failed", "error", err)
		}

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
	if _, err := currentBot.Send(edit); err != nil {
		slog.Debug("telegram: permission callback edit failed", "error", err)
	}

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
func (p *Platform) isDirectedAtBot(bot telegramBot, msg *tgbotapi.Message) bool {
	currentBot, _, err := p.currentBotFor("bot identity", bot)
	if err != nil {
		slog.Debug("telegram: ignoring group routing for stale bot", "error", err)
		return false
	}
	self := currentBot.SelfUser()
	botName := self.UserName

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
		slog.Debug("telegram: checking reply", "bot_id", self.ID, "reply_from_id", msg.ReplyToMessage.From.ID)
		if msg.ReplyToMessage.From.ID == self.ID {
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
	bot, err := p.connectedBot("reply")
	if err != nil {
		return err
	}

	html := core.MarkdownToSimpleHTML(content)
	reply := tgbotapi.NewMessage(rc.chatID, html)
	reply.ReplyToMessageID = rc.messageID
	reply.ParseMode = tgbotapi.ModeHTML

	if _, err := bot.Send(reply); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			reply.Text = content
			reply.ParseMode = ""
			_, err = bot.Send(reply)
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
	bot, err := p.connectedBot("send")
	if err != nil {
		return err
	}

	html := core.MarkdownToSimpleHTML(content)
	msg := tgbotapi.NewMessage(rc.chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML

	if _, err := bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			_, err = bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}
	bot, err := p.connectedBot("send image")
	if err != nil {
		return err
	}

	name := img.FileName
	if name == "" {
		name = "image"
	}
	slog.Debug("telegram: sending image", "chat_id", rc.chatID, "name", name, "size", len(img.Data))
	msg := tgbotapi.NewPhoto(rc.chatID, tgbotapi.FileBytes{Name: name, Bytes: img.Data})
	if _, err := bot.Send(msg); err != nil {
		return fmt.Errorf("telegram: send image: %w", err)
	}
	return nil
}

func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}
	bot, err := p.connectedBot("send file")
	if err != nil {
		return err
	}

	name := file.FileName
	if name == "" {
		name = "attachment"
	}
	msg := tgbotapi.NewDocument(rc.chatID, tgbotapi.FileBytes{Name: name, Bytes: file.Data})
	if _, err := bot.Send(msg); err != nil {
		return fmt.Errorf("telegram: send file: %w", err)
	}
	return nil
}

// SendAudio sends synthesized audio back to Telegram.
// It prefers voice messages and falls back to audio files for mp3/m4a on sendVoice failure.
func (p *Platform) SendAudio(ctx context.Context, rctx any, audio []byte, format string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: SendAudio: invalid reply context type %T", rctx)
	}

	sendData := audio
	sendFormat := strings.ToLower(strings.TrimSpace(format))
	if sendFormat == "" {
		sendFormat = "ogg"
	}

	switch sendFormat {
	case "ogg", "opus", "mp3", "m4a":
		// Attempt these formats directly with sendVoice first.
	default:
		converted, err := telegramConvertAudioToOpus(ctx, audio, sendFormat)
		if err != nil {
			return fmt.Errorf("telegram: SendAudio: convert %s to opus: %w", sendFormat, err)
		}
		sendData = converted
		sendFormat = "opus"
	}

	if err := p.sendVoice(rc.chatID, sendData, sendFormat); err != nil {
		if sendFormat == "mp3" || sendFormat == "m4a" {
			if fallbackErr := p.sendAudio(rc.chatID, sendData, sendFormat); fallbackErr == nil {
				return nil
			} else {
				return fmt.Errorf(
					"telegram: SendAudio: %w",
					errors.Join(
						fmt.Errorf("sendVoice failed: %w", err),
						fmt.Errorf("sendAudio fallback failed: %w", fallbackErr),
					),
				)
			}
		}
		return fmt.Errorf("telegram: SendAudio: sendVoice: %w", err)
	}
	return nil
}

func (p *Platform) sendVoice(chatID int64, audio []byte, format string) error {
	msg := tgbotapi.NewVoice(chatID, tgbotapi.FileBytes{
		Name:  "tts_audio." + telegramAudioFileExt(format),
		Bytes: audio,
	})
	if _, err := p.bot.Send(msg); err != nil {
		return err
	}
	return nil
}

func (p *Platform) sendAudio(chatID int64, audio []byte, format string) error {
	msg := tgbotapi.NewAudio(chatID, tgbotapi.FileBytes{
		Name:  "tts_audio." + telegramAudioFileExt(format),
		Bytes: audio,
	})
	if _, err := p.bot.Send(msg); err != nil {
		return err
	}
	return nil
}

func telegramAudioFileExt(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "oga":
		return "ogg"
	case "":
		return "bin"
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

// SendWithButtons sends a message with an inline keyboard.
func (p *Platform) SendWithButtons(ctx context.Context, rctx any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}
	bot, err := p.connectedBot("send with buttons")
	if err != nil {
		return err
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

	if _, err := bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			_, err = bot.Send(msg)
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
	bot, err := p.connectedBot("delete preview")
	if err != nil {
		return err
	}
	del := tgbotapi.NewDeleteMessage(h.chatID, h.messageID)
	_, err = bot.Request(del)
	if err != nil {
		slog.Debug("telegram: delete preview message failed", "error", err)
	}
	return err
}

func (p *Platform) downloadFile(bot telegramBot, fileID string) ([]byte, error) {
	bot, _, err := p.currentBotFor("download file", bot)
	if err != nil {
		return nil, err
	}
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := bot.GetFile(fileConfig)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	link := file.Link(bot.Token())

	resp, err := p.httpClient.Get(link)
	if err != nil {
		errMsg := core.RedactToken(err.Error(), bot.Token())
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
	bot, err := p.connectedBot("send preview")
	if err != nil {
		return nil, err
	}

	html := core.MarkdownToSimpleHTML(content)
	msg := tgbotapi.NewMessage(rc.chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML

	sent, err := bot.Send(msg)
	if err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.Text = content
			msg.ParseMode = ""
			sent, err = bot.Send(msg)
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
	bot, err := p.connectedBot("update message")
	if err != nil {
		return err
	}

	html := core.MarkdownToSimpleHTML(content)
	slog.Debug("telegram: UpdateMessage",
		"content_len", len(content), "html_len", len(html),
		"content_prefix", truncateForLog(content, 80),
		"html_prefix", truncateForLog(html, 80))

	edit := tgbotapi.NewEditMessageText(h.chatID, h.messageID, html)
	edit.ParseMode = tgbotapi.ModeHTML

	if _, err := bot.Send(edit); err != nil {
		errMsg := err.Error()
		slog.Debug("telegram: UpdateMessage HTML failed", "error", errMsg)
		if strings.Contains(errMsg, "not modified") {
			return nil
		}
		if strings.Contains(errMsg, "can't parse") {
			slog.Debug("telegram: UpdateMessage falling back to plain text", "full_html", html)
			edit.Text = content
			edit.ParseMode = ""
			if _, err2 := bot.Send(edit); err2 != nil {
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

// StartTyping sends a "typing…" chat action and repeats every 5 seconds
// until the returned stop function is called.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}

	action := tgbotapi.NewChatAction(rc.chatID, tgbotapi.ChatTyping)
	if bot, err := p.connectedBot("typing"); err == nil {
		if _, err := bot.Send(action); err != nil {
			slog.Debug("telegram: initial typing send failed", "error", err)
		}
	} else {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		ticker := p.newTypingTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C():
				bot, err := p.connectedBot("typing")
				if err != nil {
					slog.Debug("telegram: typing stopped", "error", err)
					return
				}
				if _, err := bot.Send(action); err != nil {
					slog.Debug("telegram: typing send failed", "error", err)
				}
			}
		}
	}()

	return func() { close(done) }
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	cancel := p.cancel
	bot := p.bot
	p.cancel = nil
	p.bot = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if bot != nil {
		bot.StopReceivingUpdates()
	}
	return nil
}

// RegisterCommands registers bot commands with Telegram for the command menu.
func (p *Platform) RegisterCommands(commands []core.BotCommandInfo) error {
	bot, err := p.connectedBot("register commands")
	if err != nil {
		return err
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
	_, err = bot.Request(cfg)
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

var _ core.AudioSender = (*Platform)(nil)
