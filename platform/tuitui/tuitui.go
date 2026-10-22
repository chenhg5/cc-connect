package tuitui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("tuitui", New)
}

const (
	defaultAPIBase          = "https://im.live.360.cn:8282"
	defaultWSBase           = "wss://im.live.360.cn:8282"
	httpTimeout             = 60 * time.Second
	initialReconnectBackoff = time.Second
	maxReconnectBackoff     = 30 * time.Second
	attachmentDownloadTO    = 60 * time.Second
	reactionTimeout         = 5 * time.Second
	outboundEchoTTL         = 2 * time.Minute
	defaultHistoryLimit     = 50
	maxAttachmentBytes      = 25 * 1024 * 1024
	maxJSONResponseBytes    = 16 * 1024 * 1024

	chatTypeDirect  = "direct"
	chatTypeGroup   = "group"
	chatTypeChannel = "channel"
)

type replyContext struct {
	chatID    string
	chatType  string
	messageID string
}

type Platform struct {
	appID                 string
	appSecret             string
	apiBase               string
	wsBase                string
	allowFrom             string
	groupAllowFrom        string
	ignoreFrom            string
	groupPolicy           string
	receiveReaction       string
	requireMention        bool
	shareSessionInChannel bool
	pendingHistoryLimit   int

	mu             sync.RWMutex
	handler        core.MessageHandler
	cancel         context.CancelFunc
	stopping       bool
	client         *http.Client
	dedup          core.MessageDedup
	outboundEcho   map[string]time.Time
	historyMu      sync.Mutex
	pendingHistory map[string][]pendingHistoryEntry
	nextHistoryID  uint64
}

type pendingHistoryEntry struct {
	id        uint64
	sender    string
	body      string
	timestamp time.Time
}

var (
	_ core.Platform                      = (*Platform)(nil)
	_ core.ImageSender                   = (*Platform)(nil)
	_ core.FileSender                    = (*Platform)(nil)
	_ core.ReplyContextReconstructor     = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
)

// New creates a TuiTui platform from config options.
//
//	[[projects.platforms]]
//	type = "tuitui"
//	[projects.platforms.options]
//	app_id = "${TUITUI_APP_ID}"
//	app_secret = "${TUITUI_APP_SECRET}"
//	allow_from = "*"              # user accounts, comma-separated
//	group_allow_from = "123,456"  # group IDs, team IDs, or channel IDs
//	ignore_from = "bot-xxx"       # bot accounts to ignore when webhook echoes outbound messages
//	require_mention = true        # group chats and channel posts require @bot
func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("tuitui: app_id and app_secret are required")
	}
	apiBase, _ := opts["api_base"].(string)
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	wsBase, _ := opts["ws_base"].(string)
	if wsBase == "" {
		wsBase = defaultWSBase
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("tuitui", allowFrom)
	groupAllowFrom, _ := opts["group_allow_from"].(string)
	ignoreFrom, _ := opts["ignore_from"].(string)
	groupPolicy, _ := opts["group_policy"].(string)
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	groupPolicy = strings.ToLower(strings.TrimSpace(groupPolicy))
	switch groupPolicy {
	case "allowlist", "open", "disabled":
	default:
		return nil, fmt.Errorf("tuitui: invalid group_policy %q (want allowlist, open, or disabled)", groupPolicy)
	}
	requireMention := true
	if v, ok := opts["require_mention"].(bool); ok {
		requireMention = v
	}
	receiveReaction := "收到"
	if v, ok := opts["receive_reaction"].(string); ok {
		receiveReaction = strings.TrimSpace(v)
	}
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	pendingHistoryLimit := defaultHistoryLimit
	if v, ok := intOption(opts["history_limit"]); ok {
		if v < 0 {
			return nil, fmt.Errorf("tuitui: history_limit must be non-negative")
		}
		pendingHistoryLimit = v
	}

	return &Platform{
		appID:                 appID,
		appSecret:             appSecret,
		apiBase:               strings.TrimRight(apiBase, "/"),
		wsBase:                strings.TrimRight(wsBase, "/"),
		allowFrom:             allowFrom,
		groupAllowFrom:        groupAllowFrom,
		ignoreFrom:            ignoreFrom,
		groupPolicy:           groupPolicy,
		receiveReaction:       receiveReaction,
		requireMention:        requireMention,
		shareSessionInChannel: shareSessionInChannel,
		pendingHistoryLimit:   pendingHistoryLimit,
		client:                &http.Client{Timeout: httpTimeout},
	}, nil
}

func (p *Platform) Name() string { return "tuitui" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return fmt.Errorf("tuitui: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.connectLoop(ctx)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopping = true
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.sendText(ctx, replyCtx, content)
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.sendText(ctx, replyCtx, content)
}

func (p *Platform) SendChannelPost(ctx context.Context, channelID, markdown, parentID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return fmt.Errorf("tuitui: channel id is required")
	}
	if strings.TrimSpace(markdown) == "" {
		return fmt.Errorf("tuitui: markdown is required")
	}

	chatID := channelID
	if guessChatType(channelID) == chatTypeChannel {
		if parentID = strings.TrimSpace(parentID); parentID != "" {
			target := teamsParseChatID(channelID)
			chatID = teamsBuildChatID(target["team_id"], target["channel_id"], parentID)
		}
	} else {
		info, err := p.getChannelInfo(ctx, channelID)
		if err != nil {
			return err
		}
		teamID := stringFromAny(info["team_id"])
		if teamID == "" {
			return fmt.Errorf("tuitui: team_id not found for channel %q", channelID)
		}
		chatID = teamsBuildChatID(teamID, channelID, strings.TrimSpace(parentID))
	}
	return p.sendText(ctx, replyContext{chatID: chatID, chatType: chatTypeChannel}, markdown)
}

func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	name := img.FileName
	if name == "" {
		name = "image"
	}
	mediaID, _, err := p.uploadMedia(ctx, img.Data, img.MimeType, name, "image")
	if err != nil {
		return fmt.Errorf("tuitui: upload image: %w", err)
	}
	rctx, err := requireReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.sendMediaID(ctx, rctx, mediaID, name, true)
}

func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	name := file.FileName
	if name == "" {
		name = "file"
	}
	isImage := strings.HasPrefix(file.MimeType, "image/")
	mediaType := "file"
	if isImage {
		mediaType = "image"
	}
	mediaID, _, err := p.uploadMedia(ctx, file.Data, file.MimeType, name, mediaType)
	if err != nil {
		return fmt.Errorf("tuitui: upload file: %w", err)
	}
	rctx, err := requireReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.sendMediaID(ctx, rctx, mediaID, name, isImage)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "tuitui" {
		return nil, fmt.Errorf("tuitui: invalid session key %q", sessionKey)
	}
	chatID := parts[1]
	if chatID == "" {
		return nil, fmt.Errorf("tuitui: invalid session key %q", sessionKey)
	}
	return replyContext{chatID: chatID, chatType: guessChatType(chatID)}, nil
}

func (p *Platform) FormattingInstructions() string {
	return `Formatting rules for TuiTui:
- Plain Markdown is accepted in group text messages and teams/channel messages.
- For group chats, use standard Markdown in normal text replies; do not ask for
  page/card messages unless a folded article-style message is explicitly wanted.
- Keep tables short; prefer concise lists for mobile chat readability.

TuiTui history tools:
- When the user message includes a ` + "`Recent TuiTui messages`" + ` block, treat it as
  authoritative recent chat context captured by cc-connect. If that injected
  context is sufficient to answer, answer directly without calling the history CLI.
- Use the history CLI only when the injected context is missing or insufficient,
  such as when the user asks for older messages, a broader time range, files,
  links, reports, or details not present in the injected block:
  cc-connect tuitui messages --chat <chat_id> --chat-type <direct|group|channel> --relative-time last_7_days --limit 100
  cc-connect tuitui search --chat <chat_id> --chat-type <direct|group|channel> --relative-time last_7_days --q <keyword>
  cc-connect tuitui download --url <file_or_image_url> --out ./tmp/tuitui
- The current chat id is the ` + "`chat_id`" + ` value in the injected sender context when present.
- To publish a Teams/channel markdown post, use:
  cc-connect tuitui post --channel <channel_id|teams_team_channel[_parent]> --message <markdown>
  or pipe long markdown with:
  cc-connect tuitui post --channel <channel_id> --stdin < post.md`
}

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialReconnectBackoff
	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		err := p.runWS(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		slog.Warn("tuitui: websocket disconnected, retrying", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxReconnectBackoff {
			backoff = maxReconnectBackoff
		}
	}
}

func (p *Platform) runWS(ctx context.Context) error {
	wsURL := p.wsBase + "/robot/callback/ws?auth=" + url.QueryEscape(p.appID+"."+p.appSecret)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %s", core.RedactToken(err.Error(), p.appSecret))
	}
	defer func() { _ = conn.Close() }()
	slog.Info("tuitui: websocket connected")

	done := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			p.handleFrame(ctx, conn, data)
		}
	}()

	select {
	case <-ctx.Done():
		_ = writeNormalClosure(conn)
		return ctx.Err()
	case err := <-done:
		return err
	}
}

type websocketControlWriter interface {
	WriteControl(messageType int, data []byte, deadline time.Time) error
}

func writeNormalClosure(conn websocketControlWriter) error {
	return conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	)
}

func (p *Platform) handleFrame(ctx context.Context, conn *websocket.Conn, data []byte) {
	var frame tuituiFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		slog.Warn("tuitui: failed to parse websocket frame", "error", err)
		return
	}
	if ack := frame.EventID(); ack != "" {
		_ = conn.WriteJSON(map[string]string{"ack": ack})
	}
	if frame.Body.Event == "" {
		return
	}
	dispatchEvent(func() { p.handleEvent(ctx, &frame) })
}

func dispatchEvent(handler func()) {
	go handler()
}

func (p *Platform) handleEvent(ctx context.Context, frame *tuituiFrame) {
	env := buildEnvelope(frame)
	if env.chatID == "" || env.messageID == "" {
		return
	}
	if env.text == "" && !env.hasMedia() {
		return
	}
	if msgTime := env.messageTime(); !msgTime.IsZero() && core.IsOldMessage(msgTime) {
		slog.Debug("tuitui: ignoring old message after restart", "date", msgTime)
		return
	}
	dedupeKey := strings.Join([]string{frame.EventID(), env.chatID, env.messageID, env.text}, "|")
	if p.dedup.IsDuplicate(dedupeKey) {
		return
	}
	if p.isSelfMessage(env) {
		slog.Debug("tuitui: ignoring self/bot message", "chat_type", env.chatType, "chat_id", env.chatID, "user_id", env.senderID)
		return
	}
	if !p.isAllowedIgnoringMention(env) {
		slog.Debug("tuitui: message ignored by policy", "chat_type", env.chatType, "chat_id", env.chatID, "user_id", env.senderID)
		return
	}
	if p.requiresMention(env) && !env.atMe {
		p.rememberPendingHistory(env)
		return
	}

	rctx := replyContext{chatID: env.chatID, chatType: env.chatType, messageID: env.messageID}
	p.reactOnReceive(rctx)

	images, files, audio := p.fetchInboundMedia(ctx, env)
	text := env.text
	if text == "" && audio == nil && (len(images) > 0 || len(files) > 0) {
		switch {
		case len(images) > 0 && len(files) == 0:
			text = "Please look at the attached image."
		case len(files) > 0 && len(images) == 0:
			text = "Please look at the attached file."
		default:
			text = "Please look at the attached files."
		}
	}

	sessionKey := p.sessionKey(env)
	handler := p.getHandler()
	if handler == nil {
		return
	}
	extraContent, onAccepted := p.pendingHistoryContext(env)
	handler(p, &core.Message{
		SessionKey:   sessionKey,
		Platform:     "tuitui",
		MessageID:    env.messageID,
		UserID:       env.senderID,
		UserName:     env.senderName,
		ChatName:     env.chatName,
		Content:      text,
		ExtraContent: extraContent,
		OnAccepted:   onAccepted,
		Images:       images,
		Files:        files,
		Audio:        audio,
		ChannelKey:   env.chatID,
		ReplyCtx:     rctx,
	})
}

func (p *Platform) getHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

func (p *Platform) isAllowed(env inboundEnvelope) bool {
	return p.isAllowedIgnoringMention(env) && (!p.requiresMention(env) || env.atMe)
}

func (p *Platform) isAllowedIgnoringMention(env inboundEnvelope) bool {
	if env.chatType == chatTypeDirect {
		return core.AllowList(p.allowFrom, env.senderID)
	}
	if p.groupPolicy == "disabled" {
		return false
	}
	if explicitUserAllowed(p.allowFrom, env.senderID) {
		return true
	}
	if env.chatType == chatTypeChannel {
		return allowListConfigured(p.groupAllowFrom, env.teamID) || allowListConfigured(p.groupAllowFrom, env.channelID)
	}
	if p.groupPolicy == "open" {
		return true
	}
	return allowListConfigured(p.groupAllowFrom, env.chatID)
}

func (p *Platform) requiresMention(env inboundEnvelope) bool {
	return p.requireMention && (env.chatType == chatTypeGroup || env.chatType == chatTypeChannel)
}

func (p *Platform) rememberPendingHistory(env inboundEnvelope) {
	if p.pendingHistoryLimit <= 0 || env.text == "" {
		return
	}
	entry := pendingHistoryEntry{
		sender:    senderDescription(env.senderName, env.senderID),
		body:      env.text,
		timestamp: env.messageTime(),
	}
	p.historyMu.Lock()
	defer p.historyMu.Unlock()
	p.nextHistoryID++
	entry.id = p.nextHistoryID
	if p.pendingHistory == nil {
		p.pendingHistory = make(map[string][]pendingHistoryEntry)
	}
	history := append(p.pendingHistory[env.chatID], entry)
	if len(history) > p.pendingHistoryLimit {
		history = history[len(history)-p.pendingHistoryLimit:]
	}
	p.pendingHistory[env.chatID] = history
}

func (p *Platform) pendingHistoryContext(env inboundEnvelope) (string, func()) {
	if env.chatType != chatTypeGroup && env.chatType != chatTypeChannel {
		return "", nil
	}
	p.historyMu.Lock()
	history := append([]pendingHistoryEntry(nil), p.pendingHistory[env.chatID]...)
	p.historyMu.Unlock()
	if len(history) == 0 {
		return "", nil
	}
	lines := []string{"Recent TuiTui messages sent in this chat before the bot was mentioned (context only):"}
	for _, entry := range history {
		prefix := entry.sender
		if !entry.timestamp.IsZero() {
			prefix = entry.timestamp.Format("2006-01-02 15:04:05") + " " + prefix
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", prefix, entry.body))
	}
	maxID := history[len(history)-1].id
	var once sync.Once
	return strings.Join(lines, "\n"), func() {
		once.Do(func() { p.consumePendingHistory(env.chatID, maxID) })
	}
}

func (p *Platform) consumePendingHistory(chatID string, maxID uint64) {
	p.historyMu.Lock()
	defer p.historyMu.Unlock()
	history := p.pendingHistory[chatID]
	kept := history[:0]
	for _, entry := range history {
		if entry.id > maxID {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		delete(p.pendingHistory, chatID)
		return
	}
	p.pendingHistory[chatID] = kept
}

func (p *Platform) isSelfMessage(env inboundEnvelope) bool {
	if env.senderID != "" && strings.EqualFold(env.senderID, normalizeID(p.appID)) {
		return true
	}
	if allowListConfigured(p.ignoreFrom, env.senderID) {
		return true
	}
	return p.isRecentOutboundEcho(env)
}

func (p *Platform) sessionKey(env inboundEnvelope) string {
	if env.chatType == chatTypeDirect {
		return "tuitui:" + env.senderID
	}
	if p.shareSessionInChannel || env.chatType == chatTypeChannel {
		return "tuitui:" + env.chatID
	}
	return "tuitui:" + env.chatID + ":" + env.senderID
}

func requireReplyContext(replyCtx any) (replyContext, error) {
	rctx, ok := replyCtx.(replyContext)
	if !ok {
		return replyContext{}, fmt.Errorf("tuitui: unexpected replyCtx type %T", replyCtx)
	}
	if rctx.chatType == "" {
		rctx.chatType = guessChatType(rctx.chatID)
	}
	return rctx, nil
}

func (p *Platform) sendText(ctx context.Context, replyCtx any, content string) error {
	rctx, err := requireReplyContext(replyCtx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": content,
		},
	}
	switch rctx.chatType {
	case chatTypeChannel:
		markdown := replaceSingleNewlines(replaceMentions(content))
		payload = map[string]any{
			"msgtype": "richtext/markdown",
			"richtext": map[string]string{
				"markdown": markdown,
			},
		}
		if strings.Contains(markdown, "{{tuitui_at") {
			payload["richtext"].(map[string]string)["delims_left"] = "{{"
			payload["richtext"].(map[string]string)["delims_right"] = "}}"
		}
	case chatTypeGroup:
		payload["at"] = extractMentions(content)
	}
	addTargets(payload, rctx.chatID, rctx.chatType)
	return p.sendPayload(ctx, payload)
}

func (p *Platform) reactOnReceive(rctx replyContext) {
	emoji := strings.TrimSpace(p.receiveReaction)
	if emoji == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reactionTimeout)
		defer cancel()
		if err := p.reactToMessage(ctx, rctx, emoji); err != nil {
			slog.Warn("tuitui: receive reaction failed", "error", err)
		}
	}()
}

func (p *Platform) reactToMessage(ctx context.Context, rctx replyContext, emoji string) error {
	if rctx.chatType == "" {
		rctx.chatType = guessChatType(rctx.chatID)
	}
	payload := map[string]any{
		"msgtype":        "emoji_reaction",
		"tousers":        []map[string]string{},
		"togroups":       []map[string]string{},
		"toteams":        []map[string]string{},
		"emoji_reaction": map[string]any{"emoji": emoji, "cancel": false},
	}
	switch rctx.chatType {
	case chatTypeDirect:
		payload["tousers"] = []map[string]string{{"user": rctx.chatID, "msgid": rctx.messageID}}
	case chatTypeGroup:
		payload["togroups"] = []map[string]string{{"group": rctx.chatID, "msgid": rctx.messageID}}
	case chatTypeChannel:
		team := teamsParseChatID(rctx.chatID)
		team["parent_id"] = ""
		team["post_id"] = rctx.messageID
		payload["toteams"] = []map[string]string{team}
	default:
		return fmt.Errorf("tuitui: invalid chat type %q", rctx.chatType)
	}
	return p.postJSON(ctx, "/robot/message/custom/modify", payload, nil)
}

func (p *Platform) sendMediaID(ctx context.Context, rctx replyContext, mediaID, filename string, isImage bool) error {
	payload := map[string]any{}
	if rctx.chatType == chatTypeChannel {
		markdown := fmt.Sprintf("[%s]({{tuitui_file %q}})", filename, mediaID)
		if isImage {
			markdown = fmt.Sprintf("![]({{tuitui_image %q}})", mediaID)
		}
		payload["msgtype"] = "richtext/markdown"
		payload["richtext"] = map[string]string{
			"markdown":     markdown,
			"delims_left":  "{{",
			"delims_right": "}}",
		}
	} else if isImage {
		payload["msgtype"] = "image"
		payload["image"] = map[string]string{"media_id": mediaID}
	} else {
		payload["msgtype"] = "attachment"
		payload["attachment"] = map[string]string{"media_id": mediaID}
	}
	addTargets(payload, rctx.chatID, rctx.chatType)
	return p.sendPayload(ctx, payload)
}

func (p *Platform) sendPayload(ctx context.Context, payload map[string]any) error {
	var response sendMessageResponse
	if err := p.postJSON(ctx, "/robot/message/custom/send", payload, &response); err != nil {
		return err
	}
	if response.ErrCode != 0 {
		return fmt.Errorf("errcode=%d errmsg=%s", response.ErrCode, response.ErrMsg)
	}
	p.rememberOutboundEcho(response.messageIDs())
	return nil
}

func (p *Platform) postJSON(ctx context.Context, apiPath string, payload any, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	u, err := url.Parse(p.apiBase + apiPath)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("appid", p.appID)
	q.Set("secret", p.appSecret)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return errorsRedacted(err, p.appSecret)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxJSONResponseBytes {
		return fmt.Errorf("tuitui: %s response exceeds %d bytes", apiPath, maxJSONResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var apiResp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("tuitui: decode %s response: %w (body_len=%d)", apiPath, err, len(body))
		}
		return nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("tuitui: decode %s response: %w (body_len=%d)", apiPath, err, len(body))
	}
	if apiResp.ErrCode != 0 {
		return fmt.Errorf("errcode=%d errmsg=%s", apiResp.ErrCode, apiResp.ErrMsg)
	}
	return nil
}

func (p *Platform) uploadMedia(ctx context.Context, data []byte, mimeType, filename, mediaType string) (mediaID, outName string, err error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("media", filename)
	if err != nil {
		return "", "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", "", err
	}
	if err := mw.Close(); err != nil {
		return "", "", err
	}
	u, err := url.Parse(p.apiBase + "/robot/media/upload")
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	q.Set("appid", p.appID)
	q.Set("secret", p.appSecret)
	q.Set("type", mediaType)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), &body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if mimeType != "" {
		req.Header.Set("X-Content-Type-Hint", mimeType)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", errorsRedacted(err, p.appSecret)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}
	var parsed struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", "", err
	}
	if parsed.ErrCode != 0 || parsed.MediaID == "" {
		return "", "", fmt.Errorf("errcode=%d errmsg=%s", parsed.ErrCode, parsed.ErrMsg)
	}
	return parsed.MediaID, filename, nil
}

func (p *Platform) fetchInboundMedia(ctx context.Context, env inboundEnvelope) ([]core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment) {
	data := env.data
	var images []core.ImageAttachment
	for i, imgURL := range data.Images {
		buf, mimeType, name, err := p.downloadAttachment(ctx, imgURL)
		if err != nil {
			slog.Warn("tuitui: image download failed", "error", err)
			continue
		}
		if name == "" {
			name = fmt.Sprintf("image_%d", i+1)
		}
		images = append(images, core.ImageAttachment{MimeType: mimeType, Data: buf, FileName: name})
	}

	var files []core.FileAttachment
	if data.File.URL != "" {
		buf, mimeType, name, err := p.downloadAttachment(ctx, data.File.URL)
		if err != nil {
			slog.Warn("tuitui: file download failed", "error", err)
		} else {
			if data.File.Name != "" {
				name = data.File.Name
			}
			files = append(files, core.FileAttachment{MimeType: mimeType, Data: buf, FileName: name})
		}
	}

	var audio *core.AudioAttachment
	if data.Voice != "" {
		buf, mimeType, name, err := p.downloadAttachment(ctx, data.Voice)
		if err != nil {
			slog.Warn("tuitui: voice download failed", "error", err)
		} else {
			audio = &core.AudioAttachment{MimeType: mimeType, Data: buf, Format: extFormat(name)}
		}
	}
	return images, files, audio
}

func (p *Platform) downloadAttachment(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	if rawURL == "" {
		return nil, "", "", fmt.Errorf("empty URL")
	}
	dctx, cancel := context.WithTimeout(ctx, attachmentDownloadTO)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", "", errorsRedacted(err, p.appSecret)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxAttachmentBytes {
		return nil, "", "", fmt.Errorf("attachment too large: %d", resp.ContentLength)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(buf) > maxAttachmentBytes {
		return nil, "", "", fmt.Errorf("attachment too large: %d", len(buf))
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(buf)
	}
	name := filenameFromResponse(rawURL, resp.Header.Get("Content-Disposition"))
	return buf, mimeType, name, nil
}

func (p *Platform) DownloadHistoryFile(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	return p.downloadAttachment(ctx, rawURL)
}

type tuituiFrame struct {
	ID     string `json:"event_id"`
	Header struct {
		ID string `json:"event_id"`
	} `json:"header"`
	Body tuituiBody `json:"body"`
}

func (f tuituiFrame) EventID() string {
	if f.ID != "" {
		return f.ID
	}
	return f.Header.ID
}

type tuituiBody struct {
	Event     string     `json:"event"`
	User      string     `json:"user_account"`
	UID       string     `json:"uid"`
	UserName  string     `json:"user_name"`
	Timestamp any        `json:"timestamp"`
	Data      tuituiData `json:"data"`
}

type tuituiData struct {
	MsgType     string        `json:"msg_type"`
	Text        string        `json:"text"`
	Images      []string      `json:"images"`
	Voice       string        `json:"voice"`
	Video       string        `json:"video"`
	AtMe        bool          `json:"at_me"`
	MsgID       string        `json:"msgid"`
	PostID      string        `json:"post_id"`
	GroupID     string        `json:"group_id"`
	GroupName   string        `json:"group_name"`
	TeamID      string        `json:"team_id"`
	ChannelID   string        `json:"channel_id"`
	ParentID    string        `json:"parent_id"`
	Content     string        `json:"content"`
	UserAccount string        `json:"user_account"`
	UserName    string        `json:"user_name"`
	Timestamp   any           `json:"timestamp"`
	File        tuituiFile    `json:"file"`
	Card        tuituiCard    `json:"card"`
	Link        tuituiLink    `json:"link"`
	Merged      *tuituiMerged `json:"merged"`
	Ref         *tuituiData   `json:"ref"`
}

type tuituiFile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type tuituiCard struct {
	Name    string `json:"name"`
	Account string `json:"account"`
}

type tuituiLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type tuituiMerged struct {
	Source   string       `json:"source"`
	Messages []tuituiData `json:"msgs"`
}

type sendMessageResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	MsgIDs  []struct {
		MsgID string `json:"msgid"`
	} `json:"msgids"`
}

func (r sendMessageResponse) messageIDs() []string {
	ids := make([]string, 0, len(r.MsgIDs))
	for _, item := range r.MsgIDs {
		if item.MsgID != "" {
			ids = append(ids, item.MsgID)
		}
	}
	return ids
}

type inboundEnvelope struct {
	event      string
	senderID   string
	senderName string
	chatType   string
	chatID     string
	chatName   string
	text       string
	messageID  string
	atMe       bool
	teamID     string
	channelID  string
	timestamp  any
	data       tuituiData
}

func buildEnvelope(frame *tuituiFrame) inboundEnvelope {
	body := frame.Body
	data := body.Data
	senderID := normalizeID(firstNonEmpty(body.User, body.UID))
	senderName := firstNonEmpty(body.UserName, body.User, body.UID, "unknown")
	env := inboundEnvelope{
		event:      body.Event,
		senderID:   senderID,
		senderName: senderName,
		chatType:   chatTypeDirect,
		chatID:     senderID,
		text:       buildMessageBody(data),
		messageID:  firstNonEmpty(data.MsgID, data.PostID, fmt.Sprintf("%d", time.Now().UnixNano())),
		atMe:       data.AtMe,
		timestamp:  body.Timestamp,
		data:       data,
	}
	switch body.Event {
	case "group_chat":
		env.chatType = chatTypeGroup
		env.chatID = normalizeID(data.GroupID)
		env.chatName = data.GroupName
	case "teams_post_create":
		env.chatType = chatTypeChannel
		env.teamID = data.TeamID
		env.channelID = data.ChannelID
		threadID := data.PostID
		if data.ParentID != "" && data.ParentID != "0" {
			threadID = data.ParentID
		}
		env.chatID = teamsBuildChatID(data.TeamID, data.ChannelID, threadID)
		env.chatName = "team:" + data.TeamID + "/channel:" + data.ChannelID
		env.messageID = firstNonEmpty(data.PostID, env.messageID)
		env.text = firstNonEmpty(data.Content, env.text)
	}
	return env
}

func (e inboundEnvelope) hasMedia() bool {
	return len(e.data.Images) > 0 || e.data.File.URL != "" || e.data.Voice != ""
}

func (e inboundEnvelope) messageTime() time.Time {
	switch v := e.timestamp.(type) {
	case float64:
		return timestampToTime(int64(v))
	case string:
		if v == "" {
			return time.Time{}
		}
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return timestampToTime(n)
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func timestampToTime(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n > 1_000_000_000_000 {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}

func timestampValueToTime(value any) time.Time {
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return timestampToTime(n)
		}
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	case float64:
		return timestampToTime(int64(v))
	case int:
		return timestampToTime(int64(v))
	case int64:
		return timestampToTime(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return timestampToTime(n)
		}
	}
	return time.Time{}
}

func intOption(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		if v > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(v), true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

func buildMessageBody(data tuituiData) string {
	parts := buildMessageParts(data)
	if data.Ref != nil {
		parts = append(parts, fmt.Sprintf("\n[quoted from %s]\n%s", senderDescription(data.Ref.UserName, data.Ref.UserAccount), strings.Join(nonEmpty(buildMessageParts(*data.Ref)), "\n")))
	}
	return strings.TrimSpace(strings.Join(nonEmpty(parts), "\n"))
}

func buildMessageParts(data tuituiData) []string {
	var parts []string
	switch data.MsgType {
	case "text":
		parts = append(parts, data.Text)
	case "mixed":
		parts = append(parts, data.Text)
		parts = append(parts, imageLines(data.Images)...)
	case "image":
		parts = append(parts, imageLines(data.Images)...)
	case "voice":
		if data.Voice != "" {
			parts = append(parts, "[voice] "+data.Voice)
		}
	case "video":
		if data.Video != "" {
			parts = append(parts, "[video] "+data.Video)
		}
	case "file":
		if data.File.URL != "" {
			parts = append(parts, fmt.Sprintf("[file] %s: %s", data.File.Name, data.File.URL))
		}
	case "card":
		if data.Card.Name != "" || data.Card.Account != "" {
			parts = append(parts, fmt.Sprintf("[contact]\nname: %s\naccount: %s", data.Card.Name, data.Card.Account))
		}
	case "link":
		if data.Link.Title != "" || data.Link.URL != "" {
			parts = append(parts, fmt.Sprintf("[link]\n%s\n%s", data.Link.Title, data.Link.URL))
		}
	case "merged":
		parts = append(parts, buildMergedBody(data.Merged))
	default:
		parts = append(parts, data.Text)
	}
	return nonEmpty(parts)
}

func buildMergedBody(merged *tuituiMerged) string {
	if merged == nil {
		return "[forwarded chat]"
	}
	source := firstNonEmpty(merged.Source, "chat history")
	lines := []string{"[forwarded chat: " + source + "]"}
	for _, message := range merged.Messages {
		lines = append(lines, "------")
		if ts := timestampValueToTime(message.Timestamp); !ts.IsZero() {
			lines = append(lines, "time: "+ts.Format("2006-01-02 15:04:05"))
		}
		lines = append(lines, "sender: "+senderDescription(message.UserName, message.UserAccount), "content:")
		lines = append(lines, buildMessageParts(message)...)
	}
	return strings.Join(lines, "\n")
}

func senderDescription(name, account string) string {
	name = strings.TrimSpace(name)
	account = normalizeID(account)
	if name != "" && account != "" {
		return fmt.Sprintf("%s (%s)", name, account)
	}
	return firstNonEmpty(name, account, "unknown")
}

func imageLines(images []string) []string {
	if len(images) == 0 {
		return nil
	}
	if len(images) == 1 {
		return []string{"[image] " + images[0]}
	}
	lines := []string{fmt.Sprintf("[images] %d images:", len(images))}
	for i, img := range images {
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, img))
	}
	return lines
}

func addTargets(payload map[string]any, chatID, chatType string) {
	switch chatType {
	case chatTypeDirect:
		payload["tousers"] = []string{chatID}
	case chatTypeGroup:
		payload["togroups"] = []string{chatID}
	case chatTypeChannel:
		payload["toteams"] = []map[string]string{teamsParseChatID(chatID)}
	}
}

func guessChatType(chatID string) string {
	if strings.HasPrefix(chatID, "teams_") {
		return chatTypeChannel
	}
	if regexp.MustCompile(`^\d+$`).MatchString(chatID) {
		return chatTypeGroup
	}
	return chatTypeDirect
}

func teamsBuildChatID(teamID, channelID, threadID string) string {
	out := "teams_" + teamID + "_" + channelID
	if threadID != "" {
		out += "_" + threadID
	}
	return out
}

func teamsParseChatID(chatID string) map[string]string {
	parts := strings.Split(strings.TrimPrefix(chatID, "teams_"), "_")
	out := map[string]string{}
	if len(parts) > 0 {
		out["team_id"] = parts[0]
	}
	if len(parts) > 1 {
		out["channel_id"] = parts[1]
	}
	if len(parts) > 2 {
		out["parent_id"] = parts[2]
	}
	out["post_id"] = ""
	return out
}

var mentionRE = regexp.MustCompile(`(^|[\s\r\n　、。，！？…])@([^\s,，.。;；:：!！?？、)）\]】}｝]+)`)

func extractMentions(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRE.FindAllStringSubmatch(text, -1) {
		if len(m) < 3 || seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		out = append(out, m[2])
	}
	return out
}

func replaceMentions(text string) string {
	return mentionRE.ReplaceAllString(text, `${1}{{tuitui_at "$2"}}`)
}

func replaceSingleNewlines(content string) string {
	return regexp.MustCompile(`([^\n])\n([^\n])`).ReplaceAllString(content, "$1\n\n$2")
}

func filenameFromResponse(rawURL, contentDisposition string) string {
	if contentDisposition != "" {
		for _, part := range strings.Split(contentDisposition, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(part), "filename=") {
				return strings.Trim(strings.TrimPrefix(part, "filename="), `"`)
			}
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

func extFormat(filename string) string {
	ext := strings.TrimPrefix(path.Ext(filename), ".")
	if ext == "" {
		return "audio"
	}
	return ext
}

func normalizeID(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func errorsRedacted(err error, secret string) error {
	if err == nil {
		return nil
	}
	return errors.New(core.RedactToken(err.Error(), secret))
}

func allowListConfigured(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" {
		return false
	}
	return core.AllowList(allowFrom, userID)
}

func (p *Platform) rememberOutboundEcho(messageIDs []string) {
	if len(messageIDs) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outboundEcho == nil {
		p.outboundEcho = make(map[string]time.Time)
	}
	now := time.Now()
	for k, t := range p.outboundEcho {
		if now.Sub(t) > outboundEchoTTL {
			delete(p.outboundEcho, k)
		}
	}
	for _, messageID := range messageIDs {
		if messageID != "" {
			p.outboundEcho[messageID] = now
		}
	}
}

func (p *Platform) isRecentOutboundEcho(env inboundEnvelope) bool {
	if env.messageID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, t := range p.outboundEcho {
		if now.Sub(t) > outboundEchoTTL {
			delete(p.outboundEcho, k)
		}
	}
	t, ok := p.outboundEcho[env.messageID]
	if !ok {
		return false
	}
	delete(p.outboundEcho, env.messageID)
	return now.Sub(t) <= outboundEchoTTL
}

func explicitUserAllowed(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" || allowFrom == "*" {
		return false
	}
	return core.AllowList(allowFrom, userID)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
