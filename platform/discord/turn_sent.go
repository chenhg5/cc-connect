package discord

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// maomao fork: which of this bot's posts carry the agent's own text. Channel
// backfill skips exactly those (the agent has them in its session) and shows
// every other post under this bot's name: cron --exec output and its split
// chunks, relay answers, restart notices, errors, "↪ 已插入" and command
// replies. The engine marks the reply context of each send that delivers
// agent output (core.AgentReplyMarker); everything else goes out unmarked.
// Kept on disk so a restart does not hand the last turn's replies back.

const turnSentPerChannel = 500

type turnSentLog struct {
	mu    sync.Mutex
	path  string
	ids   map[string]struct{}
	order map[string][]string // per channel, oldest first, for eviction
}

func (l *turnSentLog) load(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var order map[string][]string
	if err := json.Unmarshal(raw, &order); err != nil {
		slog.Warn("discord: turn-sent log unreadable", "path", path, "error", err)
		return
	}
	l.order = order
	l.ids = map[string]struct{}{}
	for _, ids := range order {
		for _, id := range ids {
			l.ids[id] = struct{}{}
		}
	}
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
	l.saveLocked()
}

func (l *turnSentLog) saveLocked() {
	if l.path == "" {
		return
	}
	raw, _ := json.Marshal(l.order)
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		err = os.Rename(tmp, l.path)
		if err != nil {
			slog.Warn("discord: save turn-sent log", "error", err)
		}
	}
}

func (l *turnSentLog) has(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.ids[id]
	return ok
}

// noteTurnSent records sent when it carries agent output.
func (p *Platform) noteTurnSent(rc replyContext, sent *discordgo.Message) {
	if rc.agent {
		p.recordAgentPost(sent)
	}
}

func (p *Platform) noteInteractionSent(rc *interactionReplyCtx, sent *discordgo.Message) {
	if rc != nil && rc.agent {
		p.recordAgentPost(sent)
	}
}

func (p *Platform) recordAgentPost(sent *discordgo.Message) {
	if sent == nil || sent.ID == "" {
		return
	}
	p.turnSent.add(sent.ChannelID, sent.ID)
}

// MarkFinalReply implements core.FinalReplyMarker.
func (p *Platform) MarkFinalReply(replyCtx any) any {
	if rc, ok := replyCtx.(replyContext); ok {
		rc.agent, rc.final = true, true
		return rc
	}
	return replyCtx
}

// MarkAgentReply implements core.AgentReplyMarker.
func (p *Platform) MarkAgentReply(replyCtx any) any {
	switch rc := replyCtx.(type) {
	case replyContext:
		rc.agent = true
		return rc
	case *interactionReplyCtx:
		if rc.agent {
			return rc
		}
		root := rc
		if rc.parent != nil {
			root = rc.parent
		}
		return &interactionReplyCtx{interaction: rc.interaction, channelID: rc.channelID, parent: root, agent: true}
	}
	return replyCtx
}
