package line

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

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
	messageID  string
	sessionKey string
	userID     string
	userName   string
	chatName   string
}

type Platform struct {
	channelSecret  string
	channelToken   string
	allowFrom      string
	port           string
	callbackPath   string
	bot            *messaging_api.MessagingApiAPI
	server         *http.Server
	handler        core.MessageHandler
	userNameCache  sync.Map // userID -> display name
	groupNameCache sync.Map // groupID -> group name
}

func New(opts map[string]any) (core.Platform, error) {
	secret, _ := opts["channel_secret"].(string)
	token, _ := opts["channel_token"].(string)
	allowFrom, _ := opts["allow_from"].(string)
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

	core.CheckAllowFrom("line", allowFrom)
	return &Platform{
		channelSecret: secret,
		channelToken:  token,
		allowFrom:     allowFrom,
		port:          port,
		callbackPath:  path,
	}, nil
}

func (p *Platform) Name() string { return "line" }

func (p *Platform) AuditReplyMetadata(replyCtx any) core.AuditReplyMetadata {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return core.AuditReplyMetadata{}
	}
	return core.AuditReplyMetadata{
		SessionKey:       rc.sessionKey,
		UserID:           rc.userID,
		UserName:         rc.userName,
		ChatName:         rc.chatName,
		ChannelKey:       rc.targetID,
		ReplyToMessageID: rc.messageID,
		ParentMessageID:  rc.messageID,
		Extra: map[string]any{
			"target_id":   rc.targetID,
			"target_type": rc.targetType,
		},
	}
}

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

		if e.Timestamp > 0 {
			msgTime := time.Unix(e.Timestamp/1000, (e.Timestamp%1000)*int64(time.Millisecond))
			if core.IsOldMessage(msgTime) {
				slog.Debug("line: ignoring old message after restart", "timestamp", e.Timestamp)
				continue
			}
		}

		targetID, targetType, userID := extractSource(e.Source)
		if !core.AllowList(p.allowFrom, userID) {
			slog.Debug("line: message from unauthorized user", "user", userID)
			continue
		}
		sessionKey := fmt.Sprintf("line:%s", targetID)
		chatName := ""
		if targetType == "group" {
			chatName = p.resolveGroupName(targetID)
		}
		rctx := replyContext{
			targetID:   targetID,
			targetType: targetType,
			sessionKey: sessionKey,
			userID:     userID,
			userName:   p.resolveUserName(userID),
			chatName:   chatName,
		}

		switch m := e.Message.(type) {
		case webhook.TextMessageContent:
			slog.Debug("line: message received", "user", userID, "text_len", len(m.Text))
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "line",
				MessageID: m.Id,
				UserID:    userID, UserName: rctx.userName,
				ChatName: chatName,
				Content:  m.Text, ChannelKey: targetID, ReplyCtx: replyContext{
					targetID:   rctx.targetID,
					targetType: rctx.targetType,
					messageID:  m.Id,
					sessionKey: rctx.sessionKey,
					userID:     rctx.userID,
					userName:   rctx.userName,
					chatName:   rctx.chatName,
				},
				AuditExtra: map[string]any{
					"target_id":   targetID,
					"target_type": targetType,
				},
			})

		case webhook.ImageMessageContent:
			slog.Debug("line: image received", "user", userID)
			imgData, err := p.downloadContent(m.Id)
			if err != nil {
				slog.Error("line: download image failed", "error", err)
				continue
			}
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "line",
				MessageID: m.Id,
				UserID:    userID, UserName: rctx.userName,
				ChatName:   chatName,
				ChannelKey: targetID,
				Images:     []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
				ReplyCtx: replyContext{
					targetID:   rctx.targetID,
					targetType: rctx.targetType,
					messageID:  m.Id,
					sessionKey: rctx.sessionKey,
					userID:     rctx.userID,
					userName:   rctx.userName,
					chatName:   rctx.chatName,
				},
				AuditExtra: map[string]any{
					"target_id":   targetID,
					"target_type": targetType,
				},
			})

		case webhook.AudioMessageContent:
			slog.Debug("line: audio received", "user", userID)
			audioData, err := p.downloadContent(m.Id)
			if err != nil {
				slog.Error("line: download audio failed", "error", err)
				continue
			}
			dur := 0
			if m.Duration > 0 {
				dur = int(m.Duration / 1000)
			}
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "line",
				MessageID: m.Id,
				UserID:    userID, UserName: rctx.userName,
				ChatName:   chatName,
				ChannelKey: targetID,
				Audio: &core.AudioAttachment{
					MimeType: "audio/m4a",
					Data:     audioData,
					Format:   "m4a",
					Duration: dur,
				},
				ReplyCtx: replyContext{
					targetID:   rctx.targetID,
					targetType: rctx.targetType,
					messageID:  m.Id,
					sessionKey: rctx.sessionKey,
					userID:     rctx.userID,
					userName:   rctx.userName,
					chatName:   rctx.chatName,
				},
				AuditExtra: map[string]any{
					"target_id":   targetID,
					"target_type": targetType,
				},
			})

		default:
			slog.Debug("line: ignoring unsupported message type")
		}
	}
}

func (p *Platform) resolveUserName(userID string) string {
	if cached, ok := p.userNameCache.Load(userID); ok {
		return cached.(string)
	}
	profile, err := p.bot.GetProfile(userID)
	if err != nil {
		slog.Debug("line: resolve user name failed", "user", userID, "error", err)
		return userID
	}
	name := profile.DisplayName
	if name == "" {
		name = userID
	}
	p.userNameCache.Store(userID, name)
	return name
}

func (p *Platform) resolveGroupName(groupID string) string {
	if cached, ok := p.groupNameCache.Load(groupID); ok {
		return cached.(string)
	}
	summary, err := p.bot.GetGroupSummary(groupID)
	if err != nil {
		slog.Debug("line: resolve group name failed", "group_id", groupID, "error", err)
		return groupID
	}
	name := summary.GroupName
	if name == "" {
		return groupID
	}
	p.groupNameCache.Store(groupID, name)
	return name
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

func (p *Platform) downloadContent(messageID string) ([]byte, error) {
	url := fmt.Sprintf("https://api-data.line.me/v2/bot/message/%s/content", messageID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+p.channelToken)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	_, err := p.ReplyWithReceipt(ctx, rctx, content)
	return err
}

func (p *Platform) ReplyWithReceipt(ctx context.Context, rctx any, content string) (*core.SendReceipt, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("line: invalid reply context type %T", rctx)
	}

	if content == "" {
		return nil, nil
	}

	content = core.StripMarkdown(content)

	// LINE text message limit is 5000 characters
	messages := splitMessage(content, 5000)
	var sentMessageIDs []string
	for _, text := range messages {
		resp, err := p.bot.PushMessage(
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
			return nil, fmt.Errorf("line: push message: %w", err)
		}
		for _, sent := range resp.SentMessages {
			if id := strings.TrimSpace(sent.Id); id != "" {
				sentMessageIDs = append(sentMessageIDs, id)
			}
		}
	}
	return &core.SendReceipt{
		MessageID:       lastLineMessageID(sentMessageIDs),
		ParentMessageID: rc.messageID,
		Extra: map[string]any{
			"target_id":        rc.targetID,
			"target_type":      rc.targetType,
			"sent_message_ids": sentMessageIDs,
		},
	}, nil
}

// Send sends a new message (same as Reply for LINE)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	_, err := p.SendWithReceipt(ctx, rctx, content)
	return err
}

func (p *Platform) SendWithReceipt(ctx context.Context, rctx any, content string) (*core.SendReceipt, error) {
	return p.ReplyWithReceipt(ctx, rctx, content)
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

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// line:{targetID} (user or group)
	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) < 2 || parts[0] != "line" {
		return nil, fmt.Errorf("line: invalid session key %q", sessionKey)
	}
	return replyContext{targetID: parts[1], targetType: "user", sessionKey: sessionKey}, nil
}

func lastLineMessageID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

func (p *Platform) Stop() error {
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}
