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
//   - access control identical to the WebSocket path (allow_from, allow_chat
//     for group chats, group_only) — the poller must never open a side door
//     around the platform's permission config;
//   - a catch-up-side seen record (catchupDedup, TTL covering lookback +
//     interval + buffer) that the WebSocket path also writes into (onMessage
//     marks every delivered message), so a message delivered by either
//     channel is injected at most once, even across consecutive overlapping
//     poll windows — the shared short-TTL dedup alone cannot guarantee that;
//   - the short-window dedup (core.MessageDedup) for the in-flight race
//     between a poll and a WebSocket delivery of the same message;
//   - the stale-message guard (core.IsOldMessage), so a poll right after a
//     restart does not replay historical traffic.

const (
	catchupInterval = 3 * time.Minute  // how often the poller runs
	catchupLookback = 5 * time.Minute  // how far back each poll window reaches (overlaps the interval on purpose; the catch-up seen record absorbs the overlap)
	catchupBuffer   = 30 * time.Second // exclude the most recent slice to avoid racing in-flight messages the WebSocket will still deliver
	catchupPageSize = 50
	// catchupSeenTTL is the retention of the catch-up-side seen record. It must
	// outlive every future poll window a message can still appear in (first
	// possible injection to last possible listing spans up to lookback + buffer),
	// with margin: otherwise a message listed by poll N is listed again by poll
	// N+1 after its record expired and gets injected twice.
	catchupSeenTTL = catchupLookback + catchupInterval + catchupBuffer
	// catchupMaxPages bounds the pagination walk per poll. A chat whose window
	// holds more than catchupMaxPages*catchupPageSize messages still misses the
	// older half — the catch-up is a backstop for dropped pushes, not a full
	// replay, and an unbounded loop against a broken page token would never
	// terminate.
	catchupMaxPages = 10
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
		// Several platform instances can share one app WebSocket; only the
		// primary polls, otherwise every sibling would re-inject each missed
		// message. Without this log, catch-up configured on a secondary looks
		// simply dead to the operator.
		slog.Warn(p.tag()+": catchup chats configured but this platform is not the shared-WebSocket primary; catch-up polling only runs on the primary owner of the app connection",
			"catchup_chats", p.catchupChats, "catchup_chats_p2p", p.catchupP2PChats)
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
	// The configured list decides the qualification rule, but a chat listed
	// under the wrong option key must not run with the wrong rule: a group
	// chat configured under catchup_chats_p2p would disable the mention gate
	// and re-introduce the #1618 loud-bot failure mode. Resolve the chat's
	// real type once and fail closed on a mismatch.
	if mode, ok := p.catchupChatType(ctx, chatID); ok && !catchupChatTypeMatches(isP2P, mode) {
		slog.Error(p.tag()+": catchup: chat configured under the wrong option key; excluded from catch-up polling",
			"chat_id", chatID, "chat_mode", mode, "configured_as_p2p", isP2P)
		return nil
	}

	botOpenID := p.getBotOpenID()
	if botOpenID == "" {
		// Without the bot open_id we cannot run the group mention gate.
		// Skip rather than fail open: injecting every group message would
		// turn the bot into a loud responder (issue #1618 class of bug).
		return nil
	}

	endTime := time.Now().Add(-catchupBuffer).Unix()
	startTime := time.Now().Add(-catchupLookback).Unix()

	items, err := p.listCatchupMessages(ctx, chatID, startTime, endTime)
	if err != nil {
		return err
	}

	injected := 0
	for _, msg := range items {
		createMs, ok := p.catchupShouldInject(msg, isP2P, botOpenID)
		if !ok {
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

// catchupShouldInject reports whether a REST-listed message should be
// re-injected, and its creation time in Unix milliseconds when so. It applies
// the same freshness / sender / mention filters as the WebSocket path, plus
// two dedup layers: the catch-up-side seen record (long window, shared with
// onMessage so WebSocket-delivered messages are never re-polled) and the
// short-window shared dedup (race guard against in-flight deliveries).
func (p *Platform) catchupShouldInject(msg *larkim.Message, isP2P bool, botOpenID string) (int64, bool) {
	if msg == nil || msg.MessageId == nil || msg.CreateTime == nil {
		return 0, false
	}
	if msg.Deleted != nil && *msg.Deleted {
		return 0, false
	}
	// Never re-inject the bot's own messages: the WebSocket push path
	// never delivers them, and doing so from the poller would create a
	// self-reply loop (especially in p2p chats, where every message
	// qualifies).
	if !catchupSenderQualifies(msg.Sender) {
		return 0, false
	}
	if !catchupMessageQualifies(isP2P, msg.Mentions, botOpenID) {
		return 0, false
	}
	createMs, err := strconv.ParseInt(*msg.CreateTime, 10, 64)
	if err != nil {
		return 0, false
	}
	msgTime := time.Unix(createMs/1000, (createMs%1000)*int64(time.Millisecond))
	if core.IsOldMessage(msgTime) {
		return 0, false
	}
	// The seen record outlives the poll window overlap; the shared dedup only
	// covers the seconds right around a poll (its TTL is far shorter than the
	// window overlap, so it must not be relied on across polls).
	if p.catchupDedup.IsDuplicate(*msg.MessageId) {
		return 0, false
	}
	if p.dedup.IsDuplicate(*msg.MessageId) {
		return 0, false
	}
	return createMs, true
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

// catchupPageFetcher fetches one page of a chat's message history inside the
// poll window. hasMore / nextPageToken carry the List API's pagination state.
type catchupPageFetcher func(ctx context.Context, chatID string, startTime, endTime int64, pageToken string) (items []*larkim.Message, hasMore bool, nextPageToken string, err error)

// listCatchupMessagesREST walks the Im.Message.List pagination for one poll
// window, oldest first (ByCreateTimeAsc — the dropped messages are typically
// the older half of the window). Bounded by catchupMaxPages.
func (p *Platform) listCatchupMessagesREST(ctx context.Context, chatID string, startTime, endTime int64) ([]*larkim.Message, error) {
	fetch := func(ctx context.Context, chatID string, startTime, endTime int64, pageToken string) ([]*larkim.Message, bool, string, error) {
		builder := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(chatID).
			StartTime(fmt.Sprintf("%d", startTime)).
			EndTime(fmt.Sprintf("%d", endTime)).
			SortType(larkim.SortTypeListMessageByCreateTimeAsc).
			PageSize(catchupPageSize)
		if pageToken != "" {
			builder = builder.PageToken(pageToken)
		}
		var items []*larkim.Message
		var hasMore bool
		var nextPageToken string
		if err := p.withFreshTenantAccessTokenRetry(ctx, "catchup list messages",
			func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
				resp, err := client.Im.Message.List(ctx, builder.Build(), options...)
				if err != nil {
					return err
				}
				if !resp.Success() {
					return fmt.Errorf("list messages: code=%d msg=%s", resp.Code, resp.Msg)
				}
				if resp.Data != nil {
					items = resp.Data.Items
					if resp.Data.HasMore != nil {
						hasMore = *resp.Data.HasMore
					}
					if resp.Data.PageToken != nil {
						nextPageToken = *resp.Data.PageToken
					}
				}
				return nil
			}); err != nil {
			return nil, false, "", err
		}
		return items, hasMore, nextPageToken, nil
	}
	return collectCatchupMessages(ctx, fetch, chatID, startTime, endTime)
}

// collectCatchupMessages walks a catch-upPageFetcher across pages until the
// API reports no more results or catchupMaxPages is reached, accumulating the
// items in API order (oldest first).
func collectCatchupMessages(ctx context.Context, fetch catchupPageFetcher, chatID string, startTime, endTime int64) ([]*larkim.Message, error) {
	var all []*larkim.Message
	pageToken := ""
	for page := 0; page < catchupMaxPages; page++ {
		items, hasMore, nextPageToken, err := fetch(ctx, chatID, startTime, endTime, pageToken)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if !hasMore || nextPageToken == "" {
			return all, nil
		}
		pageToken = nextPageToken
	}
	return all, nil
}

// catchupChatType resolves a chat's real mode ("group", "topic" or "p2p" —
// Im.Chat.Get's ChatMode field; its ChatType field means private/public) via
// the REST API and caches it for the process lifetime, since a chat's mode
// does not change. ok is false when the mode could not be determined.
func (p *Platform) catchupChatType(ctx context.Context, chatID string) (string, bool) {
	p.catchupChatTypeMu.Lock()
	if mode, ok := p.catchupChatTypes[chatID]; ok {
		p.catchupChatTypeMu.Unlock()
		return mode, true
	}
	p.catchupChatTypeMu.Unlock()

	mode := ""
	err := p.withFreshTenantAccessTokenRetry(ctx, "catchup resolve chat mode",
		func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Chat.Get(ctx,
				larkim.NewGetChatReqBuilder().ChatId(chatID).Build(), options...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("get chat: code=%d msg=%s", resp.Code, resp.Msg)
			}
			if resp.Data != nil && resp.Data.ChatMode != nil {
				mode = *resp.Data.ChatMode
			}
			return nil
		})
	if err != nil {
		slog.Warn(p.tag()+": catchup: failed to resolve chat mode; keeping configured qualification rule",
			"chat_id", chatID, "error", err)
		return "", false
	}
	if mode == "" {
		return "", false
	}
	p.catchupChatTypeMu.Lock()
	if p.catchupChatTypes == nil {
		p.catchupChatTypes = make(map[string]string)
	}
	p.catchupChatTypes[chatID] = mode
	p.catchupChatTypeMu.Unlock()
	return mode, true
}

// catchupChatTypeMatches reports whether a chat configured under one of the
// catch-up option keys agrees with the mode Im.Chat.Get reports. An
// inconclusive mode keeps the configured behavior rather than disabling the
// chat on unknown data (the resolve failure is already logged).
func catchupChatTypeMatches(configuredP2P bool, chatMode string) bool {
	switch chatMode {
	case "p2p":
		return configuredP2P
	case "group", "topic":
		return !configuredP2P
	default:
		return true
	}
}

// catchupAccessAllowed mirrors the WebSocket path's access-control guards
// (see onMessage: allow_from for the sender, allow_chat for group chats,
// group_only to refuse p2p traffic). The catch-up poller dispatches through
// dispatchMessage directly and never runs through onMessage, so without this
// check the poller would bypass the platform's access control entirely.
// Unlike onMessage, no unauthorized-access notice is sent back: a polled
// message the WebSocket would have rejected stays silent instead of paging
// the operator for traffic they never asked to be answered.
func catchupAccessAllowed(allowFrom, allowChat string, groupOnly bool, userID, chatID string, isP2P bool) bool {
	if !core.AllowList(allowFrom, userID) {
		return false
	}
	if isP2P {
		return !groupOnly
	}
	return core.AllowList(allowChat, chatID)
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

	if !catchupAccessAllowed(p.allowFrom, p.allowChat, p.groupOnly, userID, chatID, isP2P) {
		slog.Debug(p.tag()+": catchup: message skipped by access control",
			"message_id", messageID, "chat_id", chatID, "user", userID, "p2p", isP2P)
		return
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
