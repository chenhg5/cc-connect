package discord

import (
	"sync"

	"github.com/bwmarrin/discordgo"
)

// maomao fork: which of this bot's posts came out of a turn the agent ran for a
// channel message. Channel backfill skips exactly those (the agent wrote them
// and has them in its session) and shows every other post under this bot's
// name: cron --exec output, split chunks of it, relay answers, restart notices.
// A turn for a channel message replies with a replyContext carrying that
// message's ID; cron, relay and API sends build one without it.

const turnSentPerChannel = 500

type turnSentLog struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order map[string][]string // per channel, oldest first, for eviction
}

func (l *turnSentLog) add(channelID, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = map[string]struct{}{}
		l.order = map[string][]string{}
	}
	if _, ok := l.ids[id]; ok {
		return
	}
	l.ids[id] = struct{}{}
	o := append(l.order[channelID], id)
	if len(o) > turnSentPerChannel {
		delete(l.ids, o[0])
		o = o[1:]
	}
	l.order[channelID] = o
}

func (l *turnSentLog) has(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.ids[id]
	return ok
}

// noteTurnSent records sent when rc belongs to a turn for a channel message.
func (p *Platform) noteTurnSent(rc replyContext, sent *discordgo.Message) {
	if rc.messageID == "" || sent == nil || sent.ID == "" {
		return
	}
	p.turnSent.add(sent.ChannelID, sent.ID)
}
