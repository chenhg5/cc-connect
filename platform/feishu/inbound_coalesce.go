package feishu

import (
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type inboundBatch struct {
	messages []*core.Message
	timer    *time.Timer
	sequence uint64
}

// Only plain text and images participate. Commands, permission decisions,
// quoted context and other attachment types preserve immediate dispatch.
func canCoalesceInbound(msg *core.Message) bool {
	return msg.SessionKey != "" && msg.UserID != "" &&
		!msg.IsPermissionResponse && !msg.FromVoice && msg.ModeOverride == "" &&
		msg.ExtraContent == "" && len(msg.Files) == 0 && msg.Audio == nil && msg.Location == nil &&
		!strings.HasPrefix(strings.TrimSpace(msg.Content), "/") &&
		(strings.TrimSpace(msg.Content) != "" || len(msg.Images) > 0)
}

func (p *Platform) coalesceInboundMessage(msg *core.Message) {
	if p.getHandler() == nil || p.isMessageRecalled(msg.MessageID) {
		return
	}
	if !canCoalesceInbound(msg) {
		p.flushInboundSession(msg.SessionKey)
		p.deliverCoreMessage(msg)
		return
	}
	p.inboundCoalesceMu.Lock()
	if p.inboundCoalesceStopped {
		p.inboundCoalesceMu.Unlock()
		p.deliverCoreMessage(msg)
		return
	}
	if p.inboundCoalesce == nil {
		p.inboundCoalesce = make(map[string]*inboundBatch)
	}
	batch := p.inboundCoalesce[msg.SessionKey]
	if batch != nil {
		first := batch.messages[0]
		if first.UserID != msg.UserID || first.ChannelKey != msg.ChannelKey || first.LegacyChannelKey != msg.LegacyChannelKey {
			delete(p.inboundCoalesce, msg.SessionKey)
			batch.timer.Stop()
			p.inboundCoalesceMu.Unlock()
			p.deliverInboundBatch(batch)
			p.coalesceInboundMessage(msg)
			return
		}
	} else {
		batch = &inboundBatch{}
		p.inboundCoalesce[msg.SessionKey] = batch
	}
	if batch.timer != nil {
		batch.timer.Stop()
	}
	copy := *msg
	copy.Images = append([]core.ImageAttachment(nil), msg.Images...)
	batch.messages = append(batch.messages, &copy)
	batch.sequence++
	sequence := batch.sequence
	sessionKey := msg.SessionKey
	batch.timer = time.AfterFunc(p.inboundCoalesceWindow, func() {
		p.inboundDispatchMu.Lock()
		defer p.inboundDispatchMu.Unlock()
		p.inboundCoalesceMu.Lock()
		if p.inboundCoalesce[sessionKey] != batch || batch.sequence != sequence {
			p.inboundCoalesceMu.Unlock()
			return
		}
		delete(p.inboundCoalesce, sessionKey)
		p.inboundCoalesceMu.Unlock()
		p.deliverInboundBatch(batch)
	})
	p.inboundCoalesceMu.Unlock()
}

func (p *Platform) flushInboundSession(session string) {
	p.inboundCoalesceMu.Lock()
	batch := p.inboundCoalesce[session]
	delete(p.inboundCoalesce, session)
	if batch != nil && batch.timer != nil {
		batch.timer.Stop()
	}
	p.inboundCoalesceMu.Unlock()
	if batch != nil {
		p.deliverInboundBatch(batch)
	}
}

func (p *Platform) flushInboundCoalesced() {
	p.inboundDispatchMu.Lock()
	defer p.inboundDispatchMu.Unlock()
	p.inboundCoalesceMu.Lock()
	p.inboundCoalesceStopped = true
	pending := p.inboundCoalesce
	p.inboundCoalesce = nil
	for _, batch := range pending {
		batch.timer.Stop()
	}
	p.inboundCoalesceMu.Unlock()
	for _, batch := range pending {
		p.deliverInboundBatch(batch)
	}
}

func (p *Platform) deliverInboundBatch(batch *inboundBatch) {
	var merged *core.Message
	var callbacks []func()
	for _, msg := range batch.messages {
		if p.isMessageRecalled(msg.MessageID) {
			continue
		}
		if merged == nil {
			copy := *msg
			copy.Images = append([]core.ImageAttachment(nil), msg.Images...)
			merged = &copy
		} else {
			merged.Content = joinFeishuExtraContent(merged.Content, msg.Content)
			merged.Images = append(merged.Images, msg.Images...)
			if msg.UserMessageTimeMs >= merged.UserMessageTimeMs {
				merged.MessageID = msg.MessageID
				merged.UserMessageTimeMs = msg.UserMessageTimeMs
				merged.ReplyCtx = msg.ReplyCtx
			}
		}
		if msg.OnAccepted != nil {
			callbacks = append(callbacks, msg.OnAccepted)
		}
	}
	if merged == nil {
		return
	}
	var once sync.Once
	merged.OnAccepted = func() {
		once.Do(func() {
			for _, callback := range callbacks {
				callback()
			}
		})
	}
	p.deliverCoreMessage(merged)
}
