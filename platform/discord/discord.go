package discord

import (
	"bytes"
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
	threadID  string
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
	token                      string
	allowFrom                  string
	guildID                    string // optional: per-guild registration (instant) vs global (up to 1h propagation)
	groupReplyAll              bool
	shareSessionInChannel      bool
	threadIsolation            bool
	respondToAtEveryoneAndHere bool
	session                    *discordgo.Session
	handler                    core.MessageHandler
	botID                      string
	appID                      string
	channelNameCache           sync.Map // channelID -> name
	botRoleIDs                 sync.Map // guildID -> bot managed role ID
	readyCh                    chan struct{}
	seenMsgs                   sync.Map // message ID dedup: prevents duplicate MessageCreate events
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
	threadIsolation, _ := opts["thread_isolation"].(bool)
	respondToAtEveryoneAndHere, _ := opts["respond_to_at_everyone_and_here"].(bool)
	return &Platform{
		token:                      token,
		allowFrom:                  allowFrom,
		guildID:                    guildID,
		groupReplyAll:              groupReplyAll,
		shareSessionInChannel:      shareSessionInChannel,
		readyCh:                    make(chan struct{}),
		threadIsolation:            threadIsolation,
		respondToAtEveryoneAndHere: respondToAtEveryoneAndHere,
	}, nil
}

func (p *Platform) Name() string { return "discord" }

func (p *Platform) makeSessionKey(channelID string, userID string) string {
	return buildSessionKey(channelID, userID, p.shareSessionInChannel)
}

func buildSessionKey(channelID string, userID string, shareSessionInChannel bool) string {
	if shareSessionInChannel {
		return fmt.Sprintf("discord:%s", channelID)
	}
	return fmt.Sprintf("discord:%s:%s", channelID, userID)
}

// TODO: thread_isolation currently keys each Discord thread as one shared session, so share_session_in_channel=false does not further isolate users within the same thread.
func buildThreadSessionKey(threadID string) string {
	return fmt.Sprintf("discord:%s", threadID)
}

func (rc replyContext) targetChannelID() string {
	if rc.threadID != "" {
		return rc.threadID
	}
	return rc.channelID
}

func (rc replyContext) useThreadChannel() bool {
	return rc.threadID != "" && rc.threadID == rc.channelID
}

type discordThreadOps interface {
	ResolveChannel(channelID string) (*discordgo.Channel, error)
	StartThread(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error)
	StartStandaloneThread(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error)
	JoinThread(threadID string) error
}

type sessionThreadOps struct {
	session *discordgo.Session
}

func (o sessionThreadOps) ResolveChannel(channelID string) (*discordgo.Channel, error) {
	if o.session == nil {
		return nil, fmt.Errorf("discord: session not initialized")
	}
	if ch, err := o.session.State.Channel(channelID); err == nil && ch != nil {
		return ch, nil
	}
	return o.session.Channel(channelID)
}

func (o sessionThreadOps) StartThread(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error) {
	if o.session == nil {
		return nil, fmt.Errorf("discord: session not initialized")
	}
	return o.session.MessageThreadStart(channelID, messageID, name, archiveDuration)
}

func (o sessionThreadOps) StartStandaloneThread(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error) {
	if o.session == nil {
		return nil, fmt.Errorf("discord: session not initialized")
	}
	return o.session.ThreadStart(channelID, name, typ, archiveDuration)
}

func (o sessionThreadOps) JoinThread(threadID string) error {
	if o.session == nil {
		return fmt.Errorf("discord: session not initialized")
	}
	return o.session.ThreadJoin(threadID)
}

func isThreadChannelType(t discordgo.ChannelType) bool {
	switch t {
	case discordgo.ChannelTypeGuildNewsThread,
		discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread:
		return true
	default:
		return false
	}
}

func truncateDiscordThreadName(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

func threadNameForMessage(m *discordgo.MessageCreate, botID string) string {
	name := stripDiscordMention(m.Content, botID)
	name = strings.Join(strings.Fields(strings.ReplaceAll(name, "\n", " ")), " ")
	if name == "" && m.Author != nil {
		name = "cc " + m.Author.Username
	}
	if name == "" {
		name = "cc session"
	}
	return truncateDiscordThreadName(name, 90)
}

func freshThreadName(title string) string {
	name := strings.Join(strings.Fields(strings.ReplaceAll(title, "\n", " ")), " ")
	if name == "" {
		name = "cc cron"
	}
	return truncateDiscordThreadName(name, 90)
}

func standaloneThreadType(parentType discordgo.ChannelType) (discordgo.ChannelType, bool) {
	switch parentType {
	case discordgo.ChannelTypeGuildText:
		return discordgo.ChannelTypeGuildPublicThread, true
	case discordgo.ChannelTypeGuildNews:
		return discordgo.ChannelTypeGuildNewsThread, true
	default:
		return 0, false
	}
}

func parseDiscordSessionKeyChannelID(sessionKey string) (string, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "discord" || parts[1] == "" {
		return "", fmt.Errorf("discord: invalid session key %q", sessionKey)
	}
	return parts[1], nil
}

func resolveSessionKeyForChannel(channelID, userID string, shareSessionInChannel bool, threadIsolation bool, ops discordThreadOps) string {
	if !threadIsolation {
		return buildSessionKey(channelID, userID, shareSessionInChannel)
	}
	ch, err := ops.ResolveChannel(channelID)
	if err != nil {
		slog.Warn("discord: resolve channel for session key failed, falling back", "channel", channelID, "error", err)
		return buildSessionKey(channelID, userID, shareSessionInChannel)
	}
	if isThreadChannelType(ch.Type) {
		return buildThreadSessionKey(channelID)
	}
	return buildSessionKey(channelID, userID, shareSessionInChannel)
}

func resolveThreadReplyContext(m *discordgo.MessageCreate, botID string, ops discordThreadOps) (string, replyContext, error) {
	ch, err := ops.ResolveChannel(m.ChannelID)
	if err != nil {
		return "", replyContext{}, fmt.Errorf("resolve channel %s: %w", m.ChannelID, err)
	}
	if isThreadChannelType(ch.Type) {
		if err := ops.JoinThread(m.ChannelID); err != nil {
			slog.Debug("discord: join existing thread failed", "thread", m.ChannelID, "error", err)
		}
		rc := replyContext{channelID: m.ChannelID, messageID: m.ID, threadID: m.ChannelID}
		return buildThreadSessionKey(m.ChannelID), rc, nil
	}
	if m.Message != nil && m.Message.Thread != nil && m.Message.Thread.ID != "" {
		threadID := m.Message.Thread.ID
		if err := ops.JoinThread(threadID); err != nil {
			slog.Debug("discord: join attached thread failed", "thread", threadID, "error", err)
		}
		rc := replyContext{channelID: threadID, messageID: m.ID, threadID: threadID}
		return buildThreadSessionKey(threadID), rc, nil
	}
	if m.Flags&discordgo.MessageFlagsHasThread != 0 {
		threadID := m.ID
		if err := ops.JoinThread(threadID); err != nil {
			slog.Debug("discord: join message thread failed", "thread", threadID, "error", err)
		}
		rc := replyContext{channelID: threadID, messageID: m.ID, threadID: threadID}
		return buildThreadSessionKey(threadID), rc, nil
	}

	thread, err := ops.StartThread(m.ChannelID, m.ID, threadNameForMessage(m, botID), 1440)
	if err != nil {
		return "", replyContext{}, fmt.Errorf("start thread for message %s: %w", m.ID, err)
	}
	if err := ops.JoinThread(thread.ID); err != nil {
		slog.Debug("discord: join new thread failed", "thread", thread.ID, "error", err)
	}
	rc := replyContext{channelID: thread.ID, messageID: m.ID, threadID: thread.ID}
	return buildThreadSessionKey(thread.ID), rc, nil
}

func resolveCronReplyTarget(sessionKey, title string, ops discordThreadOps) (string, replyContext, error) {
	channelID, err := parseDiscordSessionKeyChannelID(sessionKey)
	if err != nil {
		return "", replyContext{}, err
	}

	ch, err := ops.ResolveChannel(channelID)
	if err != nil {
		return "", replyContext{}, fmt.Errorf("resolve channel %s: %w", channelID, err)
	}
	parentChannelID := channelID
	parentType := ch.Type
	if isThreadChannelType(ch.Type) {
		if ch.ParentID == "" {
			return "", replyContext{}, core.ErrNotSupported
		}
		parent, err := ops.ResolveChannel(ch.ParentID)
		if err != nil {
			return "", replyContext{}, fmt.Errorf("resolve parent channel %s: %w", ch.ParentID, err)
		}
		parentChannelID = ch.ParentID
		parentType = parent.Type
	}

	threadType, ok := standaloneThreadType(parentType)
	if !ok {
		return "", replyContext{}, core.ErrNotSupported
	}

	thread, err := ops.StartStandaloneThread(parentChannelID, freshThreadName(title), threadType, 1440)
	if err != nil {
		return "", replyContext{}, fmt.Errorf("start thread in channel %s: %w", parentChannelID, err)
	}
	if err := ops.JoinThread(thread.ID); err != nil {
		slog.Debug("discord: join fresh thread failed", "thread", thread.ID, "error", err)
	}

	rc := replyContext{channelID: thread.ID, threadID: thread.ID}
	return buildThreadSessionKey(thread.ID), rc, nil
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

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	p.session = session

	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		p.botID = r.User.ID
		p.appID = r.User.ID
		slog.Info("discord: connected", "bot", r.User.Username+"#"+r.User.Discriminator)
		// Signal readiness before guild role lookups so RegisterCommands
		// is not blocked by slow API calls when there are many guilds.
		select {
		case <-p.readyCh:
		default:
			close(p.readyCh)
		}
		for _, g := range r.Guilds {
			if g == nil || g.ID == "" || g.Unavailable {
				continue
			}
			p.cacheBotRoleIDForGuild(s, g.ID, g.Roles)
		}
	})

	session.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		if g == nil || g.Guild == nil || g.ID == "" || g.Unavailable {
			return
		}
		p.cacheBotRoleIDForGuild(s, g.ID, g.Roles)
	})

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Deduplicate: Discord gateway may deliver the same event twice
		if _, loaded := p.seenMsgs.LoadOrStore(m.ID, struct{}{}); loaded {
			slog.Debug("discord: ignoring duplicate message", "msg_id", m.ID)
			return
		}
		time.AfterFunc(2*time.Minute, func() { p.seenMsgs.Delete(m.ID) })

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

		// In guild channels, only respond when the bot is @mentioned (unless group_reply_all).
		// Check both user mentions and role mentions (Discord auto-creates a managed role
		// for each bot; users may @ the role instead of the user).
		botRoleID := p.botRoleIDForGuild(m.GuildID)
		if botRoleID == "" && m.GuildID != "" {
			p.cacheBotRoleIDForGuild(s, m.GuildID, nil)
			botRoleID = p.botRoleIDForGuild(m.GuildID)
		}
		if m.GuildID != "" && !p.groupReplyAll {
			if !isDiscordBotMention(m, p.botID, botRoleID, p.respondToAtEveryoneAndHere) {
				slog.Debug("discord: ignoring guild message without bot mention", "channel", m.ChannelID)
				return
			}
			m.Content = stripDiscordMentionWithRole(m.Content, p.botID, botRoleID)
			if m.MentionEveryone {
				m.Content = stripEveryoneHere(m.Content)
			}
		}

		slog.Debug("discord: message received", "user", m.Author.Username, "channel", m.ChannelID)

		sessionKey := p.makeSessionKey(m.ChannelID, m.Author.ID)
		rctx := replyContext{channelID: m.ChannelID, messageID: m.ID}
		if p.threadIsolation && m.GuildID != "" {
			threadSessionKey, threadCtx, err := resolveThreadReplyContext(m, p.botID, sessionThreadOps{session: p.session})
			if err != nil {
				slog.Warn("discord: thread isolation setup failed, falling back", "message", m.ID, "channel", m.ChannelID, "error", err)
			} else {
				sessionKey = threadSessionKey
				rctx = threadCtx
			}
		}

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
			MessageID: m.ID,
			UserID:    m.Author.ID, UserName: m.Author.Username,
			ChatName: p.resolveChannelName(m.ChannelID),
			Content:  m.Content, Images: images, Audio: audio, ReplyCtx: rctx,
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

// handleInteraction processes incoming Discord command and button interactions.
func (p *Platform) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	switch i.Type {
	case discordgo.InteractionMessageComponent:
		p.handleComponentInteraction(s, i, userID, userName)
		return
	case discordgo.InteractionApplicationCommand:
	default:
		return
	}

	var rctx any
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		// Defer must usually happen within ~3s; if it fails (e.g. "Unknown interaction"),
		// aborting here drops the command entirely (#258). Fall back to normal channel
		// messages — sendInteraction already falls back similarly on edit failures.
		slog.Warn("discord: defer interaction failed, continuing with channel replies", "error", err)
		channelID := i.ChannelID
		var rc replyContext
		if ch, chErr := s.Channel(channelID); chErr != nil {
			slog.Debug("discord: channel lookup for slash fallback failed", "channel", channelID, "error", chErr)
			rc = replyContext{channelID: channelID}
		} else {
			rc = replyContextForDeferredInteractionFallback(ch, channelID)
		}
		rctx = rc
	} else {
		rctx = &interactionReplyCtx{
			interaction: i.Interaction,
			channelID:   i.ChannelID,
		}
	}

	data := i.ApplicationCommandData()
	cmdText := reconstructCommand(data)
	channelID := i.ChannelID

	slog.Debug("discord: slash command", "user", userName, "command", cmdText, "channel", channelID)

	sessionKey := resolveSessionKeyForChannel(channelID, userID, p.shareSessionInChannel, p.threadIsolation, sessionThreadOps{session: p.session})

	msg := &core.Message{
		SessionKey: sessionKey, Platform: "discord",
		MessageID: i.ID,
		UserID:    userID, UserName: userName,
		ChatName: p.resolveChannelName(channelID),
		Content:  cmdText, ReplyCtx: rctx,
	}
	p.handler(p, msg)
}

// replyContextForDeferredInteractionFallback builds a replyContext for slash commands
// when InteractionRespond(defer) failed. Thread channels must set threadID so
// sendChannelReply uses ChannelMessageSend instead of ChannelMessageSendReply with an empty ref.
func replyContextForDeferredInteractionFallback(ch *discordgo.Channel, channelID string) replyContext {
	if ch == nil {
		return replyContext{channelID: channelID}
	}
	switch ch.Type {
	case discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread:
		return replyContext{channelID: channelID, threadID: channelID}
	default:
		return replyContext{channelID: channelID}
	}
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

func (p *Platform) handleComponentInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, userID, userName string) {
	data := i.MessageComponentData()
	if !strings.HasPrefix(data.CustomID, "cmd:") {
		slog.Debug("discord: unknown component interaction", "custom_id", data.CustomID)
		return
	}

	command := strings.TrimPrefix(data.CustomID, "cmd:")
	origText := ""
	if i.Message != nil {
		origText = i.Message.Content
	}
	emptyComponents := []discordgo.MessageComponent{}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    origText + "\n\n> " + command,
			Components: emptyComponents,
		},
	}); err != nil {
		slog.Debug("discord: command component update failed", "error", err)
	}

	channelID := i.ChannelID
	sessionKey := resolveSessionKeyForChannel(channelID, userID, p.shareSessionInChannel, p.threadIsolation, sessionThreadOps{session: p.session})
	p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "discord",
		MessageID:  i.ID,
		UserID:     userID,
		UserName:   userName,
		ChatName:   p.resolveChannelName(channelID),
		Content:    command,
		ReplyCtx:   replyContext{channelID: channelID},
	})
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
	chunks := core.SplitMessageCodeFenceAware(content, maxDiscordLen)
	for _, chunk := range chunks {
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
		var err error
		if rc.useThreadChannel() {
			_, err = p.session.ChannelMessageSend(rc.targetChannelID(), chunk)
		} else {
			ref := &discordgo.MessageReference{MessageID: rc.messageID}
			_, err = p.session.ChannelMessageSendReply(rc.channelID, chunk, ref)
		}
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

func (p *Platform) sendChannel(rc replyContext, content string) error {
	chunks := core.SplitMessageCodeFenceAware(content, maxDiscordLen)
	for _, chunk := range chunks {
		_, err := p.session.ChannelMessageSend(rc.targetChannelID(), chunk)
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
	}
	return nil
}

// SendImage sends an image to the channel or interaction.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	name := img.FileName
	if name == "" {
		name = "image.png"
	}

	newFile := func() *discordgo.File {
		return &discordgo.File{
			Name:        name,
			ContentType: img.MimeType,
			Reader:      bytes.NewReader(img.Data),
		}
	}

	switch rc := rctx.(type) {
	case *interactionReplyCtx:
		rc.mu.Lock()
		first := !rc.firstDone
		if first {
			rc.firstDone = true
		}
		rc.mu.Unlock()

		var err error
		if first {
			_, err = p.session.InteractionResponseEdit(rc.interaction, &discordgo.WebhookEdit{
				Files: []*discordgo.File{newFile()},
			})
		} else {
			_, err = p.session.FollowupMessageCreate(rc.interaction, true, &discordgo.WebhookParams{
				Files: []*discordgo.File{newFile()},
			})
		}
		if err != nil {
			slog.Warn("discord: interaction image failed, falling back to channel message", "error", err)
			_, err = p.session.ChannelMessageSendComplex(rc.channelID, &discordgo.MessageSend{
				Files: []*discordgo.File{newFile()},
			})
			if err != nil {
				return fmt.Errorf("discord: send image fallback: %w", err)
			}
		}
		return nil
	case replyContext:
		_, err := p.session.ChannelMessageSendComplex(rc.targetChannelID(), &discordgo.MessageSend{
			Files: []*discordgo.File{newFile()},
		})
		if err != nil {
			return fmt.Errorf("discord: send image: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("discord: SendImage: invalid reply context type %T", rctx)
	}
}

func buildDiscordActionRows(rows [][]core.ButtonOption) []discordgo.MessageComponent {
	components := make([]discordgo.MessageComponent, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		buttons := make([]discordgo.MessageComponent, 0, len(row))
		for idx, btn := range row {
			style := discordgo.SecondaryButton
			switch idx {
			case 0:
				style = discordgo.SuccessButton
			case 1:
				style = discordgo.DangerButton
			case 2:
				style = discordgo.PrimaryButton
			}
			buttons = append(buttons, discordgo.Button{
				Label:    btn.Text,
				Style:    style,
				CustomID: btn.Data,
			})
		}
		components = append(components, discordgo.ActionsRow{Components: buttons})
	}
	return components
}

func (p *Platform) SendWithButtons(ctx context.Context, rctx any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := rctx.(*interactionReplyCtx)
	if !ok {
		return core.ErrNotSupported
	}
	if len(buttons) == 0 {
		return fmt.Errorf("discord: no buttons provided")
	}
	components := buildDiscordActionRows(buttons)
	if len(components) == 0 {
		return fmt.Errorf("discord: no buttons provided")
	}
	if err := p.sendInteraction(rc, content); err != nil {
		return err
	}
	_, err := p.session.FollowupMessageCreate(rc.interaction, true, &discordgo.WebhookParams{
		Content:    content,
		Components: components,
	})
	if err != nil {
		return fmt.Errorf("discord: send button followup: %w", err)
	}
	return nil
}

var _ core.ImageSender = (*Platform)(nil)
var _ core.InlineButtonSender = (*Platform)(nil)

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// discord:{channelID}:{userID} or discord:{threadID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "discord" {
		return nil, fmt.Errorf("discord: invalid session key %q", sessionKey)
	}
	rc := replyContext{channelID: parts[1]}
	if len(parts) == 2 {
		rc.threadID = parts[1]
	}
	return rc, nil
}

func (p *Platform) ResolveCronReplyTarget(sessionKey string, title string) (string, any, error) {
	if !p.threadIsolation {
		return "", nil, core.ErrNotSupported
	}
	resolvedSessionKey, rc, err := resolveCronReplyTarget(sessionKey, title, sessionThreadOps{session: p.session})
	if err != nil {
		return "", nil, err
	}
	return resolvedSessionKey, rc, nil
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
		channelID = rc.targetChannelID()
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
	if rc.targetChannelID() != "" {
		channelID = rc.targetChannelID()
	}
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

// ResolveChannelName implements core.ChannelNameResolver.
func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	name := p.resolveChannelName(channelID)
	if name == channelID {
		return "", fmt.Errorf("discord: channel name not found for %s", channelID)
	}
	return name, nil
}

func (p *Platform) resolveChannelName(channelID string) string {
	if cached, ok := p.channelNameCache.Load(channelID); ok {
		return cached.(string)
	}
	ch, err := p.session.Channel(channelID)
	if err != nil {
		slog.Debug("discord: resolve channel name failed", "channel", channelID, "error", err)
		return channelID
	}
	name := ch.Name
	if name == "" {
		return channelID
	}
	p.channelNameCache.Store(channelID, name)
	return name
}

func (p *Platform) Stop() error {
	if p.session != nil {
		return p.session.Close()
	}
	return nil
}

// stripDiscordMention removes <@botID> and <@!botID> (nick mention) from text.
func stripDiscordMention(text, botID string) string {
	return stripDiscordMentionWithRole(text, botID, "")
}

func stripDiscordMentionWithRole(text, botID string, botRoleID string) string {
	text = strings.ReplaceAll(text, "<@!"+botID+">", "")
	text = strings.ReplaceAll(text, "<@"+botID+">", "")
	if botRoleID != "" {
		text = strings.ReplaceAll(text, "<@&"+botRoleID+">", "")
	}
	return strings.TrimSpace(text)
}

// stripEveryoneHere removes @everyone and @here from text.
func stripEveryoneHere(text string) string {
	text = strings.ReplaceAll(text, "@everyone", "")
	text = strings.ReplaceAll(text, "@here", "")
	return strings.TrimSpace(text)
}

// isDiscordBotMention checks if the message mentions the bot by user ID or managed role ID.
func isDiscordBotMention(m *discordgo.MessageCreate, botID string, botRoleID string, respondToAtEveryoneAndHere bool) bool {
	if respondToAtEveryoneAndHere && m.MentionEveryone {
		return true
	}
	for _, u := range m.Mentions {
		if u != nil && u.ID == botID {
			return true
		}
	}
	if strings.Contains(m.Content, "<@"+botID+">") || strings.Contains(m.Content, "<@!"+botID+">") {
		return true
	}
	for _, roleID := range m.MentionRoles {
		if roleID == botRoleID && roleID != "" {
			return true
		}
	}
	return botRoleID != "" && strings.Contains(m.Content, "<@&"+botRoleID+">")
}

func (p *Platform) botRoleIDForGuild(guildID string) string {
	if guildID == "" {
		return ""
	}
	v, ok := p.botRoleIDs.Load(guildID)
	if !ok {
		return ""
	}
	roleID, _ := v.(string)
	return roleID
}

func (p *Platform) cacheBotRoleIDForGuild(s *discordgo.Session, guildID string, guildRoles []*discordgo.Role) {
	if s == nil || guildID == "" || p.botID == "" {
		return
	}
	roleID, err := p.resolveBotRoleIDForGuild(s, guildID, guildRoles)
	if err != nil {
		slog.Debug("discord: resolve bot managed role failed", "guild", guildID, "error", err)
		return
	}
	if roleID == "" {
		return
	}
	p.botRoleIDs.Store(guildID, roleID)
}

func (p *Platform) resolveBotRoleIDForGuild(s *discordgo.Session, guildID string, guildRoles []*discordgo.Role) (string, error) {
	member, err := s.GuildMember(guildID, p.botID)
	if err != nil {
		return "", fmt.Errorf("fetch bot member: %w", err)
	}
	if member == nil || len(member.Roles) == 0 {
		return "", nil
	}

	memberRoleSet := make(map[string]struct{}, len(member.Roles))
	for _, roleID := range member.Roles {
		memberRoleSet[roleID] = struct{}{}
	}

	roles := guildRoles
	if len(roles) == 0 {
		roles, err = s.GuildRoles(guildID)
		if err != nil {
			return "", fmt.Errorf("fetch guild roles: %w", err)
		}
	}

	for _, role := range roles {
		if role == nil {
			continue
		}
		if _, ok := memberRoleSet[role.ID]; !ok {
			continue
		}
		if role.Managed {
			return role.ID, nil
		}
	}
	return "", nil
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
