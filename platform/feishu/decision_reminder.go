package feishu

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A reminder is created lazily with its actual request, not an empty card.
// A normal (non-thread) quoted reply keeps the source-task entry visible in
// the group; callbacks retain the task key.
type decisionReminder struct {
	mu           sync.Mutex
	p            *Platform
	card         *queryCard
	sent, closed bool
	user, token  string
}

func (p *Platform) CreateDecisionReminder(ctx context.Context, chat, key, user string) (core.InteractiveStreamingCard, error) {
	if chat == "" || key == "" || user == "" {
		return nil, fmt.Errorf("decision reminder requires chat, session and recipient")
	}
	return &decisionReminder{p: p, user: user, card: &queryCard{p: p, rc: replyContext{chatID: chat, sessionKey: key, messageID: splitTaskSession(key), forceChatReply: true}, id: rand.Text(), created: time.Now(), unpaged: true, status: core.CardStatusWorking, native: true, blocks: []core.ExecutionBlock{{Text: p.MentionText(user), Done: true}}}}, nil
}
func (c *decisionReminder) Failed() bool                               { return false }
func (c *decisionReminder) Update(ctx context.Context, s string) error { return c.SetNotice(ctx, s) }
func (c *decisionReminder) Finalize(ctx context.Context, s string) error {
	return c.Finish(ctx, s, core.CardStatusDone)
}
func (c *decisionReminder) Finish(ctx context.Context, s string, status core.CardStatus) error {
	if err := c.SetNotice(ctx, s); err != nil {
		return err
	}
	return c.SetPrompt(ctx, nil)
}
func (c *decisionReminder) SetPrompt(ctx context.Context, prompt *core.Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if prompt != nil {
		c.card.prompt = prompt
		c.token = prompt.DecisionToken
		if c.token == "" {
			for _, element := range prompt.Elements {
				if row, ok := element.(core.CardActions); ok {
					for _, b := range row.Buttons {
						if token := b.Extra["permission_token"]; token != "" {
							c.token = token
							break
						}
					}
				}
			}
		}
	} else {
		if c.card.prompt != nil {
			copy := *c.card.prompt
			copy.DecisionToken = ""
			copy.Header = nil
			copy.Elements = nil
			for _, element := range c.card.prompt.Elements {
				if _, button := element.(core.CardActions); !button {
					copy.Elements = append(copy.Elements, element)
				}
			}
			c.card.prompt = &copy
		}
		c.card.done = true
		c.card.status = core.CardStatusDone
	}
	err := c.write(ctx)
	if err == nil && prompt == nil {
		c.closed = true
	}
	return err
}
func (c *decisionReminder) SetNotice(ctx context.Context, s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.card.notice = s
	if !c.sent {
		return nil
	}
	return c.write(ctx)
}
func (c *decisionReminder) write(ctx context.Context) error {
	if !c.sent {
		_, err := c.p.sendQueryCard(ctx, c.card)
		if err == nil {
			c.sent = true
			c.rememberReply()
		}
		return err
	}
	c.card.mu.Lock()
	defer c.card.mu.Unlock()
	err := c.card.writeLocked(ctx)
	if err == nil {
		c.rememberReply()
	}
	return err
}

func questionInput(token, key, label, submit string) map[string]any {
	return map[string]any{"tag": "form", "name": "decision_answer", "elements": []map[string]any{
		{"tag": "input", "name": "answer", "label": plainText(label), "required": true, "input_type": "multiline_text", "rows": 3, "width": "fill"},
		{"tag": "button", "name": "cc_question_submit", "form_action_type": "submit", "type": "primary", "text": plainText(submit), "behaviors": []map[string]any{{"type": "callback", "value": map[string]string{"action": "question_submit", "session_key": key, "query_card": "true", "permission_token": token}}}},
	}}
}

var _ core.DecisionReminderPlatform = (*Platform)(nil)

type decisionReply struct {
	chat, session, user, token string
	foreign                    bool
	created                    time.Time
}

func (c *decisionReminder) rememberReply() {
	h, ok := c.card.handle.(*feishuPreviewHandle)
	if !ok {
		return
	}
	h.mu.Lock()
	id := h.messageID
	h.mu.Unlock()
	c.p.decisionReplies.Range(func(key, value any) bool {
		if time.Since(value.(decisionReply).created) > 24*time.Hour {
			c.p.decisionReplies.Delete(key)
		}
		return true
	})
	if c.p.registerDecisionReply != nil {
		c.p.registerDecisionReply(id, core.DecisionReplyRoute{ChatID: c.card.rc.chatID, SessionKey: c.card.rc.sessionKey, RequesterID: c.user, Token: c.token})
	}
	c.p.decisionReplies.Store(id, decisionReply{chat: c.card.rc.chatID, session: c.card.rc.sessionKey, user: c.user, token: c.token, created: time.Now()})
}
func (p *Platform) findDecisionReply(chat, parent, root string) (decisionReply, bool) {
	for _, id := range []string{parent, root} {
		if p.lookupDecisionReply != nil {
			if r, ok := p.lookupDecisionReply(chat, id); ok {
				return decisionReply{chat: r.ChatID, session: r.SessionKey, user: r.RequesterID, token: r.Token, foreign: !r.Owned}, true
			}
		}
		if value, ok := p.decisionReplies.Load(id); ok {
			r := value.(decisionReply)
			if r.chat == chat {
				return r, true
			}
		}
	}
	return decisionReply{}, false
}

func (p *Platform) SetDecisionReplyRegistry(put func(string, core.DecisionReplyRoute), get func(string, string) (core.DecisionReplyRoute, bool)) {
	p.registerDecisionReply = put
	p.lookupDecisionReply = get
}
