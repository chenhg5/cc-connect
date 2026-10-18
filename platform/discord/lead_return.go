package discord

import (
	"strings"
	"time"
)

// maomao fork: when the team lead @-s a sub to hand it work, the sub's answer must reach the
// lead even if the model forgets to write <@lead> (2026-10-14: 龙娘 finished a task with
// "交橘验收" and no @, so 橘 only saw it as context). The turn's final answer gets <@lead>
// prepended (interim posts don't, so the lead isn't woken mid-task); the normal bot→bot path
// (burst, gate) does the rest. Known gaps (fall back to the persona's own @): a
// turn whose text all went out as interim segments sends no final post, and a degraded stream
// preview finished by in-place edit never creates a message.

const leadReturnTTL = 24 * time.Hour

func (p *Platform) returnToLead(rc replyContext, content string) string {
	if !rc.final || rc.lead == "" || rc.messageID == "" || strings.TrimSpace(content) == "" {
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

// keepLead is the reply context for a burst of the lead's messages merged into one turn: the
// latest message's, but still marked as lead-dispatched if any part of the burst @-ed this sub.
func keepLead(prev, next any) any {
	n, ok := next.(replyContext)
	if !ok || n.lead != "" {
		return next
	}
	if pr, ok := prev.(replyContext); ok && pr.lead != "" {
		n.lead = pr.lead
	}
	return n
}
