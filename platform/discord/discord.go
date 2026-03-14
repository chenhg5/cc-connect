package discord

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("discord", New)
}

const maxDiscordLen = 2000

type replyContext struct {
	channelID string
	messageID string
}

// interactionReplyCtx handles Discord slash command (Application Command)
// responses. The first reply edits the deferred interaction response;
// subsequent replies use followup messages.
type interactionReplyCtx struct {
	interaction *discordgo.Interaction
	channelID   string
	mu          sync.Mutex
	firstDone   bool
}

type Platform struct {
	token                 string
	allowFrom             string
	guildID               string // optional: per-guild registration (instant) vs global (up to 1h propagation)
	groupReplyAll         bool
	shareSessionInChannel bool
	session               *discordgo.Session
	handler               core.MessageHandler
	botID                 string
	appID                 string
	readyCh               chan struct{}
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("discord: token is required")
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("discord", allowFrom)
	guildID, _ := opts["guild_id"].(string)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	return &Platform{
		token:                 token,
		allowFrom:             allowFrom,
		guildID:               guildID,
		groupReplyAll:         groupReplyAll,
		shareSessionInChannel: shareSessionInChannel,
		readyCh:               make(chan struct{}),
	}, nil
}

func (p *Platform) Name() string { return "discord" }

func (p *Platform) makeSessionKey(channelID string, userID string) string {
	if p.shareSessionInChannel {
		return fmt.Sprintf("discord:%s", channelID)
	} else {
		return fmt.Sprintf("discord:%s:%s", channelID, userID)
	}
}

// RegisterCommands registers bot commands with Discord for the slash command menu.
func (p *Platform) RegisterCommands(commands []core.BotCommandInfo) error {
	// Wait for Ready event to ensure appID is populated
	select {
	case <-p.readyCh:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("discord: timed out waiting for Ready event")
	}

	var cmds []*discordgo.ApplicationCommand
	for _, c := range commands {
		if len(c.Command) > 32 {
			slog.Warn("discord: command name > 32 skip " + c.Command)
			continue
		}
		desc := c.Description
		if runes := []rune(desc); len(runes) > 100 {
			desc = string(runes[:97]) + "..."
		}
		cmds = append(cmds, &discordgo.ApplicationCommand{
			Name:        c.Command,
			Description: desc,
			// A trick to be able to input any args
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Description: "optional args",
					Name:        "args",
					Required:    false,
				},
			},
		})
	}

	// Limit to 200 commands
	if len(cmds) > 200 {
		cmds = cmds[:200]
		slog.Warn("discord: commands > 200, truncate")
	}

	if len(cmds) == 0 {
		slog.Debug("discord: no commands to register")
		return nil
	}

	registered, err := p.session.ApplicationCommandBulkOverwrite(p.appID, p.guildID, cmds)
	if err != nil {
		slog.Error("discord: failed to register slash commands — "+
			"make sure the bot was invited with BOTH 'bot' AND 'applications.commands' OAuth2 scopes. "+
			"Re-invite URL: https://discord.com/oauth2/authorize?client_id="+p.appID+
			"&scope=bot+applications.commands&permissions=2147485696",
			"error", err, "guild_id", p.guildID)
		return err
	}
	scope := "global (may take up to 1h to appear — set guild_id for instant)"
	if p.guildID != "" {
		scope = "guild:" + p.guildID
	}
	slog.Info("discord: registered slash commands", "count", len(registered), "scope", scope)

	return nil
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	// 配置代理
	proxyURL := "http://127.0.0.1:7890"
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				return url.Parse(proxyURL)
			},
		},
	}

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	
	// 设置 HTTP 代理
	session.Client = httpClient
	session.Client.Transport = &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}
	
	// 设置 WebSocket 代理（通过 Dialer）
	if session.Dialer == nil {
		session.Dialer = &websocket.Dialer{}
	}
	session.Dialer.Proxy = func(req *http.Request) (*url.URL, error) {
		return url.Parse(proxyURL)
	}
	
	p.session = session

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		p.botID = r.User.ID
		p.appID = r.User.ID
		slog.Info("discord: connected", "bot", r.User.Username+"#"+r.User.Discriminator)
		select {
		case <-p.readyCh:
		default:
			close(p.readyCh)
		}
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || m.Author.ID == p.botID {
			return
		}
		if core.IsOldMessage(m.Timestamp) {
			slog.Debug("discord: ignoring old message after restart", "timestamp", m.Timestamp)
			return
		}
		if !core.AllowList(p.allowFrom, m.Author.ID) {
			slog.Debug("discord: message from unauthorized user", "user", m.Author.ID)
			return
		}

		// In guild channels, only respond when the bot is @mentioned (unless group_reply_all)
		if m.GuildID != "" && !p.groupReplyAll {
			mentioned := false
			for _, u := range m.Mentions {
				if u.ID == p.botID {
					mentioned = true
					break
				}
			}
			if !mentioned {
				slog.Debug("discord: ignoring guild message without bot mention", "channel", m.ChannelID)
				return
			}
			m.Content = stripDiscordMention(m.Content, p.botID)
		}

		slog.Debug("discord: message received", "user", m.Author.Username, "channel", m.ChannelID)

		sessionKey := p.makeSessionKey(m.ChannelID, m.Author.ID)
		rctx := replyContext{channelID: m.ChannelID, messageID: m.ID}

		var images []core.ImageAttachment
		var files []core.FileAttachment
		var audio *core.AudioAttachment
		for _, att := range m.Attachments {
			ct := strings.ToLower(att.ContentType)
			if strings.HasPrefix(ct, "audio/") {
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download audio failed", "url", att.URL, "error", err)
					continue
				}
				format := "ogg"
				if parts := strings.SplitN(ct, "/", 2); len(parts) == 2 {
					format = parts[1]
				}
				audio = &core.AudioAttachment{
					MimeType: ct, Data: data, Format: format,
				}
			} else if att.Width > 0 && att.Height > 0 {
				// 图片
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download attachment failed", "url", att.URL, "error", err)
					continue
				}
				images = append(images, core.ImageAttachment{
					MimeType: att.ContentType, Data: data, FileName: att.Filename,
				})
			} else {
				// 文件（PDF、DOC 等）
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download file failed", "url", att.URL, "error", err)
					continue
				}
				// 检测 MimeType
				mimeType := att.ContentType
				if mimeType == "" {
					mimeType = detectMimeTypeFromFilename(att.Filename)
				}
				files = append(files, core.FileAttachment{
					MimeType: mimeType,
					Data:     data,
					FileName: att.Filename,
				})
				slog.Debug("discord: file received", "filename", att.Filename, "size", len(data), "mime", mimeType)
			}
		}

		if m.Content == "" && len(images) == 0 && len(files) == 0 && audio == nil {
			return
		}

		msg := &core.Message{
			SessionKey: sessionKey, Platform: "discord",
			MessageID: m.ID,
			UserID:    m.Author.ID, UserName: m.Author.Username,
			Content: m.Content, Images: images, Files: files, Audio: audio, ReplyCtx: rctx,
		}
		p.handler(p, msg)
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		p.handleInteraction(s, i)
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord: open gateway: %w", err)
	}

	return nil
}

// handleInteraction processes an incoming Discord slash command interaction.
func (p *Platform) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	userID, userName := "", ""
	if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
		userName = i.Member.User.Username
	} else if i.User != nil {
		userID = i.User.ID
		userName = i.User.Username
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug("discord: interaction from unauthorized user", "user", userID)
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "You are not authorized to use this bot.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		slog.Error("discord: defer interaction failed", "error", err)
		return
	}

	data := i.ApplicationCommandData()
	cmdText := reconstructCommand(data)
	channelID := i.ChannelID

	slog.Debug("discord: slash command", "user", userName, "command", cmdText, "channel", channelID)

	sessionKey := p.makeSessionKey(channelID, userID)
	ictx := &interactionReplyCtx{
		interaction: i.Interaction,
		channelID:   channelID,
	}

	msg := &core.Message{
		SessionKey: sessionKey, Platform: "discord",
		MessageID: i.ID,
		UserID:    userID, UserName: userName,
		Content: cmdText, ReplyCtx: ictx,
	}
	p.handler(p, msg)
}

// reconstructCommand converts a Discord interaction back to a text command string
// (e.g. "/config thinking_max_len 200") that the engine can parse.
func reconstructCommand(data discordgo.ApplicationCommandInteractionData) string {
	name := data.Name
	var parts []string
	parts = append(parts, "/"+name)
	for _, opt := range data.Options {
		switch opt.Type {
		case discordgo.ApplicationCommandOptionInteger:
			parts = append(parts, fmt.Sprintf("%d", opt.IntValue()))
		default:
			parts = append(parts, opt.StringValue())
		}
	}
	return strings.Join(parts, " ")
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	content = formatContentForDiscord(content)
	switch rc := rctx.(type) {
	case *interactionReplyCtx:
		return p.sendInteraction(rc, content)
	case replyContext:
		return p.sendChannelReply(rc, content)
	default:
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}
}

// Send sends a new message (not a reply).
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	content = formatContentForDiscord(content)
	switch rc := rctx.(type) {
	case *interactionReplyCtx:
		return p.sendInteraction(rc, content)
	case replyContext:
		return p.sendChannel(rc, content)
	default:
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}
}

// tableSeparator matches markdown table separator line (e.g. |------|------| or |:---|:---:|---:|)
var tableSeparator = regexp.MustCompile(`^\|([\-:\s]*\|)+[\-:\s]*$`)

// isTableRow returns true if the line looks like a markdown table row (starts and ends with |)
func isTableRow(line string) bool {
	s := strings.TrimSpace(line)
	return len(s) >= 2 && s[0] == '|' && s[len(s)-1] == '|'
}

// formatContentForDiscord 将 Markdown 表格包在代码块中，Discord 不原生支持表格，用等宽字体显示可改善对齐
func formatContentForDiscord(content string) string {
	lines := strings.Split(content, "\n")
	var out strings.Builder
	i := 0
	for i < len(lines) {
		line := lines[i]
		// 检测表格：当前行是表头（含 |），下一行是分隔线，再下一行起是数据行
		if isTableRow(line) && i+1 < len(lines) && tableSeparator.MatchString(strings.TrimSpace(lines[i+1])) {
			tableStart := i
			i += 2 // 跳过表头和分隔线
			for i < len(lines) && isTableRow(lines[i]) {
				i++
			}
			// 把整段表格包在 ``` 代码块里
			out.WriteString("```\n")
			for j := tableStart; j < i; j++ {
				out.WriteString(lines[j])
				out.WriteByte('\n')
			}
			out.WriteString("```\n")
			continue
		}
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
		i++
	}
	return out.String()
}

// sendInteraction delivers a message through the Discord interaction response
// mechanism. The first call edits the deferred "thinking" response; subsequent
// calls create followup messages.
func (p *Platform) sendInteraction(ictx *interactionReplyCtx, content string) error {
	for len(content) > 0 {
		chunk := content
		if len(chunk) > maxDiscordLen {
			cut := maxDiscordLen
			if idx := lastIndexBefore(content, '\n', cut); idx > 0 {
				cut = idx + 1
			}
			chunk = content[:cut]
			content = content[cut:]
		} else {
			content = ""
		}

		ictx.mu.Lock()
		first := !ictx.firstDone
		if first {
			ictx.firstDone = true
		}
		ictx.mu.Unlock()

		var err error
		if first {
			c := chunk
			_, err = p.session.InteractionResponseEdit(ictx.interaction, &discordgo.WebhookEdit{Content: &c})
		} else {
			_, err = p.session.FollowupMessageCreate(ictx.interaction, true, &discordgo.WebhookParams{Content: chunk})
		}

		if err != nil {
			slog.Warn("discord: interaction response failed, falling back to channel message", "error", err)
			_, err = p.session.ChannelMessageSend(ictx.channelID, chunk)
			if err != nil {
				return fmt.Errorf("discord: send fallback: %w", err)
			}
		}
	}
	return nil
}

func (p *Platform) sendChannelReply(rc replyContext, content string) error {
	chunks := core.SplitMessageCodeFenceAware(content, maxDiscordLen)
	for _, chunk := range chunks {
		ref := &discordgo.MessageReference{MessageID: rc.messageID}
		_, err := p.session.ChannelMessageSendReply(rc.channelID, chunk, ref)
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) sendChannel(rc replyContext, content string) error {
	chunks := core.SplitMessageCodeFenceAware(content, maxDiscordLen)
	for _, chunk := range chunks {
		_, err := p.session.ChannelMessageSend(rc.channelID, chunk)
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// discord:{channelID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "discord" {
		return nil, fmt.Errorf("discord: invalid session key %q", sessionKey)
	}
	return replyContext{channelID: parts[1]}, nil
}

// discordPreviewHandle stores the IDs needed to edit or delete a preview message.
type discordPreviewHandle struct {
	channelID string
	messageID string
}

// SendPreviewStart sends a new message and returns a handle for subsequent edits.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	var channelID string
	switch rc := rctx.(type) {
	case replyContext:
		channelID = rc.channelID
	case *interactionReplyCtx:
		channelID = rc.channelID
	default:
		return nil, fmt.Errorf("discord: invalid reply context type %T", rctx)
	}

	if len(content) > maxDiscordLen {
		content = content[:maxDiscordLen]
	}
	sent, err := p.session.ChannelMessageSend(channelID, content)
	if err != nil {
		return nil, fmt.Errorf("discord: send preview: %w", err)
	}
	return &discordPreviewHandle{channelID: channelID, messageID: sent.ID}, nil
}

// UpdateMessage edits an existing message identified by previewHandle.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	h, ok := previewHandle.(*discordPreviewHandle)
	if !ok {
		return fmt.Errorf("discord: invalid preview handle type %T", previewHandle)
	}
	if len(content) > maxDiscordLen {
		content = content[:maxDiscordLen]
	}
	_, err := p.session.ChannelMessageEdit(h.channelID, h.messageID, content)
	if err != nil {
		return fmt.Errorf("discord: edit message: %w", err)
	}
	return nil
}

// DeletePreviewMessage removes the preview message so the final response can
// be sent as a fresh message (avoids notification confusion).
func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	h, ok := previewHandle.(*discordPreviewHandle)
	if !ok {
		return fmt.Errorf("discord: invalid preview handle type %T", previewHandle)
	}
	return p.session.ChannelMessageDelete(h.channelID, h.messageID)
}

// StartTyping sends a typing indicator and repeats every 8 seconds
// (Discord typing status lasts ~10s) until the returned stop function is called.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}
	channelID := rc.channelID
	if channelID == "" {
		return func() {}
	}

	_ = p.session.ChannelTyping(channelID)

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = p.session.ChannelTyping(channelID)
			}
		}
	}()

	return func() { close(done) }
}

func (p *Platform) Stop() error {
	if p.session != nil {
		return p.session.Close()
	}
	return nil
}

// stripDiscordMention removes <@botID> and <@!botID> (nick mention) from text.
func stripDiscordMention(text, botID string) string {
	text = strings.ReplaceAll(text, "<@!"+botID+">", "")
	text = strings.ReplaceAll(text, "<@"+botID+">", "")
	return strings.TrimSpace(text)
}

func downloadURL(u string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Discord CDN 需要 User-Agent
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/chenhg5/cc-connect, 1.0.0)")
	
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	// Discord CDN 有时会返回 303 重定向
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusFound {
		// 获取重定向 URL
		redirectURL := resp.Header.Get("Location")
		if redirectURL != "" {
			return downloadURL(redirectURL)
		}
	}
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: status %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func lastIndexBefore(s string, b byte, before int) int {
	for i := before - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// detectMimeTypeFromFilename 根据文件名猜测 MimeType
func detectMimeTypeFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	case ".rar":
		return "application/x-rar-compressed"
	case ".7z":
		return "application/x-7z-compressed"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	default:
		return "application/octet-stream"
	}
}
