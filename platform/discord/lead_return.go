package discord

import (
	"strings"
	"time"
)

// maomao fork: when the team lead @-s a sub to hand it work, the sub's answer must reach the
// lead even if the model forgets to write <@lead> (2026-10-14: 龙娘 finished a task with
// "交橘验收" and no @, so 橘 only saw it as context). The first agent post of that turn gets
// <@lead> prepended; the normal bot→bot path (burst, handoff fuse, gate) does the rest.

const leadReturnTTL = 24 * time.Hour

func (p *Platform) returnToLead(rc replyContext, content string) string {
	if !rc.agent || rc.lead == "" || rc.messageID == "" || strings.TrimSpace(content) == "" {
		return content
	}
	now := time.Now()
	if _, seen := p.leadReturned.LoadOrStore(rc.messageID, now); seen {
		return content
	}
	p.leadReturned.Range(func(k, v any) bool {
		if t, ok := v.(time.Time); ok && now.Sub(t) > leadReturnTTL {
			p.leadReturned.Delete(k)
		}
		return true
	})
	if strings.Contains(content, "<@"+rc.lead+">") || strings.Contains(content, "<@!"+rc.lead+">") {
		return content
	}
	return "<@" + rc.lead + "> " + content
}
