package feishu

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// feishuIncoming owns the event fields needed after the SDK callback returns.
type feishuIncoming struct {
	ctx                                   context.Context
	msgType, content                      string
	mentions                              []*larkim.MentionEvent
	messageID, sessionKey, userID, chatID string
	rctx                                  replyContext
	parentID                              string
	createTimeMs                          int64
	history                               groupHistoryContext
}

func (m feishuIncoming) dispatch(p *Platform, deliver func(*core.Message)) {
	p.dispatchMessageWithHistoryTo(m.ctx, m.msgType, m.content, m.mentions,
		m.messageID, m.sessionKey, m.userID, m.chatID, m.rctx, m.parentID,
		m.createTimeMs, m.history, deliver)
}

type pendingForwardMerge struct {
	forward  feishuIncoming
	deadline time.Time
	ready    chan struct{}
	reply    *feishuIncoming // protected by forwardMergeMu
}

func (p *Platform) canMergeForwardReply(batch *pendingForwardMerge, msg feishuIncoming) bool {
	if !time.Now().Before(batch.deadline) || msg.msgType != "text" ||
		msg.userID == "" || msg.userID != batch.forward.userID ||
		msg.chatID != batch.forward.chatID || msg.parentID != batch.forward.messageID ||
		p.isMessageRecalled(batch.forward.messageID) || p.isMessageRecalled(msg.messageID) {
		return false
	}
	if msg.createTimeMs > 0 && batch.forward.createTimeMs > msg.createTimeMs {
		return false
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(msg.content), &body) != nil {
		return false
	}
	text := strings.TrimSpace(stripMentions(body.Text, msg.mentions, p.getBotOpenID()))
	// Commands must retain their normal routing and cannot consume a forward.
	return text != "" && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!")
}

// Reserve the lane and merge candidate synchronously. Only adjacent admitted
// messages can merge: moving a later reply ahead of intervening input would
// advance the engine watermark and could discard that input.
func (p *Platform) enqueueFeishuMessage(msg feishuIncoming) {
	p.forwardMergeMu.Lock()
	defer p.forwardMergeMu.Unlock()
	if batch := p.forwardMergePending[msg.sessionKey]; batch != nil {
		delete(p.forwardMergePending, msg.sessionKey)
		if p.canMergeForwardReply(batch, msg) {
			batch.reply = &msg
			close(batch.ready)
			return
		}
		// An unrelated message is a boundary, not an instruction for this A.
		close(batch.ready)
	}
	if msg.msgType != "merge_forward" || p.forwardMergeWindow <= 0 {
		p.enqueueMessageDispatch(msg.sessionKey, func() { msg.dispatch(p, p.dispatchCoreMessage) })
		return
	}
	batch := &pendingForwardMerge{
		forward: msg, deadline: time.Now().Add(p.forwardMergeWindow), ready: make(chan struct{}),
	}
	if p.forwardMergePending == nil {
		p.forwardMergePending = make(map[string]*pendingForwardMerge)
	}
	p.forwardMergePending[msg.sessionKey] = batch
	p.enqueueMessageDispatch(msg.sessionKey, func() { p.dispatchPendingForward(batch) })
}

func (p *Platform) finishForwardWindow(batch *pendingForwardMerge) *feishuIncoming {
	timer := time.NewTimer(time.Until(batch.deadline))
	defer timer.Stop()
	select {
	case <-batch.ready:
	case <-timer.C:
	case <-batch.forward.ctx.Done():
	}
	p.forwardMergeMu.Lock()
	defer p.forwardMergeMu.Unlock()
	if p.forwardMergePending[batch.forward.sessionKey] == batch {
		delete(p.forwardMergePending, batch.forward.sessionKey)
	}
	return batch.reply
}

func (p *Platform) dispatchPendingForward(batch *pendingForwardMerge) {
	// Fetch material during the window, not after it: slow downloads do not
	// add another full window, nor do they extend the reply admission deadline.
	var material *core.Message
	batch.forward.dispatch(p, func(msg *core.Message) { material = msg })
	reply := p.finishForwardWindow(batch)
	if reply == nil || p.isMessageRecalled(reply.messageID) {
		p.dispatchCoreMessage(material)
		return
	}
	if p.isMessageRecalled(batch.forward.messageID) {
		material = nil
		reply.parentID = "" // never refetch locally recalled material
	}
	if material == nil {
		// A lookup failure must not swallow the user's instruction. The normal
		// quote path may retry the lookup and otherwise delivers B on its own.
		reply.dispatch(p, p.dispatchCoreMessage)
		return
	}
	// A was explicitly sent by this same user, so retain its downloaded
	// attachments. Do not fetch the same material again through the quote path.
	reply.parentID = ""
	var combined *core.Message
	reply.dispatch(p, func(msg *core.Message) { combined = msg })
	if combined == nil || p.isMessageRecalled(reply.messageID) {
		p.dispatchCoreMessage(material)
		return
	}
	if !p.isMessageRecalled(batch.forward.messageID) {
		history := combined.ExtraContent
		if history == "" {
			history = material.ExtraContent
		}
		combined.ExtraContent = joinFeishuExtraContent(history, material.Content)
		combined.Images = append(material.Images, combined.Images...)
		combined.Files = append(material.Files, combined.Files...)
		firstAccepted, replyAccepted := material.OnAccepted, combined.OnAccepted
		combined.OnAccepted = func() {
			if firstAccepted != nil {
				firstAccepted()
			}
			if replyAccepted != nil {
				replyAccepted()
			}
		}
		slog.Info(p.tag()+": merged forward with quoted text", "forward_message_id", batch.forward.messageID,
			"reply_message_id", reply.messageID, "session_key", reply.sessionKey)
	}
	// B is the canonical message: preserve its time, reply target and identity.
	p.dispatchCoreMessage(combined)
}

func (p *Platform) flushForwardMerges() {
	p.forwardMergeMu.Lock()
	defer p.forwardMergeMu.Unlock()
	for key, batch := range p.forwardMergePending {
		delete(p.forwardMergePending, key)
		close(batch.ready)
	}
}
