package feishu

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

// Feishu's WebSocket push (im.message.receive_v1) is best-effort: events can
// be silently dropped even while the connection looks healthy (see
// larksuite/oapi-sdk-go#195 and larksuite/node-sdk#164, where Feishu's own
// server logs show "app not online" failures against connected clients).
// The catch-up poller below is a compensation channel: every few minutes it
// lists recent messages of the operator-configured chats over the REST API
// and re-injects anything the WebSocket path missed.
//
// Injection reuses the exact same dispatchMessage path as WebSocket-delivered
// events, and is guarded by:
//   - dedup by message_id (core.MessageDedup), so a message delivered by both
//     the WebSocket and a poll is processed exactly once, whichever arrives first;
//   - the stale-message guard (core.IsOldMessage), so a poll right after a
//     restart does not replay historical traffic.

const (
	catchupInterval = 3 * time.Minute  // how often the poller runs
	catchupLookback = 5 * time.Minute  // how far back each poll window reaches (overlaps the interval on purpose; dedup absorbs the overlap)
	catchupBuffer   = 30 * time.Second // exclude the most recent slice to avoid racing in-flight messages the WebSocket will still deliver
	catchupPageSize = 50
)

// startCatchupPoller starts a background goroutine that periodically fetches
// recent messages from each configured chat and injects any qualifying
// messages missed by the WebSocket push path.
//
// It only runs on the primary owner of the shared WebSocket connection
// (isWSPrimary): secondary platforms in a shared-WS group receive fanned-out
// events from the primary, so a poller per platform would re-inject every
// missed message once per sibling. Both catchup chat options empty means the
// feature is disabled.
func (p *Platform) startCatchupPoller() {
	if p.catchupChats == "" && p.catchupP2PChats == "" {
		return
	}
	if !p.isWSPrimary {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.catchupCancel = cancel
	p.mu.Unlock()

	go func() {
		slog.Info(p.tag()+": catchup poller started",
			"interval", catchupInterval, "lookback", catchupLookback)
		defer cancel()
		ticker := time.NewTicker(catchupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.pollAllCatchupChats(ctx)
			}
		}
	}()
}

// stopCatchupPoller cancels the background poller, if running.
func (p *Platform) stopCatchupPoller() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.catchupCancel != nil {
		p.catchupCancel()
		p.catchupCancel = nil
	}
}

func (p *Platform) pollAllCatchupChats(ctx context.Context) {
	p2p := map[string]bool{}
	for _, chatID := range strings.Split(p.catchupP2PChats, ",") {
		chatID = strings.TrimSpace(chatID)
		if chatID != "" {
			p2p[chatID] = true
		}
	}
	seen := map[string]bool{}
	for _, chatID := range strings.Split(p.catchupChats+","+p.catchupP2PChats, ",") {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" || seen[chatID] {
			continue
		}
		seen[chatID] = true
		if err := p.pollChatForMissedMessages(ctx, chatID, p2p[chatID]); err != nil {
			slog.Warn(p.tag()+": catchup: poll error", "chat_id", chatID, "error", err)
		}
	}
}

// pollChatForMissedMessages lists recent messages of one chat over the REST
// API and re-injects the ones the WebSocket push path missed. isP2P selects
// the qualification rule: group chats require an explicit @bot mention, while
// p2p chats have no mention list so every user message qualifies.
func (p *Platform) pollChatForMissedMessages(ctx context.Context, chatID string, isP2P bool) error {
	botOpenID := p.getBotOpenID()
	if botOpenID == "" {
		// Without the bot open_id we cannot run the group mention gate.
		// Skip rather than fail open: injecting every group message would
		// turn the bot into a loud responder (issue #1618 class of bug).
		return nil
	}

	endTime := time.Now().Add(-catchupBuffer).Unix()
	startTime := time.Now().Add(-catchupLookback).Unix()

	req := larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(chatID).
		StartTime(fmt.Sprintf("%d", startTime)).
		EndTime(fmt.Sprintf("%d", endTime)).
		PageSize(catchupPageSize).
		Build()

	var items []*larkim.Message
	if err := p.withFreshTenantAccessTokenRetry(ctx, "catchup list messages",
		func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.List(ctx, req, options...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("list messages: code=%d msg=%s", resp.Code, resp.Msg)
			}
			if resp.Data != nil {
				items = resp.Data.Items
			}
			return nil
		}); err != nil {
		return err
	}

	injected := 0
	for _, msg := range items {
		if msg == nil || msg.MessageId == nil || msg.CreateTime == nil {
			continue
		}
		if msg.Deleted != nil && *msg.Deleted {
			continue
		}
		// Never re-inject the bot's own messages: the WebSocket push path
		// never delivers them, and doing so from the poller would create a
		// self-reply loop (especially in p2p chats, where every message
		// qualifies).
		if !catchupSenderQualifies(msg.Sender) {
			continue
		}
		if !catchupMessageQualifies(isP2P, msg.Mentions, botOpenID) {
			continue
		}
		createMs, err := strconv.ParseInt(*msg.CreateTime, 10, 64)
		if err != nil {
			continue
		}
		msgTime := time.Unix(createMs/1000, (createMs%1000)*int64(time.Millisecond))
		if core.IsOldMessage(msgTime) {
			continue
		}
		if p.dedup.IsDuplicate(*msg.MessageId) {
			continue
		}
		p.injectCatchupMessage(ctx, msg, chatID, createMs, isP2P)
		injected++
	}

	if injected > 0 {
		slog.Info(p.tag()+": catchup: injected missed messages",
			"chat_id", chatID, "count", injected)
	}
	return nil
}

// catchupMessageQualifies reports whether a REST-fetched message should be
// re-injected. Group messages require the bot to appear in the mention list;
// p2p messages skip the mention check entirely because Feishu's REST API
// never returns a mention list for p2p messages.
func catchupMessageQualifies(isP2P bool, mentions []*larkim.Mention, botOpenID string) bool {
	if isP2P {
		return true
	}
	return isBotMentionedInList(mentions, botOpenID)
}

// catchupSenderQualifies reports whether a message sender should be considered
// for catch-up injection. Messages sent by the app itself are excluded so the
// poller can never echo the bot's own replies back into the agent.
func catchupSenderQualifies(sender *larkim.Sender) bool {
	return sender == nil || sender.SenderType == nil || *sender.SenderType != "app"
}

// isBotMentionedInList checks if the bot is mentioned in a REST API message's
// mention list. The REST Mention.Id field is a *string containing the open_id
// directly, unlike the WebSocket event's Mentions[].Id which is a *UserId
// struct.
func isBotMentionedInList(mentions []*larkim.Mention, botOpenID string) bool {
	for _, m := range mentions {
		if m != nil && m.Id != nil && *m.Id == botOpenID {
			return true
		}
	}
	return false
}

// injectCatchupMessage dispatches a REST API message through the same
// dispatchMessage path used by WebSocket-delivered messages.
func (p *Platform) injectCatchupMessage(ctx context.Context, msg *larkim.Message, chatID string, createTimeMs int64, isP2P bool) {
	msgType := stringValue(msg.MsgType)
	content := ""
	if msg.Body != nil && msg.Body.Content != nil {
		content = *msg.Body.Content
	}
	mentions := convertRESTMentions(msg.Mentions)
	messageID := stringValue(msg.MessageId)
	parentID := stringValue(msg.ParentId)

	userID := ""
	if msg.Sender != nil && msg.Sender.Id != nil {
		userID = *msg.Sender.Id
	}

	chatType := "group"
	if isP2P {
		chatType = "p2p"
	}
	eventMsg := &larkim.EventMessage{
		MessageId: msg.MessageId,
		RootId:    msg.RootId,
		ChatType:  &chatType,
	}
	sessionKey := p.makeSessionKey(eventMsg, chatID, userID)
	rctx := replyContext{
		messageID:  messageID,
		chatID:     chatID,
		sessionKey: sessionKey,
	}

	slog.Info(p.tag()+": catchup: injecting missed message",
		"message_id", messageID,
		"chat_id", chatID,
		"msg_type", msgType,
	)

	go p.dispatchMessage(ctx, msgType, content, mentions, messageID, sessionKey, userID, chatID, rctx, parentID, createTimeMs)
}

// convertRESTMentions converts REST API []*larkim.Mention to the
// []*larkim.MentionEvent shape dispatchMessage expects. REST Mention.Id is a
// *string (open_id directly); MentionEvent.Id is a *UserId with an OpenId
// field.
func convertRESTMentions(mentions []*larkim.Mention) []*larkim.MentionEvent {
	result := make([]*larkim.MentionEvent, 0, len(mentions))
	for _, m := range mentions {
		if m == nil {
			continue
		}
		var userID *larkim.UserId
		if m.Id != nil {
			openID := *m.Id
			userID = &larkim.UserId{OpenId: &openID}
		}
		result = append(result, &larkim.MentionEvent{
			Key:       m.Key,
			Id:        userID,
			Name:      m.Name,
			TenantKey: m.TenantKey,
		})
	}
	return result
}
