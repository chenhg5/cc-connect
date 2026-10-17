package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// maomao fork: channel history keeps each attachment (not just a count) so core can hand the
// images of backfilled posts to the agent.
func TestHistoryAttachmentsKeepURLNameType(t *testing.T) {
	got := historyAttachments([]*discordgo.MessageAttachment{
		nil,
		{URL: "https://cdn/x.png", Filename: "x.png", ContentType: "image/png", Width: 4, Height: 3},
		{URL: "https://cdn/a.pdf", Filename: "a.pdf", ContentType: "application/pdf"},
	})
	if len(got) != 2 || got[0].URL != "https://cdn/x.png" || got[0].FileName != "x.png" ||
		got[0].ContentType != "image/png" || got[0].Width != 4 || got[1].FileName != "a.pdf" {
		t.Fatalf("attachments = %+v", got)
	}
}
