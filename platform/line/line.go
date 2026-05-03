package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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
}

type Platform struct {
	channelSecret  string
	channelToken   string
	allowFrom      string
	port           string
	callbackPath   string
	groupReplyAll  bool // if true, respond to ALL group messages without @mention
	bot            lineClient
	server         *http.Server
	handler        core.MessageHandler
	userNameCache  sync.Map // userID -> display name
	groupNameCache sync.Map // groupID -> group name
	replyTokens    sync.Map // targetID -> tokenEntry  (Reply/Push hybrid dispatch)
	sweepCancel    context.CancelFunc
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
	groupReplyAll, _ := opts["group_reply_all"].(bool)

	core.CheckAllowFrom("line", allowFrom)
	return &Platform{
		channelSecret: secret,
		channelToken:  token,
		allowFrom:     allowFrom,
		port:          port,
		callbackPath:  path,
		groupReplyAll: groupReplyAll,
	}, nil
}

func (p *Platform) Name() string { return "line" }

// FormattingInstructions tells the agent to avoid Markdown since LINE is plain text.
func (p *Platform) FormattingInstructions() string {
	return `This platform is LINE — plain text only.
Do NOT use Markdown formatting (no **, ##, ` + "``" + `, []() etc.).
Use plain text, emoji, and line breaks for structure.
Use「」for quotes, ▸ or • for bullet points, and blank lines for separation.`
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

	ctx, cancel := context.WithCancel(context.Background())
	p.sweepCancel = cancel
	go p.sweepExpiredTokens(ctx)

	go func() {
		slog.Info("line: webhook server listening", "port", p.port, "path", p.callbackPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("line: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) webhookHandler(w http.ResponseWriter, r *http.Request) {
	bodyBytes, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		slog.Error("line: read body failed", "error", readErr)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	cb, err := webhook.ParseRequest(p.channelSecret, r)
	if err != nil {
		mac := hmac.New(sha256.New, []byte(p.channelSecret))
		mac.Write(bodyBytes)
		expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		snippet := string(bodyBytes)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		slog.Error("line: parse webhook failed",
			"error", err,
			"received_sig", r.Header.Get("X-Line-Signature"),
			"expected_sig", expected,
			"secret_prefix", firstN(p.channelSecret, 6),
			"secret_len", len(p.channelSecret),
			"body_len", len(bodyBytes),
			"body_snippet", snippet,
		)
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

		// Cache reply token for Reply/Push hybrid dispatch
		if e.ReplyToken != "" {
			p.cacheReplyToken(targetID, e.ReplyToken)
		}

		sessionKey := fmt.Sprintf("line:%s", targetID)
		rctx := replyContext{targetID: targetID, targetType: targetType}

		chatName := ""
		if targetType == "group" {
			chatName = p.resolveGroupName(targetID)
		}

		// In group/room: require @bot mention unless group_reply_all=true.
		// Non-text messages (image/audio) in groups have no mention concept —
		// skip them when group_reply_all=false to stay consistent with other adapters.
		isGroupLike := targetType == "group" || targetType == "room"
		requireMention := isGroupLike && !p.groupReplyAll

		// Show loading animation immediately so user knows we're processing
		go p.showLoading(userID)

		switch m := e.Message.(type) {
		case webhook.TextMessageContent:
			text := m.Text
			if requireMention {
				if !isBotMentioned(m.Mention) {
					slog.Debug("line: group message without @bot mention, skip", "user", userID)
					continue
				}
				text = stripBotMention(text, m.Mention)
			}
			slog.Debug("line: message received", "user", userID, "text_len", len(text))
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "line",
				MessageID: m.Id,
				UserID:    userID, UserName: p.resolveUserName(userID),
				ChatName:  chatName,
				Content:   text, ReplyCtx: rctx,
			})

		case webhook.ImageMessageContent:
			if requireMention {
				slog.Debug("line: skip group image (no @mention concept for images)", "user", userID)
				continue
			}
			slog.Debug("line: image received", "user", userID)
			imgData, err := p.downloadContent(m.Id)
			if err != nil {
				slog.Error("line: download image failed", "error", err)
				continue
			}
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "line",
				MessageID: m.Id,
				UserID:    userID, UserName: p.resolveUserName(userID),
				ChatName:  chatName,
				Images:    []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
				ReplyCtx:  rctx,
			})

		case webhook.AudioMessageContent:
			if requireMention {
				slog.Debug("line: skip group audio (no @mention concept for audio)", "user", userID)
				continue
			}
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
				UserID:    userID, UserName: p.resolveUserName(userID),
				ChatName:  chatName,
				Audio: &core.AudioAttachment{
					MimeType: "audio/m4a",
					Data:     audioData,
					Format:   "m4a",
					Duration: dur,
				},
				ReplyCtx: rctx,
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

// showLoading displays a typing indicator for the user (up to 60 seconds).
func (p *Platform) showLoading(userID string) {
	if p.bot == nil || userID == "" {
		return
	}
	_, err := p.bot.ShowLoadingAnimation(
		&messaging_api.ShowLoadingAnimationRequest{
			ChatId:         userID,
			LoadingSeconds: 60,
		},
	)
	if err != nil {
		slog.Debug("line: show loading failed", "error", err)
	}
}

// isBotMentioned reports whether the webhook's bot itself is mentioned.
func isBotMentioned(mention *webhook.Mention) bool {
	if mention == nil {
		return false
	}
	for _, m := range mention.Mentionees {
		if u, ok := m.(webhook.UserMentionee); ok && u.IsSelf {
			return true
		}
	}
	return false
}

// stripBotMention removes the @bot substring(s) from text using the Index/Length
// offsets provided by LINE. Iterates from the last mention backwards so earlier
// offsets stay valid.
func stripBotMention(text string, mention *webhook.Mention) string {
	if mention == nil {
		return text
	}
	runes := []rune(text)
	type rng struct{ start, end int }
	var ranges []rng
	for _, m := range mention.Mentionees {
		u, ok := m.(webhook.UserMentionee)
		if !ok || !u.IsSelf {
			continue
		}
		start := int(u.Index)
		end := start + int(u.Length)
		if start < 0 || end > len(runes) || start >= end {
			continue
		}
		ranges = append(ranges, rng{start, end})
	}
	for i := len(ranges) - 1; i >= 0; i-- {
		r := ranges[i]
		runes = append(runes[:r.start], runes[r.end:]...)
	}
	return strings.TrimSpace(string(runes))
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
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("line: invalid reply context type %T", rctx)
	}

	if content == "" {
		return nil
	}

	content = core.StripMarkdown(content)

	// Rule 13 violation scan — alert on implementation-leak phrases in outbound text.
	// Does NOT strip the text (observe-only for now); see SKILL.md 硬規則 13.
	if matches := scanRule13Violations(content); len(matches) > 0 {
		preview := content
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		core.AlertError("rule13_violation", "bot 回覆含實作細節洩漏關鍵詞（觀察模式，未修改內容）", map[string]any{
			"target_id":    rc.targetID,
			"target_type":  rc.targetType,
			"matches":      matches,
			"text_preview": preview,
			"text_len":     len(content),
		})
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
			core.AlertError("line_push_failed", "LINE PushMessage API returned error", map[string]any{
				"target_id":   rc.targetID,
				"target_type": rc.targetType,
				"content_len": len(text),
				"error":       err.Error(),
			})
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

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// line:{targetID} (user or group)
	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) < 2 || parts[0] != "line" {
		return nil, fmt.Errorf("line: invalid session key %q", sessionKey)
	}
	return replyContext{targetID: parts[1], targetType: "user"}, nil
}

func (p *Platform) Stop() error {
	if p.sweepCancel != nil {
		p.sweepCancel()
	}
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// rule13Patterns — phrases the bot should never reveal to the end user
// (see joey-brain SKILL.md 硬規則 13). Intentionally scoped to high-signal
// implementation leaks; generic words like "artifact" are avoided since they
// can appear in legitimate content. Update as you observe new violations.
var rule13Patterns = []*regexp.Regexp{
	regexp.MustCompile(`thread_id`),
	regexp.MustCompile(`phase\s*=\s*awaiting-`),
	regexp.MustCompile(`\bt_jw_`),
	regexp.MustCompile(`\bt_ins_`),
	regexp.MustCompile(`\btjw_`),
	regexp.MustCompile(`\bins_2\d{3}`),
	regexp.MustCompile(`context bundle`),
	regexp.MustCompile(`CDN\s*快取`),
	regexp.MustCompile(`CloudFront`),
	regexp.MustCompile(`InsForge`),
}

func scanRule13Violations(s string) []string {
	var hits []string
	for _, re := range rule13Patterns {
		if m := re.FindString(s); m != "" {
			hits = append(hits, m)
		}
	}
	return hits
}
