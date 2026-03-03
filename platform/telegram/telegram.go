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
	token      string
	allowFrom  string
	bot        *tgbotapi.BotAPI
	httpClient *http.Client
	handler    core.MessageHandler
	cancel     context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	allowFrom, _ := opts["allow_from"].(string)

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

	return &Platform{token: token, allowFrom: allowFrom, httpClient: httpClient}, nil
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
		case update := <-updates:
			// Handle inline keyboard button clicks
			if update.CallbackQuery != nil {
				p.handleCallbackQuery(update.CallbackQuery)
				continue
			}

			if update.Message == nil {
				continue
			}

			msg := update.Message
				userName := msg.From.UserName
				if userName == "" {
					userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
				}
				sessionKey := fmt.Sprintf("telegram:%d:%d", msg.Chat.ID, msg.From.ID)
				userID := strconv.FormatInt(msg.From.ID, 10)
				if !core.AllowList(p.allowFrom, userID) {
					slog.Debug("telegram: message from unauthorized user", "user", userID)
					continue
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
					coreMsg := &core.Message{
						SessionKey: sessionKey, Platform: "telegram",
						UserID: userID, UserName: userName,
						Content:  msg.Caption,
						Images:   []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
						ReplyCtx: rctx,
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
						UserID: userID, UserName: userName,
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
						UserID: userID, UserName: userName,
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

				if msg.Text == "" {
					continue
				}

				text := msg.Text
				if p.bot.Self.UserName != "" {
					text = strings.Replace(text, "@"+p.bot.Self.UserName, "", 1)
				}

				coreMsg := &core.Message{
					SessionKey: sessionKey, Platform: "telegram",
					UserID: userID, UserName: userName,
					Content: text, ReplyCtx: rctx,
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

	// Map callback data to permission response text
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

	// Edit the original message to show the choice and remove buttons
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
	editText := origText + "\n\n" + choiceLabel
	edit := tgbotapi.NewEditMessageText(chatID, msgID, editText)
	emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
	edit.ReplyMarkup = &emptyMarkup
	p.bot.Send(edit)

	// Route as a regular message to the engine's permission handler
	userName := cb.From.UserName
	if userName == "" {
		userName = strings.TrimSpace(cb.From.FirstName + " " + cb.From.LastName)
	}
	sessionKey := fmt.Sprintf("telegram:%d:%d", chatID, cb.From.ID)
	rctx := replyContext{chatID: chatID, messageID: msgID}

	coreMsg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "telegram",
		UserID:     userID,
		UserName:   userName,
		Content:    responseText,
		ReplyCtx:   rctx,
	}
	p.handler(p, coreMsg)
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context type %T", rctx)
	}

	reply := tgbotapi.NewMessage(rc.chatID, content)
	reply.ReplyToMessageID = rc.messageID
	reply.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(reply); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
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

	msg := tgbotapi.NewMessage(rc.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdown

	if _, err := p.bot.Send(msg); err != nil {
		// Markdown parse failure → retry as plain text
		if strings.Contains(err.Error(), "can't parse") {
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

	msg := tgbotapi.NewMessage(rc.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdown
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)

	if _, err := p.bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "can't parse") {
			msg.ParseMode = ""
			_, err = p.bot.Send(msg)
		}
		if err != nil {
			return fmt.Errorf("telegram: sendWithButtons: %w", err)
		}
	}
	return nil
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
		return nil, fmt.Errorf("download: %w", err)
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

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.bot != nil {
		p.bot.StopReceivingUpdates()
	}
	return nil
}
