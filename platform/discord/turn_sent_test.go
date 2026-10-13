package discord

import (
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestNoteTurnSentOnlyForChannelMessageTurns(t *testing.T) {
	p := &Platform{}
	p.noteTurnSent(replyContext{channelID: "c", messageID: "m1"}, &discordgo.Message{ID: "r1", ChannelID: "c"})
	p.noteTurnSent(replyContext{channelID: "c"}, &discordgo.Message{ID: "cron1", ChannelID: "c"}) // cron/relay ctx
	if !p.turnSent.has("r1") {
		t.Fatal("a reply in a turn must be recorded")
	}
	if p.turnSent.has("cron1") {
		t.Fatal("a post without a triggering message (cron, relay) must not be recorded")
	}
}

func TestTurnSentLogEvictsOldest(t *testing.T) {
	var l turnSentLog
	for i := 0; i <= turnSentPerChannel; i++ {
		l.add("c", fmt.Sprint(i))
	}
	if l.has("0") || !l.has(fmt.Sprint(turnSentPerChannel)) {
		t.Fatal("oldest id should be evicted at the cap")
	}
}
