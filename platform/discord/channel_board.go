package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

// maomao fork: the channel task board ("频道任务看板") — one pinned message per
// channel, posted and edited by the project with channel_board = true. See
// core/channel_board.go.
//
// Taken over from conest PR #162 (bridge/platform/discord/task_card.go):
// mentions are disabled on every send/edit, and a board whose stored ID was lost
// (or whose post response never came back) is found again by a marker in the
// embed footer before a new one is posted.

// channelBoardMarker is the footer text that marks this bot's board message.
const channelBoardMarker = "cc-board"

var _ core.ChannelBoardPoster = (*Platform)(nil)

// boardSession reads the session under p.mu: Stop() and reconnects replace it
// (same as StartTyping).
func (p *Platform) boardSession() *discordgo.Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.session
}

func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func channelBoardEmbed(content string) (*discordgo.MessageEmbed, error) {
	payload, ok := core.ParseChannelBoardPayload(content)
	if !ok {
		return nil, fmt.Errorf("discord: not a channel board payload")
	}
	return buildDiscordChannelBoardEmbed(payload), nil
}

// PostChannelBoard implements core.ChannelBoardPoster.
func (p *Platform) PostChannelBoard(ctx context.Context, channelID, content string) (string, error) {
	s := p.boardSession()
	if s == nil {
		return "", fmt.Errorf("discord: not connected")
	}
	embed, err := channelBoardEmbed(content)
	if err != nil {
		return "", err
	}
	m, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: noMentions(),
	}, discordgo.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("discord: post channel board: %w", err)
	}
	return m.ID, nil
}

// EditChannelBoard implements core.ChannelBoardPoster. A deleted message is
// reported as core.ErrChannelBoardGone.
func (p *Platform) EditChannelBoard(ctx context.Context, channelID, messageID, content string) error {
	s := p.boardSession()
	if s == nil {
		return fmt.Errorf("discord: not connected")
	}
	embed, err := channelBoardEmbed(content)
	if err != nil {
		return err
	}
	edit := discordgo.NewMessageEdit(channelID, messageID)
	edit.SetContent("")
	edit.SetEmbeds([]*discordgo.MessageEmbed{embed})
	edit.AllowedMentions = noMentions()
	if _, err := s.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
		if isDiscordUnknownMessage(err) {
			return fmt.Errorf("%w: %v", core.ErrChannelBoardGone, err)
		}
		return fmt.Errorf("discord: edit channel board: %w", err)
	}
	return nil
}

// PinChannelBoard implements core.ChannelBoardPoster (needs Manage Messages /
// Pin Messages in the channel).
func (p *Platform) PinChannelBoard(ctx context.Context, channelID, messageID string) error {
	s := p.boardSession()
	if s == nil {
		return fmt.Errorf("discord: not connected")
	}
	// The current endpoint (docs.discord.com/developers/resources/message, "Pin
	// Message": PUT /channels/{id}/messages/pins/{id}); discordgo v0.29's
	// ChannelMessagePin uses the deprecated /channels/{id}/pins/{id}, kept as the
	// fallback.
	url := discordgo.EndpointChannelMessages(channelID) + "/pins/" + messageID
	_, err := s.RequestWithBucketID("PUT", url, nil, discordgo.EndpointChannelMessages(channelID)+"/pins/", discordgo.WithContext(ctx))
	if err == nil {
		return nil
	}
	if err2 := s.ChannelMessagePin(channelID, messageID, discordgo.WithContext(ctx)); err2 != nil {
		return fmt.Errorf("discord: pin channel board: %w (deprecated endpoint: %v)", err, err2)
	}
	return nil
}

// FindChannelBoard implements core.ChannelBoardPoster: this bot's message with
// the board marker, among the pinned messages, then the latest page of the
// channel (one request each).
func (p *Platform) FindChannelBoard(ctx context.Context, channelID string) (string, bool, error) {
	s := p.boardSession()
	if s == nil {
		return "", false, fmt.Errorf("discord: not connected")
	}
	// A failed pin list is not fatal: the latest messages are still looked at.
	if pinned, err := s.ChannelMessagesPinned(channelID, discordgo.WithContext(ctx)); err == nil {
		if id := p.findBoardIn(pinned); id != "" {
			return id, true, nil
		}
	}
	// 来源: Discord docs (docs.discord.com/developers/resources/message), Get Channel
	// Messages: limit default 50, max 100 — 沿用 the default page.
	recent, err := s.ChannelMessages(channelID, 50, "", "", "", discordgo.WithContext(ctx))
	if err != nil {
		return "", false, fmt.Errorf("discord: list messages: %w", err)
	}
	return p.findBoardIn(recent), false, nil
}

func (p *Platform) findBoardIn(msgs []*discordgo.Message) string {
	for _, m := range msgs {
		if m == nil || m.Author == nil || m.Author.ID != p.botID {
			continue
		}
		for _, e := range m.Embeds {
			if e != nil && e.Footer != nil && e.Footer.Text == channelBoardMarker {
				return m.ID
			}
		}
	}
	return ""
}

func isDiscordUnknownMessage(err error) bool {
	var rest *discordgo.RESTError
	return errors.As(err, &rest) && rest.Message != nil && rest.Message.Code == discordgo.ErrCodeUnknownMessage
}

// buildDiscordChannelBoardEmbed renders the board:
//
//	**在做的**
//	🔄 橘 · <在做什么> · 开始于 <t:…:R>
//	　└ 🔧 Bash — go test ./core/
//	　🔄 后台 · <子 agent 描述> · 开始于 <t:…:R>
//	　　└ <子 agent 当前那一步>
//	**排着队的**
//	💬 龙娘 · <排队的消息>
//	⏰ 橘 · <定时任务描述> · <t:…:R>
//	**今天做完的**
//	✅ 鲸鱼娘 · <做了什么> · <t:…:t>
func buildDiscordChannelBoardEmbed(b *core.ChannelBoardPayload) *discordgo.MessageEmbed {
	zh := normalizeDiscordProgressLang(b.Lang) == "zh" || normalizeDiscordProgressLang(b.Lang) == "zh-tw"
	t := func(zhText, en string) string {
		if zh {
			return zhText
		}
		return en
	}
	var lines []string
	section := func(title string, rows []string) {
		lines = append(lines, "**"+title+"**")
		if len(rows) == 0 {
			rows = []string{t("（无）", "(none)")}
		}
		lines = append(lines, rows...)
	}
	short := func(s string) string {
		return trimDiscordRunes(compactDiscordProgressText(s), maxDiscordProgressLineLen)
	}

	var running []string
	for _, r := range b.Running {
		head := "🔄 " + r.Bot
		if r.Desc != "" {
			head += " · " + short(r.Desc)
		} else if r.StartedAt.IsZero() {
			head += " · " + t("后台子 agent", "background sub-agents")
		}
		if !r.StartedAt.IsZero() {
			head += " · " + fmt.Sprintf(t("开始于 <t:%d:R>", "started <t:%d:R>"), r.StartedAt.Unix())
		}
		running = append(running, head)
		if r.Step != nil {
			if line := buildDiscordProgressLine(*r.Step, b.Lang); line != "" {
				running = append(running, "　└ "+line)
			}
		}
		for _, s := range r.Subs {
			for _, l := range strings.Split(buildDiscordSubTaskRow(s, zh), "\n") {
				running = append(running, "　"+l)
			}
		}
	}
	section(t("在做的", "In progress"), running)

	var queued []string
	for _, q := range b.Queued {
		icon := "💬"
		if q.Kind == "cron" || q.Kind == "timer" {
			icon = "⏰"
		}
		row := icon + " " + q.Bot
		if q.Desc != "" {
			row += " · " + short(q.Desc)
		}
		if !q.At.IsZero() {
			row += fmt.Sprintf(" · <t:%d:R>", q.At.Unix())
		}
		queued = append(queued, row)
	}
	section(t("排着队的", "Queued"), queued)

	var done []string
	for _, d := range b.Done {
		icon := "✅"
		switch d.Outcome {
		case core.BoardDoneFailed:
			icon = "❌"
		case core.BoardDoneStopped:
			icon = "⏹️"
		}
		row := icon + " " + d.Bot
		if d.Desc != "" {
			row += " · " + short(d.Desc)
		}
		row += fmt.Sprintf(" · <t:%d:t>", d.EndedAt.Unix())
		done = append(done, row)
	}
	section(t("今天做完的", "Done today"), done)

	// Same colors as the progress card: working / idle.
	color := 0x57F287
	if len(b.Running) > 0 {
		color = 0x5865F2
	}
	return &discordgo.MessageEmbed{
		Title:       t("频道任务看板", "Channel tasks"),
		Description: trimDiscordRunes(strings.Join(lines, "\n"), maxDiscordProgressDescLen),
		Color:       color,
		Footer:      &discordgo.MessageEmbedFooter{Text: channelBoardMarker},
	}
}
