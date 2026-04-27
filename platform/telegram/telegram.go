package telegram

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var telegramConvertAudioToOpus = core.ConvertAudioToOpus

func init() {
	core.RegisterPlatform("telegram", New)
}

type replyContext struct {
	chatID    int64
	threadID  int
	messageID int
}

// telegramBot abstracts the Telegram bot API methods for testability.
// *tgbot.Bot satisfies this interface.
type telegramBot interface {
	SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error)
	SendPhoto(ctx context.Context, params *tgbot.SendPhotoParams) (*models.Message, error)
	SendDocument(ctx context.Context, params *tgbot.SendDocumentParams) (*models.Message, error)
	SendVoice(ctx context.Context, params *tgbot.SendVoiceParams) (*models.Message, error)
	SendAudio(ctx context.Context, params *tgbot.SendAudioParams) (*models.Message, error)
	SendChatAction(ctx context.Context, params *tgbot.SendChatActionParams) (bool, error)
	EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error)
	DeleteMessage(ctx context.Context, params *tgbot.DeleteMessageParams) (bool, error)
	AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error)
	SetMyCommands(ctx context.Context, params *tgbot.SetMyCommandsParams) (bool, error)
	GetFile(ctx context.Context, params *tgbot.GetFileParams) (*models.File, error)
	FileDownloadLink(f *models.File) string
	SetMessageReaction(ctx context.Context, params *tgbot.SetMessageReactionParams) (bool, error)
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

type stdlibBackoffTimer struct {
	*time.Timer
}

func (t *stdlibBackoffTimer) C() <-chan time.Time { return t.Timer.C }

type stdlibTypingTicker struct {
	*time.Ticker
}

func (t *stdlibTypingTicker) C() <-chan time.Time { return t.Ticker.C }

// botFactory creates a bot, returns it plus self user info and a blocking poll function.
type botFactory func(token string, onUpdate func(context.Context, *models.Update), httpClient *http.Client) (telegramBot, *models.User, func(context.Context), error)

type Platform struct {
	token                 string
	allowFrom             string
	groupReplyAll         bool
	shareSessionInChannel bool
	enableReactions       bool
	httpClient            *http.Client

	mu                  sync.RWMutex
	bot                 telegramBot
	selfUser            *models.User
	handler             core.MessageHandler
	lifecycleHandler    core.PlatformLifecycleHandler
	cancel              context.CancelFunc
	stopping            bool
	generation          uint64
	unavailableNotified bool
	everConnected       bool
	newBot              botFactory
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

	// Build HTTP client with optional proxy support.
	// Timeout must exceed the server-side long-poll duration (pollTimeout − 1s = 59s)
	// to avoid the HTTP client racing with Telegram's response. 90s gives 30s headroom.
	httpClient := &http.Client{Timeout: 90 * time.Second}
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
	enableReactions, _ := opts["enable_reactions"].(bool)
	return &Platform{token: token, allowFrom: allowFrom, groupReplyAll: groupReplyAll, shareSessionInChannel: shareSessionInChannel, enableReactions: enableReactions, httpClient: httpClient}, nil
}

func (p *Platform) Name() string { return "telegram" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return fmt.Errorf("telegram: platform stopped")
	}
	if p.newBot == nil {
		p.newBot = defaultNewBot
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
	p.selfUser = nil

	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func defaultNewBot(token string, onUpdate func(context.Context, *models.Update), httpClient *http.Client) (telegramBot, *models.User, func(context.Context), error) {
	handler := func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		onUpdate(ctx, update)
	}
	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(handler),
		tgbot.WithNotAsyncHandlers(),
	}
	if httpClient != nil {
		opts = append(opts, tgbot.WithHTTPClient(60*time.Second, httpClient))
	}
	b, err := tgbot.New(token, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getMe: %w", err)
	}
	return b, me, b.Start, nil
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
	factory := p.getNewBot()
	b, me, startPoll, err := factory(p.token, p.processUpdate, p.httpClient)
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
		return nil
	}

	gen, ok := p.publishBot(b, me)
	if !ok {
		return nil
	}

	slog.Info("telegram: connected", "bot", me.Username)
	p.emitReady(gen)

	// Start polling — blocks until ctx is cancelled or connection drops.
	startPoll(ctx)

	p.clearBot(gen, b)
	return nil
}

func (p *Platform) processUpdate(ctx context.Context, update *models.Update) {
	if update.CallbackQuery != nil {
		p.handleCallbackQuery(ctx, update.CallbackQuery)
		return
	}

	if update.Message == nil {
		return
	}
	p.handleMessage(ctx, update.Message)
}

func (p *Platform) handleMessage(ctx context.Context, msg *models.Message) {
	msgTime := time.Unix(int64(msg.Date), 0)
	if core.IsOldMessage(msgTime) {
		slog.Debug("telegram: ignoring old message after restart", "date", msgTime)
		return
	}
	if msg.From == nil {
		return
	}

	userName := msg.From.Username
	if userName == "" {
		userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	}

	threadID := 0
	if msg.Chat.IsForum {
		threadID = msg.MessageThreadID
	}
	sessionKey := p.buildSessionKey(msg.Chat.ID, threadID, msg.From.ID)
	channelKey := buildChannelKey(msg.Chat.ID, threadID)

	userID := strconv.FormatInt(msg.From.ID, 10)
	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("telegram: message from unauthorized user", "user", userID)
		return
	}

	isGroup := msg.Chat.Type == models.ChatTypeGroup || msg.Chat.Type == models.ChatTypeSupergroup
	chatName := ""
	if isGroup {
		chatName = msg.Chat.Title
	}

	if isGroup && !p.groupReplyAll {
		slog.Debug("telegram: checking group message", "text", msg.Text, "is_command", isCommand(msg))
		if !p.isDirectedAtBot(msg) {
			return
		}
	}

	rctx := replyContext{chatID: msg.Chat.ID, threadID: threadID, messageID: msg.ID}
	if p.enableReactions {
		go p.reactToMessage(ctx, msg.Chat.ID, msg.ID, "⚡")
	}
	botName := p.botUsername()

	if len(msg.Photo) > 0 {
		best := msg.Photo[len(msg.Photo)-1]
		imgData, err := p.downloadFile(best.FileID)
		if err != nil {
			slog.Error("telegram: download photo failed", "error", err)
			return
		}
		caption := stripBotMention(msg.Caption, botName)
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			Content:    caption,
			MessageID:  strconv.Itoa(msg.ID),
			ChannelKey: channelKey,
			Images:     []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
			ReplyCtx:   rctx,
		}, msg)
		return
	}

	if msg.Voice != nil {
		slog.Debug("telegram: voice received", "user", userName, "duration", msg.Voice.Duration)
		audioData, err := p.downloadFile(msg.Voice.FileID)
		if err != nil {
			slog.Error("telegram: download voice failed", "error", err)
			return
		}
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			MessageID:  strconv.Itoa(msg.ID),
			ChannelKey: channelKey,
			Audio: &core.AudioAttachment{
				MimeType: msg.Voice.MimeType,
				Data:     audioData,
				Format:   "ogg",
				Duration: msg.Voice.Duration,
			},
			ReplyCtx: rctx,
		}, msg)
		return
	}

	if msg.Audio != nil {
		slog.Debug("telegram: audio file received", "user", userName)
		audioData, err := p.downloadFile(msg.Audio.FileID)
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
			MessageID:  strconv.Itoa(msg.ID),
			ChannelKey: channelKey,
			Audio: &core.AudioAttachment{
				MimeType: msg.Audio.MimeType,
				Data:     audioData,
				Format:   format,
				Duration: msg.Audio.Duration,
			},
			ReplyCtx: rctx,
		}, msg)
		return
	}

	if msg.Document != nil {
		slog.Info("telegram: document received", "user", userName, "file_name", msg.Document.FileName, "mime", msg.Document.MimeType, "file_id", msg.Document.FileID)
		fileData, err := p.downloadFile(msg.Document.FileID)
		if err != nil {
			slog.Error("telegram: download document failed", "error", err)
			return
		}
		caption := stripBotMention(msg.Caption, botName)
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			Content:    caption,
			MessageID:  strconv.Itoa(msg.ID),
			ChannelKey: channelKey,
			Files:      []core.FileAttachment{{MimeType: msg.Document.MimeType, Data: fileData, FileName: msg.Document.FileName}},
			ReplyCtx:   rctx,
		}, msg)
		return
	}

	if msg.Location != nil {
		slog.Info("telegram: location received", "user", userName, "latitude", msg.Location.Latitude, "longitude", msg.Location.Longitude)
		p.dispatchMessage(&core.Message{
			SessionKey: sessionKey, Platform: "telegram",
			UserID: userID, UserName: userName, ChatName: chatName,
			MessageID:  strconv.Itoa(msg.ID),
			ChannelKey: channelKey,
			Location: &core.LocationAttachment{
				Latitude:             msg.Location.Latitude,
				Longitude:            msg.Location.Longitude,
				HorizontalAccuracy:   msg.Location.HorizontalAccuracy,
				LivePeriod:           msg.Location.LivePeriod,
				Heading:              msg.Location.Heading,
				ProximityAlertRadius: msg.Location.ProximityAlertRadius,
			},
			ReplyCtx: rctx,
		}, msg)
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
		Content:    text,
		MessageID:  strconv.Itoa(msg.ID),
		ChannelKey: channelKey,
		ReplyCtx:   rctx,
	}, msg)
}

func (p *Platform) dispatchMessage(msg *core.Message, tgMsg *models.Message) {
	// Enrich with platform-specific context (reply quotes, location text, etc.)
	var extras []string
	if replyText := enrichReplyContent(tgMsg); replyText != "" {
		extras = append(extras, replyText)
	}
	if locText := enrichLocation(msg); locText != "" {
		extras = append(extras, locText)
	}
	if len(extras) > 0 {
		msg.ExtraContent = strings.Join(extras, "\n")
	}

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

// reactToMessage sets an emoji reaction on a Telegram message.
// It is called asynchronously so it never blocks the message dispatch path.
func (p *Platform) reactToMessage(ctx context.Context, chatID int64, messageID int, emoji string) {
	bot, err := p.connectedBot("react")
	if err != nil {
		return
	}
	if _, err := bot.SetMessageReaction(ctx, &tgbot.SetMessageReactionParams{
		ChatID:    chatID,
		MessageID: messageID,
		Reaction: []models.ReactionType{{
			Type:              models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji},
		}},
	}); err != nil {
		slog.Debug("telegram: set reaction failed", "error", err)
	}
}

func (p *Platform) buildSessionKey(chatID int64, threadID int, userID int64) string {
	if p.shareSessionInChannel {
		if threadID != 0 {
			return fmt.Sprintf("telegram:%d:%d", chatID, threadID)
		}
		return fmt.Sprintf("telegram:%d", chatID)
	}
	if threadID != 0 {
		return fmt.Sprintf("telegram:%d:%d:%d", chatID, threadID, userID)
	}
	return fmt.Sprintf("telegram:%d:%d", chatID, userID)
}

func buildChannelKey(chatID int64, threadID int) string {
	if threadID != 0 {
		return fmt.Sprintf("%d:%d", chatID, threadID)
	}
	return strconv.FormatInt(chatID, 10)
}

func stripBotMention(text, botName string) string {
	if botName == "" {
		return text
	}
	text = strings.ReplaceAll(text, "@"+botName, "")
	return strings.TrimSpace(text)
}

func (p *Platform) getNewBot() botFactory {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.newBot
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

func (p *Platform) publishBot(b telegramBot, me *models.User) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopping {
		return 0, false
	}
	p.generation++
	p.bot = b
	p.selfUser = me
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

func (p *Platform) clearBot(gen uint64, b telegramBot) {
	notify := false
	p.mu.Lock()
	if p.bot == b && p.generation == gen {
		p.bot = nil
		p.selfUser = nil
		notify = !p.stopping
	}
	p.mu.Unlock()

	if notify {
		p.notifyUnavailable(fmt.Errorf("telegram: connection lost"))
	}
}

func (p *Platform) connectedBot(action string) (telegramBot, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.bot == nil {
		return nil, fmt.Errorf("telegram: %s: bot not connected", action)
	}
	return p.bot, nil
}

func (p *Platform) botUsername() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.selfUser == nil {
		return ""
	}
	return p.selfUser.Username
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

func (p *Platform) handleCallbackQuery(ctx context.Context, cb *models.CallbackQuery) {
	msg := cb.Message.Message
	if msg == nil {
		return
	}

	bot, err := p.connectedBot("callback query")
	if err != nil {
		slog.Debug("telegram: ignoring callback for disconnected bot", "error", err)
		return
	}

	data := cb.Data
	chatID := msg.Chat.ID
	msgID := msg.ID
	userID := strconv.FormatInt(cb.From.ID, 10)

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("telegram: callback from unauthorized user", "user", userID)
		return
	}

	// Answer the callback to clear the loading indicator
	if _, err := bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cb.ID}); err != nil {
		slog.Debug("telegram: answer callback failed", "error", err)
	}

	userName := cb.From.Username
	if userName == "" {
		userName = strings.TrimSpace(cb.From.FirstName + " " + cb.From.LastName)
	}

	threadID := 0
	if msg.Chat.IsForum {
		threadID = msg.MessageThreadID
	}
	sessionKey := p.buildSessionKey(chatID, threadID, cb.From.ID)
	channelKey := buildChannelKey(chatID, threadID)

	isGroup := msg.Chat.Type == models.ChatTypeGroup || msg.Chat.Type == models.ChatTypeSupergroup
	chatName := ""
	if isGroup {
		chatName = msg.Chat.Title
	}
	rctx := replyContext{chatID: chatID, threadID: threadID, messageID: msgID}

	emptyMarkup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}

	// Command callbacks (cmd:/lang en, cmd:/mode yolo, etc.)
	if strings.HasPrefix(data, "cmd:") {
		command := strings.TrimPrefix(data, "cmd:")

		origText := msg.Text
		if origText == "" {
			origText = ""
		}
		if _, err := bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        origText + "\n\n> " + command,
			ReplyMarkup: emptyMarkup,
		}); err != nil {
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
			ChannelKey: channelKey,
			ReplyCtx:   rctx,
		})
		return
	}

	// AskUserQuestion callbacks (askq:qIdx:optIdx)
	if strings.HasPrefix(data, "askq:") {
		parts := strings.SplitN(data, ":", 3)
		choiceLabel := data
		if len(parts) == 3 {
			if msg.ReplyMarkup != nil {
				for _, row := range msg.ReplyMarkup.InlineKeyboard {
					for _, btn := range row {
						if btn.CallbackData == data {
							choiceLabel = "✅ " + btn.Text
						}
					}
				}
			}
		}

		origText := msg.Text
		if origText == "" {
			origText = "(question)"
		}
		if _, err := bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        origText + "\n\n" + choiceLabel,
			ReplyMarkup: emptyMarkup,
		}); err != nil {
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
			ChannelKey: channelKey,
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

	origText := msg.Text
	if origText == "" {
		origText = "(permission request)"
	}
	if _, err := bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   msgID,
		Text:        origText + "\n\n" + choiceLabel,
		ReplyMarkup: emptyMarkup,
	}); err != nil {
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
		ChannelKey: channelKey,
		ReplyCtx:   rctx,
	})
}

// isDirectedAtBot checks whether a group message is directed at this bot:
//   - Command with @thisbot suffix (e.g. /help@thisbot)
//   - Command without @suffix (broadcast to all bots — accept it)
//   - Command with @otherbot suffix → reject
//   - Non-command: accept if bot is @mentioned or message is a reply to bot
func (p *Platform) isDirectedAtBot(msg *models.Message) bool {
	p.mu.RLock()
	self := p.selfUser
	p.mu.RUnlock()
	if self == nil {
		slog.Debug("telegram: ignoring group routing, self user unknown")
		return false
	}
	botName := self.Username

	// Commands: /cmd or /cmd@botname
	if isCommand(msg) {
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
			if e.Type == models.MessageEntityTypeMention {
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
			if e.Type == models.MessageEntityTypeMention {
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

func isCommand(msg *models.Message) bool {
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeBotCommand && e.Offset == 0 {
			return true
		}
	}
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
	params := &tgbot.SendMessageParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Text:            html,
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: rc.messageID},
	}

	if _, err := bot.SendMessage(ctx, params); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			slog.Warn("telegram: HTML rejected by Telegram, sending as plain text",
				"method", "Reply",
				"error", err.Error(),
				"html_prefix", truncateForLog(html, 200),
				"html_len", len(html),
			)
			params.Text = content
			params.ParseMode = ""
			_, err = bot.SendMessage(ctx, params)
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
	params := &tgbot.SendMessageParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Text:            html,
		ParseMode:       models.ParseModeHTML,
	}

	if _, err := bot.SendMessage(ctx, params); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			slog.Warn("telegram: HTML rejected by Telegram, sending as plain text",
				"method", "Send",
				"error", err.Error(),
				"html_prefix", truncateForLog(html, 200),
				"html_len", len(html),
			)
			params.Text = content
			params.ParseMode = ""
			_, err = bot.SendMessage(ctx, params)
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
	params := &tgbot.SendPhotoParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Photo:           &models.InputFileUpload{Filename: name, Data: bytes.NewReader(img.Data)},
	}
	if _, err := bot.SendPhoto(ctx, params); err != nil {
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
	params := &tgbot.SendDocumentParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Document:        &models.InputFileUpload{Filename: name, Data: bytes.NewReader(file.Data)},
	}
	if _, err := bot.SendDocument(ctx, params); err != nil {
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

	if err := p.sendVoice(ctx, rc, sendData, sendFormat); err != nil {
		if sendFormat == "mp3" || sendFormat == "m4a" {
			if fallbackErr := p.sendAudio(ctx, rc, sendData, sendFormat); fallbackErr == nil {
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

func (p *Platform) sendVoice(ctx context.Context, rc replyContext, audio []byte, format string) error {
	bot, err := p.connectedBot("send voice")
	if err != nil {
		return err
	}
	params := &tgbot.SendVoiceParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Voice:           &models.InputFileUpload{Filename: "tts_audio." + telegramAudioFileExt(format), Data: bytes.NewReader(audio)},
	}
	if _, err := bot.SendVoice(ctx, params); err != nil {
		return err
	}
	return nil
}

func (p *Platform) sendAudio(ctx context.Context, rc replyContext, audio []byte, format string) error {
	bot, err := p.connectedBot("send audio")
	if err != nil {
		return err
	}
	params := &tgbot.SendAudioParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Audio:           &models.InputFileUpload{Filename: "tts_audio." + telegramAudioFileExt(format), Data: bytes.NewReader(audio)},
	}
	if _, err := bot.SendAudio(ctx, params); err != nil {
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

	var rows [][]models.InlineKeyboardButton
	for _, row := range buttons {
		var btns []models.InlineKeyboardButton
		for _, b := range row {
			btns = append(btns, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		rows = append(rows, btns)
	}

	html := core.MarkdownToSimpleHTML(content)
	params := &tgbot.SendMessageParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Text:            html,
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup:     &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	}

	if _, err := bot.SendMessage(ctx, params); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			slog.Warn("telegram: HTML rejected by Telegram, sending as plain text",
				"method", "SendWithButtons",
				"error", err.Error(),
				"html_prefix", truncateForLog(html, 200),
				"html_len", len(html),
			)
			params.Text = content
			params.ParseMode = ""
			_, err = bot.SendMessage(ctx, params)
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
	_, err = bot.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: h.chatID, MessageID: h.messageID})
	if err != nil {
		slog.Debug("telegram: delete preview message failed", "error", err)
	}
	return err
}

func (p *Platform) downloadFile(fileID string) ([]byte, error) {
	bot, err := p.connectedBot("download file")
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	f, err := bot.GetFile(ctx, &tgbot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("get file: empty file_path returned for file_id %s", fileID)
	}
	link := bot.FileDownloadLink(f)

	resp, err := p.httpClient.Get(link)
	if err != nil {
		return nil, fmt.Errorf("download file %s: %w", fileID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file %s: status %d", fileID, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// Formats:
	//   telegram:{chatID}                      - shared session, no topic
	//   telegram:{chatID}:{threadID}           - shared session, with topic
	//   telegram:{chatID}:{userID}             - per-user session, no topic
	//   telegram:{chatID}:{threadID}:{userID}  - per-user session, with topic
	parts := strings.SplitN(sessionKey, ":", 5)
	if len(parts) < 2 || parts[0] != "telegram" {
		return nil, fmt.Errorf("telegram: invalid session key %q", sessionKey)
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram: invalid chat ID in %q", sessionKey)
	}

	threadID := 0
	switch len(parts) {
	case 2:
		// telegram:{chatID}
	case 3:
		if p.shareSessionInChannel {
			// telegram:{chatID}:{threadID}
			threadID, _ = strconv.Atoi(parts[2])
		}
		// else: telegram:{chatID}:{userID} — no threadID
	case 4:
		// telegram:{chatID}:{threadID}:{userID}
		threadID, _ = strconv.Atoi(parts[2])
	}

	return replyContext{chatID: chatID, threadID: threadID}, nil
}

// telegramPreviewHandle stores the chat, thread, and message IDs for an editable preview message.
type telegramPreviewHandle struct {
	chatID    int64
	threadID  int
	messageID int
}

// SendPreviewStart sends a new message and returns a handle for subsequent edits.
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
	params := &tgbot.SendMessageParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Text:            html,
		ParseMode:       models.ParseModeHTML,
	}

	sent, err := bot.SendMessage(ctx, params)
	if err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			slog.Warn("telegram: HTML rejected by Telegram, sending preview as plain text",
				"method", "SendPreviewStart",
				"error", err.Error(),
				"html_prefix", truncateForLog(html, 200),
				"html_len", len(html),
			)
			params.Text = content
			params.ParseMode = ""
			sent, err = bot.SendMessage(ctx, params)
		}
		if err != nil {
			return nil, fmt.Errorf("telegram: send preview: %w", err)
		}
	}
	return &telegramPreviewHandle{chatID: rc.chatID, threadID: rc.threadID, messageID: sent.ID}, nil
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

	params := &tgbot.EditMessageTextParams{
		ChatID:    h.chatID,
		MessageID: h.messageID,
		Text:      html,
		ParseMode: models.ParseModeHTML,
	}

	if _, err := bot.EditMessageText(ctx, params); err != nil {
		errMsg := err.Error()
		slog.Debug("telegram: UpdateMessage HTML failed", "error", errMsg)
		if strings.Contains(errMsg, "not modified") {
			return nil
		}
		if strings.Contains(errMsg, "can't parse") {
			slog.Warn("telegram: HTML rejected by Telegram, editing as plain text",
				"method", "UpdateMessage",
				"error", errMsg,
				"html_prefix", truncateForLog(html, 200),
				"html_len", len(html),
			)
			params.Text = content
			params.ParseMode = ""
			if _, err2 := bot.EditMessageText(ctx, params); err2 != nil {
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

	params := &tgbot.SendChatActionParams{
		ChatID:          rc.chatID,
		MessageThreadID: rc.threadID,
		Action:          models.ChatActionTyping,
	}

	if bot, err := p.connectedBot("typing"); err == nil {
		if _, err := bot.SendChatAction(ctx, params); err != nil {
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
				if _, err := bot.SendChatAction(ctx, params); err != nil {
					slog.Debug("telegram: typing send failed", "error", err)
				}
			}
		}
	}()

	return func() { close(done) }
}

func truncateForLog(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

const telegramBotCommandDescriptionLimit = 40

// truncateTelegramBotDescription keeps Telegram command descriptions within a
// conservative safety budget. Telegram documents a larger per-field limit, but
// shorter descriptions avoid command menu registration failures when many
// commands are installed. Byte slicing breaks UTF-8 for CJK text and triggers
// "text must be encoded in UTF-8" from the API (#119).
func truncateTelegramBotDescription(s string) string {
	const max = telegramBotCommandDescriptionLimit
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	cancel := p.cancel
	p.cancel = nil
	p.bot = nil
	p.selfUser = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// RegisterCommands registers bot commands with Telegram for the command menu.
func (p *Platform) RegisterCommands(commands []core.BotCommandInfo) error {
	bot, err := p.connectedBot("register commands")
	if err != nil {
		return err
	}

	// Telegram limits: max 100 commands; keep descriptions conservatively short
	// to avoid menu registration failures with larger command sets.
	var tgCommands []models.BotCommand
	seen := make(map[string]bool)
	for _, c := range commands {
		cmd := sanitizeTelegramCommand(c.Command)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		desc := truncateTelegramBotDescription(c.Description)
		tgCommands = append(tgCommands, models.BotCommand{
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

	ctx := context.Background()
	if _, err := bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: tgCommands}); err != nil {
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
