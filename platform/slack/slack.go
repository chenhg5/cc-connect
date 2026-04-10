package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func init() {
	core.RegisterPlatform("slack", New)
}

type replyContext struct {
	channel   string
	timestamp string // thread_ts for threading replies
}

type Platform struct {
	botToken              string
	appToken              string
	allowFrom             string
	shareSessionInChannel bool
	client                *slack.Client
	socket                *socketmode.Client
	handler               core.MessageHandler
	cancel                context.CancelFunc
	channelNameCache      map[string]string
	channelCacheMu        sync.RWMutex
	userNameCache         sync.Map // userID -> display name
}

func New(opts map[string]any) (core.Platform, error) {
	botToken, _ := opts["bot_token"].(string)
	appToken, _ := opts["app_token"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("slack", allowFrom)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	if botToken == "" || appToken == "" {
		return nil, fmt.Errorf("slack: bot_token and app_token are required")
	}
	return &Platform{
		botToken:              botToken,
		appToken:              appToken,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
		channelNameCache:      make(map[string]string),
	}, nil
}

func (p *Platform) Name() string { return "slack" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	p.client = slack.New(p.botToken,
		slack.OptionAppLevelToken(p.appToken),
	)
	p.socket = socketmode.New(p.client)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-p.socket.Events:
				p.handleEvent(evt)
			}
		}
	}()

	go func() {
		if err := p.socket.RunContext(ctx); err != nil {
			slog.Error("slack: socket mode error", "error", err)
		}
	}()

	slog.Info("slack: socket mode connected")
	return nil
}

func (p *Platform) handleEvent(evt socketmode.Event) {
	slog.Debug("slack: raw event received", "type", evt.Type)
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		data, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			slog.Debug("slack: EventsAPI type assertion failed")
			return
		}
		slog.Debug("slack: EventsAPI event", "outer_type", data.Type, "inner_type", data.InnerEvent.Type)
		if evt.Request != nil {
			p.socket.Ack(*evt.Request)
		}

		if data.Type == slackevents.CallbackEvent {
			switch ev := data.InnerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				if ev.BotID != "" || ev.User == "" {
					return
				}

				if ts := ev.TimeStamp; ts != "" {
					if dotIdx := strings.IndexByte(ts, '.'); dotIdx > 0 {
						if sec, err := strconv.ParseInt(ts[:dotIdx], 10, 64); err == nil {
							if core.IsOldMessage(time.Unix(sec, 0)) {
								slog.Debug("slack: ignoring old app_mention after restart", "ts", ts)
								return
							}
						}
					}
				}

				slog.Debug("slack: app_mention received", "user", ev.User, "channel", ev.Channel)

				if !core.AllowList(p.allowFrom, ev.User) {
					slog.Debug("slack: app_mention from unauthorized user", "user", ev.User)
					return
				}

				var sessionKey string
				if p.shareSessionInChannel {
					sessionKey = fmt.Sprintf("slack:%s", ev.Channel)
				} else {
					sessionKey = fmt.Sprintf("slack:%s:%s", ev.Channel, ev.User)
				}

				var shareFiles []slackevents.File
				if cb, ok := data.Data.(*slackevents.EventsAPICallbackEvent); ok {
					shareFiles = parseSlackInnerEventFiles(cb.InnerEvent)
				}
				images, audio, docFiles := p.processSlackFileShares(shareFiles)
				content := stripAppMentionText(ev.Text)
				if content == "" && len(images) == 0 && audio == nil && len(docFiles) == 0 {
					return
				}
				msg := &core.Message{
					SessionKey: sessionKey, Platform: "slack",
					UserID: ev.User, UserName: p.resolveUserName(ev.User),
					ChatName:  p.resolveChannelNameForMsg(ev.Channel),
					Content:   content,
					Images:    images,
					Files:     docFiles,
					Audio:     audio,
					MessageID: ev.TimeStamp,
					ReplyCtx:  replyContext{channel: ev.Channel, timestamp: ev.TimeStamp},
				}
				p.handler(p, msg)

			case *slackevents.MessageEvent:
				if ev.BotID != "" || ev.User == "" {
					return
				}

				if ts := ev.TimeStamp; ts != "" {
					if dotIdx := strings.IndexByte(ts, '.'); dotIdx > 0 {
						if sec, err := strconv.ParseInt(ts[:dotIdx], 10, 64); err == nil {
							if core.IsOldMessage(time.Unix(sec, 0)) {
								slog.Debug("slack: ignoring old message after restart", "ts", ts)
								return
							}
						}
					}
				}

				slog.Debug("slack: message received", "user", ev.User, "channel", ev.Channel)

				if !core.AllowList(p.allowFrom, ev.User) {
					slog.Debug("slack: message from unauthorized user", "user", ev.User)
					return
				}

				var sessionKey string
				if p.shareSessionInChannel {
					sessionKey = fmt.Sprintf("slack:%s", ev.Channel)
				} else {
					sessionKey = fmt.Sprintf("slack:%s:%s", ev.Channel, ev.User)
				}
				ts := ev.TimeStamp

				images, audio, docFiles := p.processSlackFileShares(ev.Files)

				if ev.Text == "" && len(images) == 0 && audio == nil && len(docFiles) == 0 {
					return
				}

				msg := &core.Message{
					SessionKey: sessionKey, Platform: "slack",
					UserID: ev.User, UserName: p.resolveUserName(ev.User),
					ChatName: p.resolveChannelNameForMsg(ev.Channel),
					Content:  ev.Text, Images: images, Files: docFiles, Audio: audio,
					MessageID: ts,
					ReplyCtx:  replyContext{channel: ev.Channel, timestamp: ts},
				}
				p.handler(p, msg)
			}
		}

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slack.SlashCommand)
		if !ok {
			slog.Debug("slack: slash command type assertion failed")
			return
		}
		if evt.Request != nil {
			p.socket.Ack(*evt.Request)
		}

		if !core.AllowList(p.allowFrom, cmd.UserID) {
			slog.Debug("slack: slash command from unauthorized user", "user", cmd.UserID)
			return
		}

		// Convert slash command to a regular message with / prefix so the
		// engine's command handling picks it up.
		cmdName := strings.TrimPrefix(cmd.Command, "/")
		content := "/" + cmdName
		if cmd.Text != "" {
			content += " " + cmd.Text
		}

		var sessionKey string
		if p.shareSessionInChannel {
			sessionKey = fmt.Sprintf("slack:%s", cmd.ChannelID)
		} else {
			sessionKey = fmt.Sprintf("slack:%s:%s", cmd.ChannelID, cmd.UserID)
		}

		msg := &core.Message{
			SessionKey: sessionKey, Platform: "slack",
			UserID: cmd.UserID, UserName: cmd.UserName,
			Content:  content,
			ReplyCtx: replyContext{channel: cmd.ChannelID},
		}
		slog.Debug("slack: slash command", "command", cmd.Command, "text", cmd.Text, "user", cmd.UserID)
		p.handler(p, msg)

	case socketmode.EventTypeConnecting:
		slog.Debug("slack: connecting...")
	case socketmode.EventTypeConnected:
		slog.Info("slack: connected")
	case socketmode.EventTypeConnectionError:
		slog.Error("slack: connection error")
	}
}

func stripAppMentionText(text string) string {
	if idx := strings.Index(text, "> "); idx != -1 && strings.HasPrefix(text, "<@") {
		return strings.TrimSpace(text[idx+2:])
	}
	return text
}

// parseSlackInnerEventFiles extracts the files array from a raw Events API inner
// event. AppMentionEvent is unmarshaled without a Files field in slack-go, but
// Slack still includes "files" in the JSON when a mention is sent with uploads.
func parseSlackInnerEventFiles(raw *json.RawMessage) []slackevents.File {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var wrapper struct {
		Files []slackevents.File `json:"files"`
	}
	if err := json.Unmarshal(*raw, &wrapper); err != nil {
		slog.Debug("slack: parse inner event files", "error", err)
		return nil
	}
	return wrapper.Files
}

// processSlackFileShares downloads Slack file shares and maps them to core
// attachments. Non-audio/non-image types (e.g. PDF, text) become FileAttachment
// so the engine can persist them and pass paths to the agent.
func (p *Platform) processSlackFileShares(files []slackevents.File) (images []core.ImageAttachment, audio *core.AudioAttachment, docFiles []core.FileAttachment) {
	for _, f := range files {
		fileURL := f.URLPrivateDownload
		if fileURL == "" {
			fileURL = f.URLPrivate
		}
		if fileURL == "" {
			slog.Warn("slack: file has no download URL", "file_id", f.ID, "name", f.Name)
			continue
		}

		mt := strings.TrimSpace(strings.ToLower(f.Mimetype))
		switch {
		case strings.HasPrefix(mt, "audio/"):
			data, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download audio failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			format := "mp3"
			if parts := strings.SplitN(mt, "/", 2); len(parts) == 2 {
				format = parts[1]
			}
			audioMime := f.Mimetype
			if audioMime == "" {
				audioMime = mt
			}
			audio = &core.AudioAttachment{
				MimeType: audioMime, Data: data, Format: format,
			}
		case strings.HasPrefix(mt, "image/"):
			imgData, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download image failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			images = append(images, core.ImageAttachment{
				MimeType: f.Mimetype, Data: imgData, FileName: slackFileDisplayName(f),
			})
		default:
			data, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download file failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			if mt == "" {
				mt = "application/octet-stream"
			}
			docFiles = append(docFiles, core.FileAttachment{
				MimeType: mt,
				Data:     data,
				FileName: slackFileDisplayName(f),
			})
		}
	}
	return images, audio, docFiles
}

func slackFileDisplayName(f slackevents.File) string {
	if f.Name != "" {
		return f.Name
	}
	return f.Title
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}

	opts := []slack.MsgOption{
		slack.MsgOptionText(content, false),
	}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionTS(rc.timestamp))
	}

	_, _, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
}

// Send sends a new message (not a reply)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}

	_, _, err := p.client.PostMessageContext(ctx, rc.channel, slack.MsgOptionText(content, false))
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
}

// SendImage uploads and sends an image to the channel.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: SendImage: invalid reply context type %T", rctx)
	}

	name := img.FileName
	if name == "" {
		name = "image.png"
	}

	_, err := p.client.UploadFileV2Context(ctx, slack.UploadFileV2Parameters{
		Reader:          bytes.NewReader(img.Data),
		FileSize:        len(img.Data),
		Filename:        name,
		Channel:         rc.channel,
		ThreadTimestamp: rc.timestamp,
	})
	if err != nil {
		return fmt.Errorf("slack: send image: %w", err)
	}
	return nil
}

var _ core.ImageSender = (*Platform)(nil)

// SendFile uploads and sends a generic file to the channel.
// Implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: SendFile: invalid reply context type %T", rctx)
	}

	name := file.FileName
	if name == "" {
		name = "attachment"
	}

	_, err := p.client.UploadFileV2Context(ctx, slack.UploadFileV2Parameters{
		Reader:          bytes.NewReader(file.Data),
		FileSize:        len(file.Data),
		Filename:        name,
		Channel:         rc.channel,
		ThreadTimestamp: rc.timestamp,
	})
	if err != nil {
		return fmt.Errorf("slack: send file: %w", err)
	}
	return nil
}

var _ core.FileSender = (*Platform)(nil)

func (p *Platform) downloadSlackFile(url string) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("empty URL")
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", core.RedactToken(err.Error(), p.botToken))
	}
	defer resp.Body.Close()

	// Check if we got an unexpected status code (e.g., redirect to login page)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Basic sanity check: detect if we received HTML instead of binary data
	if len(data) > 0 && (bytes.HasPrefix(data, []byte("<!DOCTYPE")) || bytes.HasPrefix(data, []byte("<html"))) {
		return nil, fmt.Errorf("received HTML response (likely missing auth); first 100 bytes: %s", string(data[:min(100, len(data))]))
	}

	return data, nil
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// slack:{channel}:{user}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "slack" {
		return nil, fmt.Errorf("slack: invalid session key %q", sessionKey)
	}
	return replyContext{channel: parts[1]}, nil
}

func (p *Platform) resolveUserName(userID string) string {
	if cached, ok := p.userNameCache.Load(userID); ok {
		return cached.(string)
	}
	user, err := p.client.GetUserInfo(userID)
	if err != nil {
		slog.Debug("slack: resolve user name failed", "user", userID, "error", err)
		return userID
	}
	name := user.RealName
	if name == "" {
		name = user.Profile.DisplayName
	}
	if name == "" {
		name = userID
	}
	p.userNameCache.Store(userID, name)
	return name
}

func (p *Platform) resolveChannelNameForMsg(channelID string) string {
	name, err := p.ResolveChannelName(channelID)
	if err != nil || name == "" {
		return channelID
	}
	return name
}

func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	p.channelCacheMu.RLock()
	if name, ok := p.channelNameCache[channelID]; ok {
		p.channelCacheMu.RUnlock()
		return name, nil
	}
	p.channelCacheMu.RUnlock()

	info, err := p.client.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return "", fmt.Errorf("slack: resolve channel name for %s: %w", channelID, err)
	}

	p.channelCacheMu.Lock()
	p.channelNameCache[channelID] = info.Name
	p.channelCacheMu.Unlock()

	return info.Name, nil
}

// FormattingInstructions returns Slack mrkdwn formatting guidance for the agent.
func (p *Platform) FormattingInstructions() string {
	return `You are responding in Slack. Use Slack's mrkdwn format, NOT standard Markdown:
- Bold: *text* (single asterisks, not double)
- Italic: _text_
- Strikethrough: ~text~
- Code: ` + "`text`" + `
- Code block: ` + "```text```" + `
- Blockquote: > text
- Lists: use bullet (•) or numbered lists normally
- Links: <url|display text>
- Do NOT use ## headings — Slack does not render them. Use *bold text* on its own line instead.`
}

// StartTyping adds emoji reactions to the user's message as a heartbeat
// indicator so the user knows the bot is still working.
//
// Timeline:
//   - Immediately: eyes
//   - After 2 minutes: clock
//   - Every 5 minutes after that: one more emoji (sequential from extras list)
//
// All reactions are removed when the returned stop function is called.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.channel == "" || rc.timestamp == "" {
		return func() {}
	}

	ref := slack.ItemRef{Channel: rc.channel, Timestamp: rc.timestamp}
	var mu sync.Mutex
	var added []string

	addReaction := func(emoji string) {
		if err := p.client.AddReaction(emoji, ref); err != nil {
			slog.Debug("slack: add reaction failed", "emoji", emoji, "error", err)
			return
		}
		mu.Lock()
		added = append(added, emoji)
		mu.Unlock()
	}

	// Immediately add eyes
	addReaction("eyes")

	extras := []string{
		"hourglass_flowing_sand", "hourglass", "gear", "hammer_and_wrench",
		"mag", "bulb", "rocket", "zap", "fire", "sparkles",
		"brain", "crystal_ball", "jigsaw", "microscope", "satellite",
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		// After 2 minutes, add clock
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		select {
		case <-timer.C:
			addReaction("clock1")
		case <-done:
			return
		}

		// Every 5 minutes, add a random extra emoji
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		idx := 0
		for {
			select {
			case <-ticker.C:
				if idx < len(extras) {
					addReaction(extras[idx])
					idx++
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
		mu.Lock()
		emojis := make([]string, len(added))
		copy(emojis, added)
		mu.Unlock()
		for _, emoji := range emojis {
			if err := p.client.RemoveReaction(emoji, ref); err != nil {
				slog.Debug("slack: remove reaction failed", "emoji", emoji, "error", err)
			}
		}
	}
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
