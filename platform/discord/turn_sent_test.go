package discord

import (
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestOnlyAgentOutputIsRecorded(t *testing.T) {
	p := &Platform{}
	turn := replyContext{channelID: "c", messageID: "m1"} // reply context of a turn
	p.noteTurnSent(turn, &discordgo.Message{ID: "notice", ChannelID: "c"})
	agent := p.MarkAgentReply(turn).(replyContext)
	p.noteTurnSent(agent, &discordgo.Message{ID: "reply", ChannelID: "c"})
	cron := p.MarkAgentReply(replyContext{channelID: "c"}).(replyContext) // cron --agent turn output
	p.noteTurnSent(cron, &discordgo.Message{ID: "cronreply", ChannelID: "c"})
	p.noteTurnSent(replyContext{channelID: "c"}, &discordgo.Message{ID: "execout", ChannelID: "c"})
	if !p.turnSent.has("reply") || !p.turnSent.has("cronreply") {
		t.Fatal("agent output must be recorded")
	}
	if p.turnSent.has("notice") || p.turnSent.has("execout") {
		t.Fatal("status lines and cron --exec output must not be recorded")
	}
}

func TestMarkedInteractionSharesFirstReplyState(t *testing.T) {
	p := &Platform{}
	rc := &interactionReplyCtx{interaction: &discordgo.Interaction{}, channelID: "c"}
	if !rc.takeFirst() {
		t.Fatal("first reply")
	}
	agent := p.MarkAgentReply(rc).(*interactionReplyCtx)
	if agent.takeFirst() {
		t.Fatal("the marked copy must see that the first reply already went out")
	}
	p.noteInteractionSent(agent, &discordgo.Message{ID: "r", ChannelID: "c"})
	p.noteInteractionSent(rc, &discordgo.Message{ID: "n", ChannelID: "c"})
	if !p.turnSent.has("r") || p.turnSent.has("n") {
		t.Fatal("only the marked interaction send is agent output")
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

func TestTurnSentSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/ts.json"
	p := &Platform{}
	p.turnSent.load(path)
	p.noteTurnSent(p.MarkAgentReply(replyContext{channelID: "c"}).(replyContext), &discordgo.Message{ID: "r9", ChannelID: "c"})
	q := &Platform{}
	q.turnSent.load(path)
	if !q.turnSent.has("r9") {
		t.Fatal("ids must survive a restart")
	}
}
