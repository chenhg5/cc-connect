package discord

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

	"github.com/bwmarrin/discordgo"
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
}

func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("discord: token is required")
	}
	allowFrom, _ := opts["allow_from"].(string)
	guildID, _ := opts["guild_id"].(string)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	return &Platform{token: token, allowFrom: allowFrom, guildID: guildID, groupReplyAll: groupReplyAll, shareSessionInChannel: shareSessionInChannel}, nil
}

func (p *Platform) Name() string { return "discord" }

// builtinSlashCommands defines the Application Commands registered with Discord.
func builtinSlashCommands() []*discordgo.ApplicationCommand {
	optStr := func(name, desc string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionString, Name: name, Description: desc, Required: required,
		}
	}
	optInt := func(name, desc string) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{
			Type: discordgo.ApplicationCommandOptionInteger, Name: name, Description: desc, Required: false,
		}
	}

	return []*discordgo.ApplicationCommand{
		{Name: "help", Description: "Show available commands"},
		{Name: "new", Description: "Start a new session", Options: []*discordgo.ApplicationCommandOption{
			optStr("name", "Session name", false),
		}},
		{Name: "list", Description: "List agent sessions"},
		{Name: "cc-switch", Description: "Resume an existing session", Options: []*discordgo.ApplicationCommandOption{
			optStr("id", "Session number or ID prefix", true),
		}},
		{Name: "delete", Description: "Delete a session by list number", Options: []*discordgo.ApplicationCommandOption{
			optStr("target", "Session number or ID prefix", true),
		}},
		{Name: "name", Description: "Name a session for easy identification", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. my-project, or: 2 my-project", true),
		}},
		{Name: "current", Description: "Show current active session"},
		{Name: "status", Description: "Show system status"},
		{Name: "history", Description: "Show recent messages", Options: []*discordgo.ApplicationCommandOption{
			optInt("count", "Number of messages (default 10)"),
		}},
		{Name: "model", Description: "View or switch model", Options: []*discordgo.ApplicationCommandOption{
			optStr("name", "Model name or number", false),
		}},
		{Name: "mode", Description: "View or switch permission mode", Options: []*discordgo.ApplicationCommandOption{
			optStr("name", "Mode: default / edit / plan / yolo", false),
		}},
		{Name: "lang", Description: "View or switch language", Options: []*discordgo.ApplicationCommandOption{
			optStr("language", "en / zh / zh-TW / ja / es / auto", false),
		}},
		{Name: "quiet", Description: "Toggle thinking/tool progress messages"},
		{Name: "compress", Description: "Compress conversation context"},
		{Name: "cc-stop", Description: "Stop current execution"},
		{Name: "version", Description: "Show cc-connect version"},
		{Name: "doctor", Description: "Run system diagnostics"},
		{Name: "upgrade", Description: "Check for updates and self-update", Options: []*discordgo.ApplicationCommandOption{
			optStr("action", "confirm to install update", false),
		}},
		{Name: "restart", Description: "Restart cc-connect service"},
		{Name: "skills", Description: "List agent skills"},
		{Name: "allow", Description: "Pre-allow a tool for next session", Options: []*discordgo.ApplicationCommandOption{
			optStr("tool", "Tool name (e.g. Bash)", false),
		}},
		{Name: "config", Description: "View or update runtime configuration", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. thinking_max_len 200", false),
		}},
		{Name: "provider", Description: "Manage API providers", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. list, switch <name>, add ...", false),
		}},
		{Name: "memory", Description: "View or edit agent memory files", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. add <text>, global, global add <text>", false),
		}},
		{Name: "cron", Description: "Manage scheduled tasks", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. list, add 0 6 * * * <prompt>", false),
		}},
		{Name: "commands", Description: "Manage custom slash commands", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. list, add <name> <prompt>, del <name>", false),
		}},
		{Name: "alias", Description: "Manage command aliases", Options: []*discordgo.ApplicationCommandOption{
			optStr("args", "e.g. list, add 帮助 /help, del 帮助", false),
		}},
	}
}

// slashNameToEngine maps Discord command names that differ from engine names.
var slashNameToEngine = map[string]string{
	"cc-switch": "switch",
	"cc-stop":   "stop",
}

func (p *Platform) makeSessionKey(channelID string, userID string) string {
	if p.shareSessionInChannel {
		return fmt.Sprintf("discord:%s", channelID)
	} else {
		return fmt.Sprintf("discord:%s:%s", channelID, userID)
	}
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	p.session = session

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		p.botID = r.User.ID
		p.appID = r.User.ID
		slog.Info("discord: connected", "bot", r.User.Username+"#"+r.User.Discriminator)
		go p.registerSlashCommands()
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
				data, err := downloadURL(att.URL)
				if err != nil {
					slog.Error("discord: download attachment failed", "url", att.URL, "error", err)
					continue
				}
				images = append(images, core.ImageAttachment{
					MimeType: att.ContentType, Data: data, FileName: att.Filename,
				})
			}
		}

		if m.Content == "" && len(images) == 0 && audio == nil {
			return
		}

		msg := &core.Message{
			SessionKey: sessionKey, Platform: "discord",
			UserID: m.Author.ID, UserName: m.Author.Username,
			Content: m.Content, Images: images, Audio: audio, ReplyCtx: rctx,
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

// registerSlashCommands registers all built-in commands with the Discord API.
func (p *Platform) registerSlashCommands() {
	cmds := builtinSlashCommands()
	registered, err := p.session.ApplicationCommandBulkOverwrite(p.appID, p.guildID, cmds)
	if err != nil {
		slog.Error("discord: failed to register slash commands — "+
			"make sure the bot was invited with BOTH 'bot' AND 'applications.commands' OAuth2 scopes. "+
			"Re-invite URL: https://discord.com/oauth2/authorize?client_id="+p.appID+
			"&scope=bot+applications.commands&permissions=2147485696",
			"error", err, "guild_id", p.guildID)
		return
	}
	scope := "global (may take up to 1h to appear — set guild_id for instant)"
	if p.guildID != "" {
		scope = "guild:" + p.guildID
	}
	slog.Info("discord: registered slash commands", "count", len(registered), "scope", scope)
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
		UserID: userID, UserName: userName,
		Content: cmdText, ReplyCtx: ictx,
	}
	p.handler(p, msg)
}

// reconstructCommand converts a Discord interaction back to a text command string
// (e.g. "/config thinking_max_len 200") that the engine can parse.
func reconstructCommand(data discordgo.ApplicationCommandInteractionData) string {
	name := data.Name
	if engineName, ok := slashNameToEngine[name]; ok {
		name = engineName
	}

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
	switch rc := rctx.(type) {
	case *interactionReplyCtx:
		return p.sendInteraction(rc, content)
	case replyContext:
		return p.sendChannel(rc, content)
	default:
		return fmt.Errorf("discord: invalid reply context type %T", rctx)
	}
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
	resp, err := core.HTTPClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
