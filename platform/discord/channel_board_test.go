package discord

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

func TestChannelBoardEmbed_Render(t *testing.T) {
	start := time.Unix(1792500000, 0)
	b := core.ChannelBoardPayload{
		Lang: "zh",
		Running: []core.BoardRunning{
			{Bot: "橘", Desc: "帮我看一下，现在感觉我们的任务有点乱", StartedAt: start,
				Step: &core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "go test ./core/"},
				Subs: []core.SubTask{{ID: "a1", Description: "查 Discord 限流文档", Background: true, Status: core.SubTaskRunning, StartedAt: start, Step: "WebFetch docs.discord.com"}}},
			{Bot: "龙娘", Desc: "【橘】@龙娘 写 diff 审查", StartedAt: start.Add(time.Minute)},
		},
		Queued: []core.BoardQueued{
			{Bot: "鲸鱼娘", Kind: "message", Desc: "润色一下日报"},
			{Bot: "橘", Kind: "cron", Desc: "每天早报", At: start.Add(8 * time.Hour)},
		},
		Done: []core.BoardDone{
			{Bot: "彩虹猫", Desc: "查梗", EndedAt: start.Add(-time.Hour), Outcome: core.BoardDoneCompleted},
			{Bot: "龙娘", Desc: "跑回测", EndedAt: start.Add(-2 * time.Hour), Outcome: core.BoardDoneFailed},
			{Bot: "橘", Desc: "重启前那件", EndedAt: start.Add(-3 * time.Hour), Outcome: core.BoardDoneStopped},
		},
	}
	embed, err := channelBoardEmbed(core.BuildChannelBoardPayload(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("title=%s\n%s\nfooter=%s", embed.Title, embed.Description, embed.Footer.Text)
	want := []string{
		"**在做的**",
		fmt.Sprintf("🔄 橘 · 帮我看一下，现在感觉我们的任务有点乱 · 开始于 <t:%d:R>", start.Unix()),
		"　└ 🔧 Bash — go test ./core/",
		"　🔄 后台 · 查 Discord 限流文档",
		"🔄 龙娘 · 【橘】@龙娘 写 diff 审查",
		"**排着队的**",
		"💬 鲸鱼娘 · 润色一下日报",
		fmt.Sprintf("⏰ 橘 · 每天早报 · <t:%d:R>", start.Add(8*time.Hour).Unix()),
		"**今天做完的**",
		fmt.Sprintf("✅ 彩虹猫 · 查梗 · <t:%d:t>", start.Add(-time.Hour).Unix()),
		"❌ 龙娘 · 跑回测",
		"⏹️ 橘 · 重启前那件",
	}
	for _, w := range want {
		if !strings.Contains(embed.Description, w) {
			t.Errorf("missing %q in\n%s", w, embed.Description)
		}
	}
	if embed.Title != "频道任务看板" || embed.Footer == nil || embed.Footer.Text != channelBoardMarker || embed.Color != 0x5865F2 {
		t.Fatalf("title/footer/color = %q %+v %x", embed.Title, embed.Footer, embed.Color)
	}

	empty, _ := channelBoardEmbed(core.BuildChannelBoardPayload(core.ChannelBoardPayload{Lang: "zh"}))
	if strings.Count(empty.Description, "（无）") != 3 || empty.Color != 0x57F287 {
		t.Fatalf("empty board = %q", empty.Description)
	}
	if _, err := channelBoardEmbed("plain text"); err == nil {
		t.Fatal("non-payload accepted")
	}
}

func TestChannelBoard_UnknownMessageAndFind(t *testing.T) {
	rest := &discordgo.RESTError{Message: &discordgo.APIErrorMessage{Code: discordgo.ErrCodeUnknownMessage}}
	if !isDiscordUnknownMessage(fmt.Errorf("wrap: %w", rest)) {
		t.Fatal("10008 not recognised")
	}
	if isDiscordUnknownMessage(&discordgo.RESTError{Message: &discordgo.APIErrorMessage{Code: discordgo.ErrCodeMissingPermissions}}) || isDiscordUnknownMessage(errors.New("x")) {
		t.Fatal("other errors treated as gone")
	}
	p := &Platform{botID: "me"}
	board := &discordgo.MessageEmbed{Footer: &discordgo.MessageEmbedFooter{Text: channelBoardMarker}}
	msgs := []*discordgo.Message{
		{ID: "1", Author: &discordgo.User{ID: "other"}, Embeds: []*discordgo.MessageEmbed{board}},
		{ID: "2", Author: &discordgo.User{ID: "me"}, Embeds: []*discordgo.MessageEmbed{{Title: "进度"}}},
		{ID: "3", Author: &discordgo.User{ID: "me"}, Embeds: []*discordgo.MessageEmbed{board}},
	}
	if got := p.findBoardIn(msgs); got != "3" {
		t.Fatalf("found %q, want this bot's board 3", got)
	}
}

// Not connected: every board call errors instead of panicking (the core then
// logs and retries on the next change).
func TestChannelBoard_NotConnected(t *testing.T) {
	p := &Platform{}
	content := core.BuildChannelBoardPayload(core.ChannelBoardPayload{})
	if _, err := p.PostChannelBoard(t.Context(), "c", content); err == nil {
		t.Fatal("post without session")
	}
	if err := p.EditChannelBoard(t.Context(), "c", "m", content); err == nil {
		t.Fatal("edit without session")
	}
	if err := p.PinChannelBoard(t.Context(), "c", "m"); err == nil {
		t.Fatal("pin without session")
	}
	if _, _, err := p.FindChannelBoard(t.Context(), "c"); err == nil {
		t.Fatal("find without session")
	}
}

// The board calls read the session under p.mu (Stop and reconnects replace it).
func TestChannelBoard_SessionReadUnderLock(t *testing.T) {
	p := &Platform{}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.mu.Lock()
			p.session = nil
			p.mu.Unlock()
		}
	}()
	content := core.BuildChannelBoardPayload(core.ChannelBoardPayload{})
	for i := 0; i < 200; i++ {
		_, _ = p.PostChannelBoard(t.Context(), "c", content)
		_ = p.EditChannelBoard(t.Context(), "c", "m", content)
		_ = p.PinChannelBoard(t.Context(), "c", "m")
		_, _, _ = p.FindChannelBoard(t.Context(), "c")
	}
	close(stop)
}
