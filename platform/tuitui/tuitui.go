package tuitui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
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
	maxAttachmentBytes      = 25 * 1024 * 1024

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
	groupPolicy           string
	requireMention        bool
	shareSessionInChannel bool

	mu       sync.RWMutex
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool
	client   *http.Client
	dedup    core.MessageDedup
}

// New creates a TuiTui platform from config options.
//
//	[[projects.platforms]]
//	type = "tuitui"
//	[projects.platforms.options]
//	app_id = "${TUITUI_APP_ID}"
//	app_secret = "${TUITUI_APP_SECRET}"
//	allow_from = "*"              # user accounts, comma-separated
//	group_allow_from = "123,456"  # group IDs, team IDs, or channel IDs
//	require_mention = true        # group chats only; channels are always accepted if allowlisted
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
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)

	return &Platform{
		appID:                 appID,
		appSecret:             appSecret,
		apiBase:               strings.TrimRight(apiBase, "/"),
		wsBase:                strings.TrimRight(wsBase, "/"),
		allowFrom:             allowFrom,
		groupAllowFrom:        groupAllowFrom,
		groupPolicy:           groupPolicy,
		requireMention:        requireMention,
		shareSessionInChannel: shareSessionInChannel,
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
- Plain Markdown is accepted in teams/channel messages.
- Direct and group chats render plain text most reliably.
- Keep tables short; prefer concise lists for mobile chat readability.`
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
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
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
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return ctx.Err()
	case err := <-done:
		return err
	}
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
	p.handleEvent(ctx, &frame)
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
	if !p.isAllowed(env) {
		slog.Debug("tuitui: message ignored by policy", "chat_type", env.chatType, "chat_id", env.chatID, "user_id", env.senderID)
		return
	}

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
	rctx := replyContext{chatID: env.chatID, chatType: env.chatType, messageID: env.messageID}
	handler := p.getHandler()
	if handler == nil {
		return
	}
	handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "tuitui",
		MessageID:  env.messageID,
		UserID:     env.senderID,
		UserName:   env.senderName,
		ChatName:   env.chatName,
		Content:    text,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ChannelKey: env.chatID,
		ReplyCtx:   rctx,
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
	if env.chatType == chatTypeDirect {
		return core.AllowList(p.allowFrom, env.senderID)
	}
	if p.groupPolicy == "disabled" {
		return false
	}
	if p.requireMention && env.chatType == chatTypeGroup && !env.atMe {
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
	if rctx.chatType == chatTypeChannel {
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
	} else if rctx.chatType == chatTypeGroup {
		payload["at"] = extractMentions(content)
	}
	addTargets(payload, rctx.chatID, rctx.chatType)
	return p.postJSON(ctx, "/robot/message/custom/send", payload, nil)
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
	return p.postJSON(ctx, "/robot/message/custom/send", payload, nil)
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
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var apiResp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return err
		}
		return nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return err
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
		return "", "", err
	}
	defer resp.Body.Close()
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
		return nil, "", "", err
	}
	defer resp.Body.Close()
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
	MsgType   string   `json:"msg_type"`
	Text      string   `json:"text"`
	Images    []string `json:"images"`
	Voice     string   `json:"voice"`
	AtMe      bool     `json:"at_me"`
	MsgID     string   `json:"msgid"`
	PostID    string   `json:"post_id"`
	GroupID   string   `json:"group_id"`
	GroupName string   `json:"group_name"`
	TeamID    string   `json:"team_id"`
	ChannelID string   `json:"channel_id"`
	ParentID  string   `json:"parent_id"`
	Content   string   `json:"content"`
	File      struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"file"`
	Ref *struct {
		MsgType  string   `json:"msg_type"`
		Text     string   `json:"text"`
		UserName string   `json:"user_name"`
		Images   []string `json:"images"`
		File     struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"file"`
	} `json:"ref"`
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

func buildMessageBody(data tuituiData) string {
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
	case "file":
		if data.File.URL != "" {
			parts = append(parts, fmt.Sprintf("[file] %s: %s", data.File.Name, data.File.URL))
		}
	default:
		parts = append(parts, data.Text)
	}
	if data.Ref != nil {
		refText := firstNonEmpty(data.Ref.Text, data.Ref.File.URL, strings.Join(data.Ref.Images, "\n"), "["+data.Ref.MsgType+"]")
		parts = append(parts, fmt.Sprintf("\n[quoted from %s]\n%s", data.Ref.UserName, refText))
	}
	return strings.TrimSpace(strings.Join(nonEmpty(parts), "\n"))
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
	return "teams_" + teamID + "_" + channelID + "_" + threadID
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

func allowListConfigured(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" {
		return false
	}
	return core.AllowList(allowFrom, userID)
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
