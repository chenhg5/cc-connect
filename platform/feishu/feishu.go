package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/core"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// sanitizingLogger wraps a logger and masks sensitive URL parameters.
type sanitizingLogger struct {
	inner larkcore.Logger
}

func (l *sanitizingLogger) maskURL(args ...interface{}) []interface{} {
	masked := make([]interface{}, len(args))
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			masked[i] = l.sanitize(s)
		} else {
			masked[i] = arg
		}
	}
	return masked
}

func (l *sanitizingLogger) sanitize(s string) string {
	// Mask sensitive query parameters in URLs
	sensitiveParams := []string{
		"device_id=", "access_key=", "ticket=", "conn_id=",
		"secret=", "token=", "password=", "key=",
	}
	for _, param := range sensitiveParams {
		if idx := strings.Index(s, param); idx != -1 {
			// Find the end of the value (either & or end of string)
			end := idx + len(param)
			for end < len(s) && s[end] != '&' && s[end] != ' ' {
				end++
			}
			s = s[:idx+len(param)] + "***" + s[end:]
		}
	}
	return s
}

func (l *sanitizingLogger) Debug(ctx context.Context, args ...interface{}) {
	for _, arg := range args {
		s, ok := arg.(string)
		if !ok {
			continue
		}
		msg := strings.ToLower(s)
		if strings.Contains(msg, "ping success") || strings.Contains(msg, "receive pong") {
			return
		}
	}
	l.inner.Debug(ctx, l.maskURL(args...)...)
}

func (l *sanitizingLogger) Info(ctx context.Context, args ...interface{}) {
	l.inner.Info(ctx, l.maskURL(args...)...)
}

func (l *sanitizingLogger) Warn(ctx context.Context, args ...interface{}) {
	l.inner.Warn(ctx, l.maskURL(args...)...)
}

func (l *sanitizingLogger) Error(ctx context.Context, args ...interface{}) {
	l.inner.Error(ctx, l.maskURL(args...)...)
}

func init() {
	core.RegisterPlatform("feishu", func(opts map[string]any) (core.Platform, error) {
		return newPlatform("feishu", lark.FeishuBaseUrl, opts)
	})
	core.RegisterPlatform("lark", func(opts map[string]any) (core.Platform, error) {
		return newPlatform("lark", lark.LarkBaseUrl, opts)
	})
}

type replyContext struct {
	messageID  string
	chatID     string
	sessionKey string
}

type Platform struct {
	platformName               string
	domain                     string
	appID                      string
	appSecret                  string
	progressStyle              string
	useInteractiveCard         bool
	self                       core.Platform
	reactionEmoji              string
	allowFrom                  string
	groupReplyAll              bool
	respondToAtEveryoneAndHere bool
	shareSessionInChannel      bool
	threadIsolation            bool
	// noReplyToTrigger: when true, send via Create instead of Im.Message.Reply (no quote to the user's message).
	noReplyToTrigger bool
	resolveMentions  bool
	client           *lark.Client
	replayClient     *lark.Client
	replayClientMu   sync.Mutex
	wsClient         *larkws.Client
	handler          core.MessageHandler
	cardNavHandler   core.CardNavigationHandler
	cancel           context.CancelFunc
	dedup            core.MessageDedup
	botOpenID        string
	userNameCache    sync.Map // open_id -> display name
	chatNameCache    sync.Map // chat_id -> chat name
	chatMemberCache  sync.Map // chatID -> *chatMemberEntry
	// Webhook mode fields (for Lark international version)
	server       *http.Server
	port         string
	callbackPath string
	encryptKey   string
	eventHandler *dispatcher.EventDispatcher
	// cardActionMessageIDs tracks the most recent card-action messageID per
	// session key, enabling async card refreshes via the Patch API.
	cardActionMsgMu  sync.Mutex
	cardActionMsgIDs map[string]string // sessionKey → messageID
}

type interactivePlatform struct {
	*Platform
}

type feishuRequestFunc func(client *lark.Client, options ...larkcore.RequestOptionFunc) error

func (p *Platform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	p.cardNavHandler = h
}

func New(opts map[string]any) (core.Platform, error) {
	return newPlatform("feishu", lark.FeishuBaseUrl, opts)
}

func newPlatform(name, domain string, opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("%s: app_id and app_secret are required", name)
	}
	if v, ok := opts["domain"].(string); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			if _, err := url.ParseRequestURI(v); err != nil {
				return nil, fmt.Errorf("%s: invalid domain %q: %w", name, v, err)
			}
			domain = v
		}
	}
	reactionEmoji, _ := opts["reaction_emoji"].(string)
	if reactionEmoji == "" {
		reactionEmoji = "OnIt"
	}
	if v, ok := opts["reaction_emoji"].(string); ok && v == "none" {
		reactionEmoji = ""
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom(name, allowFrom)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	respondToAtEveryoneAndHere, _ := opts["respond_to_at_everyone_and_here"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	threadIsolation, _ := opts["thread_isolation"].(bool)
	resolveMentionsOpt, _ := opts["resolve_mentions"].(bool)
	noReplyToTrigger := false
	if v, ok := opts["reply_to_trigger"].(bool); ok && !v {
		noReplyToTrigger = true
	}

	progressStyle := "legacy"
	if v, ok := opts["progress_style"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "legacy":
			progressStyle = "legacy"
		case "compact", "card":
			progressStyle = strings.ToLower(strings.TrimSpace(v))
		default:
			return nil, fmt.Errorf("%s: invalid progress_style %q (want legacy, compact, or card)", name, v)
		}
	}
	useInteractiveCard := true
	if v, ok := opts["enable_feishu_card"].(bool); ok {
		useInteractiveCard = v
	}

	// Webhook mode configuration (for Lark international version)
	port, _ := opts["port"].(string)
	if port == "" {
		port = "8080"
	}
	callbackPath, _ := opts["callback_path"].(string)
	if callbackPath == "" {
		callbackPath = "/feishu/webhook"
	}
	encryptKey, _ := opts["encrypt_key"].(string)

	var clientOpts []lark.ClientOptionFunc
	if domain != lark.FeishuBaseUrl {
		clientOpts = append(clientOpts, lark.WithOpenBaseUrl(domain))
	}

	base := &Platform{
		platformName:               name,
		domain:                     domain,
		appID:                      appID,
		appSecret:                  appSecret,
		progressStyle:              progressStyle,
		useInteractiveCard:         useInteractiveCard,
		reactionEmoji:              reactionEmoji,
		allowFrom:                  allowFrom,
		groupReplyAll:              groupReplyAll,
		respondToAtEveryoneAndHere: respondToAtEveryoneAndHere,
		shareSessionInChannel:      shareSessionInChannel,
		threadIsolation:            threadIsolation,
		resolveMentions:            resolveMentionsOpt,
		noReplyToTrigger:           noReplyToTrigger,
		client:                     lark.NewClient(appID, appSecret, clientOpts...),
		replayClient:               newFeishuReplayClient(appID, appSecret, domain),
		port:                       port,
		callbackPath:               callbackPath,
		encryptKey:                 encryptKey,
	}
	if !useInteractiveCard {
		base.self = base
		return base, nil
	}
	wrapped := &interactivePlatform{Platform: base}
	base.self = wrapped
	return wrapped, nil
}

func (p *Platform) Name() string { return p.platformName }

func (p *Platform) ProgressStyle() string { return p.progressStyle }

func (p *Platform) SupportsProgressCardPayload() bool { return true }

func (p *Platform) tag() string { return p.platformName }

func (p *Platform) dispatchPlatform() core.Platform {
	if p.self != nil {
		return p.self
	}
	return p
}

func (p *Platform) KeepPreviewOnFinish() bool {
	return p.useInteractiveCard
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	if openID, err := p.fetchBotOpenID(); err != nil {
		slog.Warn(p.platformName+": failed to get bot open_id, group chat filtering disabled", "error", err)
	} else {
		p.botOpenID = openID
		slog.Info(p.platformName+": bot identified", "open_id", openID)
	}

	p.eventHandler = dispatcher.NewEventDispatcher("", p.encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			slog.Debug(p.platformName+": message received", "app_id", p.appID)
			return p.onMessage(ctx, event)
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return nil // ignore read receipts
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			slog.Debug(p.platformName+": user opened bot chat", "app_id", p.appID)
			return nil
		}).
		OnP1P2PChatCreatedV1(func(ctx context.Context, event *larkim.P1P2PChatCreatedV1) error {
			slog.Debug(p.platformName+": p2p chat created", "app_id", p.appID)
			return nil
		}).
		OnP2MessageReactionCreatedV1(func(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return nil // ignore reaction events (triggered by our own addReaction)
		}).
		OnP2MessageReactionDeletedV1(func(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
			return nil // ignore reaction removal events (triggered by our own removeReaction)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return p.onCardAction(event)
		}).
		OnP2BotMenuV6(func(ctx context.Context, event *larkapplication.P2BotMenuV6) error {
			return p.onBotMenu(event)
		})

	// Lark international version uses Webhook mode, not WebSocket long connection
	// Feishu domestic version supports WebSocket long connection
	if p.platformName == "lark" {
		return p.startWebhookMode()
	}

	return p.startWebSocketMode()
}

// startWebSocketMode starts the WebSocket long connection mode (for Feishu domestic version)
func (p *Platform) startWebSocketMode() error {
	wsOpts := []larkws.ClientOption{
		larkws.WithEventHandler(p.eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithLogger(&sanitizingLogger{inner: larkcore.NewEventLogger()}),
	}
	if p.domain != lark.FeishuBaseUrl {
		wsOpts = append(wsOpts, larkws.WithDomain(p.domain))
	}
	p.wsClient = larkws.NewClient(p.appID, p.appSecret, wsOpts...)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		if err := p.wsClient.Start(ctx); err != nil {
			slog.Error(p.tag()+": websocket error", "error", err)
		}
	}()

	return nil
}

// startWebhookMode starts the HTTP webhook server mode (for Lark international version)
func (p *Platform) startWebhookMode() error {
	mux := http.NewServeMux()
	mux.HandleFunc(p.callbackPath, p.webhookHandler)

	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: mux,
	}

	_, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		slog.Info(p.tag()+": webhook server listening", "port", p.port, "path", p.callbackPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error(p.tag()+": webhook server error", "error", err)
		}
	}()

	return nil
}

// webhookHandler handles HTTP webhook requests from Lark international version
func (p *Platform) webhookHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error(p.tag()+": read webhook body failed", "error", err)
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// Build EventReq from HTTP request
	req := &larkevent.EventReq{
		Header:     r.Header,
		Body:       body,
		RequestURI: r.RequestURI,
	}

	// Use the SDK's event dispatcher to handle the request
	resp := p.eventHandler.Handle(r.Context(), req)

	// Write response
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.Body)
}

// onCardAction handles card.action.trigger callbacks via the official SDK event dispatcher.
// Three prefixes are supported:
//   - nav:/xxx   — render a card page and update the original card in-place
//   - act:/xxx   — execute an action, then render and update the card in-place
//   - cmd:/xxx   — legacy: dispatch as a user command (sends a new message)
func (p *Platform) onCardAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event.Event == nil || event.Event.Action == nil {
		return nil, nil
	}

	actionVal, _ := event.Event.Action.Value["action"].(string)

	// select_static callbacks put the chosen value in event.Event.Action.Option
	if actionVal == "" && event.Event.Action.Option != "" {
		actionVal = event.Event.Action.Option
	}
	if actionVal == "" {
		switch event.Event.Action.Name {
		case "delete_mode_submit":
			actionVal = "act:/delete-mode form-submit"
		case "delete_mode_cancel":
			actionVal = "act:/delete-mode cancel"
		}
	}
	if actionVal == "act:/delete-mode form-submit" {
		ids := collectDeleteModeSelectedFromFormValue(event.Event.Action.FormValue)
		if len(ids) > 0 {
			actionVal += " " + strings.Join(ids, ",")
		}
	}

	userID := ""
	if event.Event.Operator != nil {
		userID = event.Event.Operator.OpenID
	}
	chatID := ""
	messageID := ""
	if event.Event.Context != nil {
		chatID = event.Event.Context.OpenChatID
		messageID = event.Event.Context.OpenMessageID
	}
	if chatID == "" {
		chatID = userID
	}
	sessionKey := p.sessionKeyFromCardAction(chatID, userID, event.Event.Action.Value)

	// nav: / act: — synchronous card update
	if strings.HasPrefix(actionVal, "nav:") || strings.HasPrefix(actionVal, "act:") {
		if messageID != "" {
			p.cardActionMsgMu.Lock()
			if p.cardActionMsgIDs == nil {
				p.cardActionMsgIDs = make(map[string]string)
			}
			p.cardActionMsgIDs[sessionKey] = messageID
			p.cardActionMsgMu.Unlock()
		}
		// Feishu uses native form checker for delete-mode toggle,
		// so return a toast without calling cardNavHandler to avoid a full card refresh.
		if strings.HasPrefix(actionVal, "act:/delete-mode toggle ") {
			return &callback.CardActionTriggerResponse{
				Toast: &callback.Toast{
					Type:    "info",
					Content: "已记录选择（Selection recorded）",
				},
			}, nil
		}
		if p.cardNavHandler != nil {
			card := p.cardNavHandler(actionVal, sessionKey)
			if card != nil {
				return &callback.CardActionTriggerResponse{
					Card: &callback.Card{
						Type: "raw",
						Data: renderCardMap(card, sessionKey),
					},
				}, nil
			}
		}
		if strings.HasPrefix(actionVal, "act:") {
			slog.Debug(p.tag()+": card action produced no card update", "action", actionVal)
			return nil, nil
		}
		slog.Warn(p.tag()+": card nav returned nil, ignoring", "action", actionVal)
		return nil, nil
	}

	// perm: — permission response with in-place card update
	if strings.HasPrefix(actionVal, "perm:") {
		var responseText string
		switch actionVal {
		case "perm:allow":
			responseText = "allow"
		case "perm:deny":
			responseText = "deny"
		case "perm:allow_all":
			responseText = "allow all"
		default:
			return nil, nil
		}

		rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}
		go p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			UserName:   p.resolveUserName(userID),
			ChatName:   p.resolveChatName(chatID),
			Content:    responseText,
			ReplyCtx:   rctx,
		})

		permLabel, _ := event.Event.Action.Value["perm_label"].(string)
		permColor, _ := event.Event.Action.Value["perm_color"].(string)
		permBody, _ := event.Event.Action.Value["perm_body"].(string)
		if permColor == "" {
			permColor = "green"
		}
		cb := core.NewCard().Title(permLabel, permColor)
		if permBody != "" {
			cb.Markdown(permBody)
		}
		return &callback.CardActionTriggerResponse{
			Card: &callback.Card{
				Type: "raw",
				Data: renderCardMap(cb.Build(), sessionKey),
			},
		}, nil
	}

	// askq: — AskUserQuestion option selected, forward as user message
	if strings.HasPrefix(actionVal, "askq:") {
		rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}
		go p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			UserName:   p.resolveUserName(userID),
			ChatName:   p.resolveChatName(chatID),
			Content:    actionVal,
			ReplyCtx:   rctx,
		})

		answerLabel, _ := event.Event.Action.Value["askq_label"].(string)
		askqQuestion, _ := event.Event.Action.Value["askq_question"].(string)
		if answerLabel == "" {
			answerLabel = actionVal
		}
		cb := core.NewCard().Title("✅ "+answerLabel, "green")
		if askqQuestion != "" {
			cb.Markdown(askqQuestion)
		}
		cb.Markdown("**→ " + answerLabel + "**")
		return &callback.CardActionTriggerResponse{
			Card: &callback.Card{
				Type: "raw",
				Data: renderCardMap(cb.Build(), sessionKey),
			},
		}, nil
	}

	// cmd: — async command dispatch
	if strings.HasPrefix(actionVal, "cmd:") {
		cmdText := strings.TrimPrefix(actionVal, "cmd:")
		rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}

		slog.Info(p.tag()+": card action dispatched as command", "cmd", cmdText, "user", userID)

		go p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			UserName:   p.resolveUserName(userID),
			ChatName:   p.resolveChatName(chatID),
			Content:    cmdText,
			ReplyCtx:   rctx,
		})
	}

	return nil, nil
}

func (p *Platform) addReaction(messageID string) string {
	if p.reactionEmoji == "" {
		return ""
	}
	emojiType := p.reactionEmoji
	resp, err := p.client.Im.MessageReaction.Create(context.Background(),
		larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(&larkim.Emoji{EmojiType: &emojiType}).
				Build()).
			Build())
	if err != nil {
		slog.Debug(p.tag()+": add reaction failed", "error", err)
		return ""
	}
	if !resp.Success() {
		slog.Debug(p.tag()+": add reaction failed", "code", resp.Code, "msg", resp.Msg)
		return ""
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		return *resp.Data.ReactionId
	}
	return ""
}

func (p *Platform) removeReaction(messageID, reactionID string) {
	if reactionID == "" || messageID == "" {
		return
	}
	resp, err := p.client.Im.MessageReaction.Delete(context.Background(),
		larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build())
	if err != nil {
		slog.Debug(p.tag()+": remove reaction failed", "error", err)
		return
	}
	if !resp.Success() {
		slog.Debug(p.tag()+": remove reaction failed", "code", resp.Code, "msg", resp.Msg)
	}
}

// StartTyping adds an emoji reaction to the user's message and returns a stop
// function that removes the reaction when processing is complete.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.messageID == "" {
		return func() {}
	}
	reactionID := p.addReaction(rc.messageID)
	return func() {
		go p.removeReaction(rc.messageID, reactionID)
	}
}

func (p *Platform) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	sender := event.Event.Sender

	msgType := ""
	if msg.MessageType != nil {
		msgType = *msg.MessageType
	}

	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}
	userID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		userID = *sender.SenderId.OpenId
	}
	// userName and chatName are resolved in dispatchMessage to avoid blocking
	// the SDK dispatcher goroutine with synchronous HTTP calls.

	messageID := ""
	if msg.MessageId != nil {
		messageID = *msg.MessageId
	}

	if p.dedup.IsDuplicate(messageID) {
		slog.Debug(p.tag()+": duplicate message ignored", "message_id", messageID)
		return nil
	}

	if msg.CreateTime != nil {
		if ms, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			msgTime := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if core.IsOldMessage(msgTime) {
				slog.Debug(p.tag()+": ignoring old message after restart", "create_time", *msg.CreateTime)
				return nil
			}
		}
	}

	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}
	mentionCount := len(msg.Mentions)
	slog.Debug(p.tag()+": inbound message",
		"message_id", messageID,
		"chat_id", chatID,
		"chat_type", chatType,
		"root_id", stringValue(msg.RootId),
		"thread_id", stringValue(msg.ThreadId),
		"parent_id", stringValue(msg.ParentId),
		"mentions", mentionCount,
		"group_reply_all", p.groupReplyAll,
		"thread_isolation", p.threadIsolation,
	)

	if chatType == "group" && !p.groupReplyAll && p.botOpenID != "" {
		if !isBotMentioned(msg.Mentions, p.botOpenID) {
			// Feishu @all sends {"text":"@_all"} with 0 mentions.
			if p.respondToAtEveryoneAndHere && msg.Content != nil && strings.Contains(*msg.Content, "@_all") {
				slog.Debug(p.tag()+": responding to @all message", "chat_id", chatID)
			} else {
				slog.Debug(p.tag()+": ignoring group message without bot mention", "chat_id", chatID)
				return nil
			}
		}
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug(p.tag()+": message from unauthorized user", "user", userID)
		return nil
	}

	if msg.Content == nil && msgType != "merge_forward" {
		slog.Debug(p.tag()+": message content is nil", "message_id", messageID, "type", msgType)
		return nil
	}

	// Capture content before going async — the SDK may reuse the event object.
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	mentions := msg.Mentions
	parentID := stringValue(msg.ParentId)

	sessionKey := p.makeSessionKey(msg, chatID, userID)
	rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}
	slog.Debug(p.tag()+": routed inbound message",
		"message_id", messageID,
		"session_key", sessionKey,
		"reply_in_thread", p.shouldReplyInThread(rctx),
	)

	// Dispatch message handling asynchronously so the SDK event loop is not
	// blocked by IO-heavy operations (image/audio download, handler HTTP calls).
	// The dedup and old-message checks above remain synchronous to guarantee
	// correctness before spawning the goroutine.
	go p.dispatchMessage(ctx, msgType, content, mentions, messageID, sessionKey, userID, chatID, rctx, parentID)

	return nil
}

// dispatchMessage handles the message content parsing, media download, and
// handler invocation. It runs in its own goroutine so that onMessage returns
// quickly and does not block the SDK event loop.
func (p *Platform) dispatchMessage(ctx context.Context, msgType, content string, mentions []*larkim.MentionEvent, messageID, sessionKey, userID, chatID string, rctx replyContext, parentID string) {
	// Resolve user and chat names asynchronously so SDK dispatcher is not blocked.
	userName := ""
	if userID != "" {
		userName = p.resolveUserName(userID)
	}
	chatName := p.resolveChatName(chatID)

	// If this message is a reply to another message, fetch the quoted content
	// and prepend it so the agent has full context.
	quotedPrefix := ""
	if parentID != "" {
		quotedPrefix = p.fetchQuotedMessage(ctx, parentID)
	}

	switch msgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &textBody); err != nil {
			slog.Error(p.tag()+": failed to parse text content", "error", err)
			return
		}
		text := stripMentions(textBody.Text, mentions, p.botOpenID)
		if text == "" {
			slog.Debug(p.tag()+": dropping empty text after mention stripping",
				"message_id", messageID,
				"raw_text_len", len(textBody.Text),
				"mentions", len(mentions),
			)
			return
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content: quotedPrefix + text, ReplyCtx: rctx,
		})

	case "image":
		var imgBody struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(content), &imgBody); err != nil {
			slog.Error(p.tag()+": failed to parse image content", "error", err)
			return
		}
		imgData, mimeType, err := p.downloadImage(messageID, imgBody.ImageKey)
		if err != nil {
			slog.Error(p.tag()+": download image failed", "error", err)
			return
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Images:   []core.ImageAttachment{{MimeType: mimeType, Data: imgData}},
			ReplyCtx: rctx,
		})

	case "audio":
		var audioBody struct {
			FileKey  string `json:"file_key"`
			Duration int    `json:"duration"` // milliseconds
		}
		if err := json.Unmarshal([]byte(content), &audioBody); err != nil {
			slog.Error(p.tag()+": failed to parse audio content", "error", err)
			return
		}
		slog.Debug(p.tag()+": audio received", "user", userID, "file_key", audioBody.FileKey)
		audioData, err := p.downloadResource(messageID, audioBody.FileKey, "file")
		if err != nil {
			slog.Error(p.tag()+": download audio failed", "error", err)
			return
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Audio: &core.AudioAttachment{
				MimeType: "audio/opus",
				Data:     audioData,
				Format:   "ogg",
				Duration: audioBody.Duration / 1000,
			},
			ReplyCtx: rctx,
		})

	case "post":
		textParts, images := p.parsePostContent(messageID, content)
		text := stripMentions(strings.Join(textParts, "\n"), mentions, p.botOpenID)
		if text == "" && len(images) == 0 {
			return
		}
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content: quotedPrefix + text, Images: images,
			ReplyCtx: rctx,
		})

	case "file":
		var fileBody struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(content), &fileBody); err != nil {
			slog.Error(p.tag()+": failed to parse file content", "error", err)
			return
		}
		slog.Info(p.tag()+": file received", "user", userID, "file_key", fileBody.FileKey, "file_name", fileBody.FileName)
		fileData, err := p.downloadResource(messageID, fileBody.FileKey, "file")
		if err != nil {
			slog.Error(p.tag()+": download file failed", "error", err)
			return
		}
		slog.Debug(p.tag()+": file downloaded", "file_name", fileBody.FileName, "size", len(fileData))
		mimeType := detectMimeType(fileData)
		p.handler(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Files: []core.FileAttachment{{
				MimeType: mimeType,
				Data:     fileData,
				FileName: fileBody.FileName,
			}},
			ReplyCtx: rctx,
		})

	case "merge_forward":
		text, images, files := p.parseMergeForward(messageID)
		if text == "" && len(images) == 0 && len(files) == 0 {
			slog.Warn(p.tag()+": merge_forward produced no content", "message_id", messageID)
			return
		}
		coreMsg := &core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content:  text,
			Images:   images,
			Files:    files,
			ReplyCtx: rctx,
		}
		p.handler(p.dispatchPlatform(), coreMsg)

	default:
		slog.Debug(p.tag()+": ignoring unsupported message type", "type", msgType)
	}
}

// resolveUserName fetches a user's display name via the Contact API, with caching.
func (p *Platform) resolveUserName(openID string) string {
	if cached, ok := p.userNameCache.Load(openID); ok {
		return cached.(string)
	}
	resp, err := p.client.Contact.User.Get(context.Background(),
		larkcontact.NewGetUserReqBuilder().
			UserId(openID).
			UserIdType("open_id").
			Build())
	if err != nil {
		slog.Debug(p.tag()+": resolve user name failed", "open_id", openID, "error", err)
		return openID
	}
	if !resp.Success() || resp.Data == nil || resp.Data.User == nil || resp.Data.User.Name == nil {
		slog.Debug(p.tag()+": resolve user name: no data", "open_id", openID, "code", resp.Code)
		return openID
	}
	name := *resp.Data.User.Name
	p.userNameCache.Store(openID, name)
	return name
}

// resolveUserNames batch-resolves open_ids to display names.
func (p *Platform) resolveUserNames(openIDs []string) map[string]string {
	names := make(map[string]string, len(openIDs))
	for _, id := range openIDs {
		if _, ok := names[id]; !ok {
			names[id] = p.resolveUserName(id)
		}
	}
	return names
}

// resolveChatName fetches a chat/group name via the IM API, with caching.
func (p *Platform) resolveChatName(chatID string) string {
	if chatID == "" {
		return ""
	}
	if cached, ok := p.chatNameCache.Load(chatID); ok {
		return cached.(string)
	}
	resp, err := p.client.Im.Chat.Get(context.Background(),
		larkim.NewGetChatReqBuilder().ChatId(chatID).Build())
	if err != nil {
		slog.Debug(p.tag()+": resolve chat name failed", "chat_id", chatID, "error", err)
		return chatID
	}
	if !resp.Success() || resp.Data == nil || resp.Data.Name == nil {
		slog.Debug(p.tag()+": resolve chat name: no data", "chat_id", chatID, "code", resp.Code)
		return chatID
	}
	name := *resp.Data.Name
	if name == "" {
		return chatID
	}
	p.chatNameCache.Store(chatID, name)
	return name
}

// --- Mention resolution ---

const chatMemberCacheTTL = 1 * time.Hour

type chatMemberEntry struct {
	members   map[string]string // displayName -> openID
	fetchedAt time.Time
}

// fetchChatMembers retrieves all members of a chat and returns a name->openID map.
func (p *Platform) fetchChatMembers(ctx context.Context, chatID string) (map[string]string, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not initialized", p.tag())
	}
	members := make(map[string]string)
	req := larkim.NewGetChatMembersReqBuilder().
		ChatId(chatID).
		MemberIdType("open_id").
		PageSize(100).
		Build()
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := p.fetchFreshTenantAccessToken(timeoutCtx)
	if err != nil {
		return nil, fmt.Errorf("%s: fetch tenant token for chat members: %w", p.tag(), err)
	}
	iter, err := p.client.Im.ChatMembers.GetByIterator(timeoutCtx, req, larkcore.WithTenantAccessToken(token))
	if err != nil {
		return nil, fmt.Errorf("%s: list chat members: %w", p.tag(), err)
	}
	for {
		hasMore, member, err := iter.Next()
		if err != nil {
			slog.Debug(p.tag()+": fetch chat members page error", "chat_id", chatID, "error", err)
			break
		}
		if member != nil && member.Name != nil && member.MemberId != nil {
			name := *member.Name
			if _, exists := members[name]; !exists {
				members[name] = *member.MemberId
			} else {
				members[name] = ""
			}
		}
		if !hasMore {
			break
		}
	}
	return members, nil
}

// getChatMembers returns the cached name->openID map for a chat, fetching if needed.
func (p *Platform) getChatMembers(ctx context.Context, chatID string) map[string]string {
	if v, ok := p.chatMemberCache.Load(chatID); ok {
		entry := v.(*chatMemberEntry)
		if time.Since(entry.fetchedAt) < chatMemberCacheTTL {
			return entry.members
		}
	}
	members, err := p.fetchChatMembers(ctx, chatID)
	if err != nil {
		slog.Debug(p.tag()+": fetch chat members failed", "chat_id", chatID, "error", err)
		return nil
	}
	p.chatMemberCache.Store(chatID, &chatMemberEntry{members: members, fetchedAt: time.Now()})
	return members
}

// resolveMentionsInContent replaces @name with Feishu at tags in raw content
// (before JSON serialization). Reverse-matches against the chat member list,
// longest name first. Uses the correct at syntax based on predicted message type.
func (p *Platform) resolveMentionsInContent(ctx context.Context, chatID, content string) string {
	if !p.resolveMentions || chatID == "" || !strings.Contains(content, "@") {
		return content
	}
	members := p.getChatMembers(ctx, chatID)
	if len(members) == 0 {
		return content
	}
	// Sort names longest-first to avoid partial matches.
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	useCardFormat := predictMsgType(content) == larkim.MsgTypeInteractive
	result := content
	for _, name := range names {
		pattern := "@" + name
		if !strings.Contains(result, pattern) {
			continue
		}
		openID := members[name]
		if openID == "" {
			slog.Debug(p.tag()+": skipping ambiguous mention", "name", name)
			continue
		}
		var atTag string
		if useCardFormat {
			atTag = fmt.Sprintf(`<at id=%s></at>`, openID)
		} else {
			escapedName := html.EscapeString(name)
			atTag = fmt.Sprintf(`<at user_id="%s">%s</at>`, openID, escapedName)
		}
		slog.Debug(p.tag()+": mention resolved", "name", name, "card_format", useCardFormat)
		result = strings.ReplaceAll(result, pattern, atTag)
	}
	return result
}

// fetchQuotedMessage retrieves the content of a parent message that the user
// is replying to, and returns a formatted prefix string for context injection.
// Returns empty string on any failure (graceful degradation — the user's own
// message is still delivered without the quote).
func (p *Platform) fetchQuotedMessage(ctx context.Context, parentID string) string {
	// Use raw API call with card_msg_content_type=raw_card_content so that
	// interactive card messages return the full card JSON (with json_card field)
	// instead of the simplified final state.
	apiPath := fmt.Sprintf("/open-apis/im/v1/messages/%s?card_msg_content_type=raw_card_content", parentID)
	apiResp, err := p.client.Get(ctx, apiPath, nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		slog.Debug(p.tag()+": fetch quoted message failed", "parent_id", parentID, "error", err)
		return ""
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				MsgType string `json:"msg_type"`
				Sender  struct {
					ID string `json:"id"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
				Mentions []*larkim.Mention `json:"mentions"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil || resp.Code != 0 || len(resp.Data.Items) == 0 {
		slog.Debug(p.tag()+": fetch quoted message: parse failed or no data", "parent_id", parentID)
		return ""
	}

	item := resp.Data.Items[0]
	msgType := item.MsgType
	content := item.Body.Content
	if content == "" {
		return ""
	}

	// Extract plain text based on message type.
	var quotedText string
	switch msgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &textBody); err == nil {
			quotedText = replaceMentions(textBody.Text, item.Mentions)
		}
	case "post":
		// Rich text — extract text elements from the post structure.
		quotedText = extractPostPlainText(content)
	case "interactive":
		quotedText = extractInteractiveCardText(content)
	default:
		// For non-text types (image, file, audio, etc.), use a type indicator.
		quotedText = fmt.Sprintf("[%s]", msgType)
	}

	if quotedText == "" {
		return ""
	}

	// Resolve sender name.
	senderName := ""
	if item.Sender.ID != "" {
		senderName = p.resolveUserName(item.Sender.ID)
	}
	if senderName == "" {
		senderName = "unknown"
	}

	return fmt.Sprintf("[Quoted message from %s]:\n%s\n\n", senderName, quotedText)
}

// extractPostPlainText extracts plain text from a Lark post (rich text) JSON content.
func extractPostPlainText(content string) string {
	var post struct {
		Content [][]struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			Language string `json:"language,omitempty"`
		} `json:"content"`
		Title string `json:"title"`
	}
	// Post content may be wrapped in a locale key like {"zh_cn": {...}}.
	// Try direct parse first, then try extracting from locale wrapper.
	if err := json.Unmarshal([]byte(content), &post); err != nil || len(post.Content) == 0 {
		var localeWrapper map[string]json.RawMessage
		if err2 := json.Unmarshal([]byte(content), &localeWrapper); err2 == nil {
			for _, v := range localeWrapper {
				if err3 := json.Unmarshal(v, &post); err3 == nil && len(post.Content) > 0 {
					break
				}
			}
		}
	}
	if len(post.Content) == 0 {
		return ""
	}
	var parts []string
	if post.Title != "" {
		parts = append(parts, post.Title)
	}
	for _, para := range post.Content {
		var line []string
		for _, elem := range para {
			switch elem.Tag {
			case "text":
				if elem.Text != "" {
					line = append(line, elem.Text)
				}
			case "code_block":
				if elem.Text != "" {
					lang := elem.Language
					line = append(line, "```"+lang+"\n"+elem.Text+"\n```")
				}
			}
		}
		if len(line) > 0 {
			parts = append(parts, strings.Join(line, ""))
		}
	}
	return strings.Join(parts, "\n")
}

// extractInteractiveCardText extracts readable text from a Feishu interactive card JSON.
// With raw_card_content, the response wraps the card in {"json_card": "...", ...}.
// Supports schema 2.0 (body.property.elements with recursive nesting) and
// legacy format (top-level title + elements).
func extractInteractiveCardText(content string) string {
	// Try raw_card_content format: {"json_card": "<escaped JSON>", ...}
	var wrapper struct {
		JsonCard string `json:"json_card"`
	}
	cardJSON := content
	if json.Unmarshal([]byte(content), &wrapper) == nil && wrapper.JsonCard != "" {
		cardJSON = wrapper.JsonCard
	}

	var card map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		return "[interactive card]"
	}

	var parts []string

	// Schema 2.0: body may use property.elements (standard) or direct elements (simplified).
	if raw, ok := card["body"]; ok {
		var body struct {
			Tag      string            `json:"tag"`
			Elements []json.RawMessage `json:"elements"`
			Property struct {
				Elements []json.RawMessage `json:"elements"`
			} `json:"property"`
		}
		if json.Unmarshal(raw, &body) == nil {
			if body.Tag == "body" && len(body.Property.Elements) > 0 {
				extractCardElements(body.Property.Elements, &parts)
			} else if len(body.Elements) > 0 {
				extractCardElements(body.Elements, &parts)
			}
		}
	}

	// Legacy: direct title string + flat/nested elements.
	if len(parts) == 0 {
		if raw, ok := card["header"]; ok {
			var header struct {
				Title struct {
					Content string `json:"content"`
				} `json:"title"`
			}
			if json.Unmarshal(raw, &header) == nil && header.Title.Content != "" {
				parts = append(parts, header.Title.Content)
			}
		}
		if len(parts) == 0 {
			if raw, ok := card["title"]; ok {
				var title string
				if json.Unmarshal(raw, &title) == nil && title != "" {
					parts = append(parts, title)
				}
			}
		}
		var elements []json.RawMessage
		if raw, ok := card["elements"]; ok {
			var nested [][]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil && len(nested) > 0 {
				for _, row := range nested {
					elements = append(elements, row...)
				}
			} else {
				_ = json.Unmarshal(raw, &elements)
			}
		}
		for _, raw := range elements {
			var elem struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &elem) == nil && elem.Tag == "text" && strings.TrimSpace(elem.Text) != "" {
				parts = append(parts, elem.Text)
			}
		}
	}

	if len(parts) == 0 {
		return "[interactive card]"
	}
	return strings.Join(parts, "\n")
}

// extractCardElements recursively extracts text from schema 2.0 card elements.
// Handles: property.content, property.text (nested element), property.elements (recursive),
// code_span, code_block (with tokenized contents), text_tag, hr, etc.
func extractCardElements(elements []json.RawMessage, parts *[]string) {
	for _, raw := range elements {
		var elem struct {
			Tag      string `json:"tag"`
			Content  string `json:"content"`
			Property struct {
				Content  string            `json:"content"`
				Contents json.RawMessage   `json:"contents"`
				Language string            `json:"language"`
				Elements []json.RawMessage `json:"elements"`
				Text     json.RawMessage   `json:"text"`
				Items    json.RawMessage   `json:"items"`
				Columns  json.RawMessage   `json:"columns"`
				Rows     json.RawMessage   `json:"rows"`
			} `json:"property"`
		}
		if json.Unmarshal(raw, &elem) != nil {
			continue
		}
		switch elem.Tag {
		case "code_block":
			var lines []struct {
				Contents []struct {
					Content string `json:"content"`
				} `json:"contents"`
			}
			if json.Unmarshal(elem.Property.Contents, &lines) == nil {
				var codeLines []string
				for _, line := range lines {
					var lineText string
					for _, tok := range line.Contents {
						lineText += tok.Content
					}
					codeLines = append(codeLines, lineText)
				}
				code := strings.Join(codeLines, "")
				if strings.TrimSpace(code) != "" {
					lang := elem.Property.Language
					if lang != "" {
						*parts = append(*parts, fmt.Sprintf("```%s\n%s```", lang, code))
					} else {
						*parts = append(*parts, fmt.Sprintf("```\n%s```", code))
					}
				}
			}
		case "code_span":
			if elem.Property.Content != "" {
				*parts = append(*parts, "`"+elem.Property.Content+"`")
			}
		case "hr":
			*parts = append(*parts, "---")
		case "table":
			extractCardTable(elem.Property.Columns, elem.Property.Rows, parts)
		case "list":
			extractCardListItems(elem.Property.Items, parts)
		default:
			content := elem.Property.Content
			if content == "" {
				content = elem.Content
			}
			if content != "" {
				*parts = append(*parts, content)
			}
			if len(elem.Property.Text) > 0 {
				var textElem struct {
					Property struct {
						Content string `json:"content"`
					} `json:"property"`
				}
				if json.Unmarshal(elem.Property.Text, &textElem) == nil && textElem.Property.Content != "" {
					*parts = append(*parts, textElem.Property.Content)
				}
			}
		}
		if len(elem.Property.Elements) > 0 {
			extractCardElements(elem.Property.Elements, parts)
		}
	}
}

// extractCardTable extracts text from a Feishu card table element.
// Table structure: property.columns defines column names/headers,
// property.rows is an array of row objects where each key is the column name
// and the value has a "data" field containing a markdown/plain_text element.
func extractCardTable(columnsRaw, rowsRaw json.RawMessage, parts *[]string) {
	var columns []struct {
		DisplayName string `json:"displayName"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal(columnsRaw, &columns); err != nil || len(columns) == 0 {
		return
	}
	var rows []map[string]struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		return
	}

	// Build markdown table.
	header := make([]string, len(columns))
	for i, col := range columns {
		header[i] = col.DisplayName
	}
	*parts = append(*parts, "| "+strings.Join(header, " | ")+" |")
	sep := make([]string, len(columns))
	for i := range sep {
		sep[i] = "---"
	}
	*parts = append(*parts, "| "+strings.Join(sep, " | ")+" |")

	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cell := row[col.Name]
			var cellParts []string
			extractCardElements([]json.RawMessage{cell.Data}, &cellParts)
			cells[i] = strings.Join(cellParts, " ")
		}
		*parts = append(*parts, "| "+strings.Join(cells, " | ")+" |")
	}
}

// extractCardListItems extracts text from a Feishu card list element.
// List structure: property.items is an array of items, each with an "elements" array.
func extractCardListItems(itemsRaw json.RawMessage, parts *[]string) {
	var items []struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return
	}
	for _, item := range items {
		var itemParts []string
		extractCardElements(item.Elements, &itemParts)
		if len(itemParts) > 0 {
			*parts = append(*parts, "- "+strings.Join(itemParts, " "))
		}
	}
}

// parseMergeForward fetches sub-messages of a merge_forward message via the
// GET /open-apis/im/v1/messages/{message_id} API, then formats them into
// readable text. Returns combined text, images, and files from the sub-messages.
func (p *Platform) parseMergeForward(rootMessageID string) (string, []core.ImageAttachment, []core.FileAttachment) {
	resp, err := p.client.Im.Message.Get(context.Background(),
		larkim.NewGetMessageReqBuilder().
			MessageId(rootMessageID).
			Build())
	if err != nil {
		slog.Error(p.tag()+": fetch merge_forward sub-messages failed", "error", err)
		return "", nil, nil
	}
	if !resp.Success() {
		slog.Error(p.tag()+": fetch merge_forward sub-messages failed", "code", resp.Code, "msg", resp.Msg)
		return "", nil, nil
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		slog.Warn(p.tag()+": merge_forward has no sub-messages", "message_id", rootMessageID)
		return "", nil, nil
	}

	items := resp.Data.Items
	slog.Info(p.tag()+": merge_forward sub-messages fetched", "message_id", rootMessageID, "count", len(items))

	// Build tree: group children by upper_message_id, collect sender IDs
	childrenMap := make(map[string][]*larkim.Message)
	senderIDs := make(map[string]struct{})
	for _, item := range items {
		if item.MessageId != nil && *item.MessageId == rootMessageID {
			continue // skip root container
		}
		parentID := ""
		if item.UpperMessageId != nil {
			parentID = *item.UpperMessageId
		}
		if parentID == "" || parentID == rootMessageID {
			parentID = rootMessageID
		}
		childrenMap[parentID] = append(childrenMap[parentID], item)
		if item.Sender != nil && item.Sender.Id != nil {
			senderIDs[*item.Sender.Id] = struct{}{}
		}
	}

	// Resolve sender IDs to display names
	uniqueIDs := make([]string, 0, len(senderIDs))
	for id := range senderIDs {
		uniqueIDs = append(uniqueIDs, id)
	}
	nameMap := p.resolveUserNames(uniqueIDs)

	var allImages []core.ImageAttachment
	var allFiles []core.FileAttachment
	var sb strings.Builder
	sb.WriteString("<forwarded_messages>\n")
	p.formatMergeForwardTree(rootMessageID, childrenMap, nameMap, &sb, &allImages, &allFiles, 0)
	sb.WriteString("</forwarded_messages>")

	return sb.String(), allImages, allFiles
}

// replaceMentions replaces @_user_N placeholders with real names from the Mentions list.
func replaceMentions(text string, mentions []*larkim.Mention) string {
	for _, m := range mentions {
		if m.Key != nil && m.Name != nil {
			text = strings.ReplaceAll(text, *m.Key, "@"+*m.Name)
		}
	}
	return text
}

// formatMergeForwardTree recursively formats the sub-message tree.
func (p *Platform) formatMergeForwardTree(parentID string, childrenMap map[string][]*larkim.Message, nameMap map[string]string, sb *strings.Builder, images *[]core.ImageAttachment, files *[]core.FileAttachment, depth int) {
	if depth > 10 {
		sb.WriteString(strings.Repeat("    ", depth) + "[nested forwarding truncated]\n")
		return
	}
	children := childrenMap[parentID]
	indent := strings.Repeat("    ", depth)

	for _, item := range children {
		msgID := ""
		if item.MessageId != nil {
			msgID = *item.MessageId
		}
		msgType := ""
		if item.MsgType != nil {
			msgType = *item.MsgType
		}
		senderID := ""
		if item.Sender != nil && item.Sender.Id != nil {
			senderID = *item.Sender.Id
		}
		senderName := senderID
		if name, ok := nameMap[senderID]; ok {
			senderName = name
		}

		// Format timestamp
		ts := ""
		if item.CreateTime != nil {
			if ms, err := strconv.ParseInt(*item.CreateTime, 10, 64); err == nil {
				ts = time.Unix(ms/1000, 0).Format("2006-01-02 15:04:05")
			}
		}

		content := ""
		if item.Body != nil && item.Body.Content != nil {
			content = *item.Body.Content
		}

		switch msgType {
		case "text":
			var textBody struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(content), &textBody); err == nil && textBody.Text != "" {
				msgText := replaceMentions(textBody.Text, item.Mentions)
				sb.WriteString(fmt.Sprintf("%s[%s] %s:\n", indent, ts, senderName))
				for _, line := range strings.Split(msgText, "\n") {
					sb.WriteString(fmt.Sprintf("%s    %s\n", indent, line))
				}
			}

		case "post":
			textParts, postImages := p.parsePostContent(msgID, content)
			*images = append(*images, postImages...)
			text := replaceMentions(strings.Join(textParts, "\n"), item.Mentions)
			if text != "" {
				sb.WriteString(fmt.Sprintf("%s[%s] %s:\n", indent, ts, senderName))
				for _, line := range strings.Split(text, "\n") {
					sb.WriteString(fmt.Sprintf("%s    %s\n", indent, line))
				}
			}

		case "image":
			var imgBody struct {
				ImageKey string `json:"image_key"`
			}
			if err := json.Unmarshal([]byte(content), &imgBody); err == nil && imgBody.ImageKey != "" {
				imgData, mimeType, err := p.downloadImage(msgID, imgBody.ImageKey)
				if err != nil {
					slog.Error(p.tag()+": download merge_forward image failed", "error", err)
					sb.WriteString(fmt.Sprintf("%s[%s] %s: [image - download failed]\n", indent, ts, senderName))
				} else {
					*images = append(*images, core.ImageAttachment{MimeType: mimeType, Data: imgData})
					sb.WriteString(fmt.Sprintf("%s[%s] %s: [image]\n", indent, ts, senderName))
				}
			}

		case "file":
			var fileBody struct {
				FileKey  string `json:"file_key"`
				FileName string `json:"file_name"`
			}
			if err := json.Unmarshal([]byte(content), &fileBody); err == nil && fileBody.FileKey != "" {
				fileData, err := p.downloadResource(msgID, fileBody.FileKey, "file")
				if err != nil {
					slog.Error(p.tag()+": download merge_forward file failed", "error", err)
					sb.WriteString(fmt.Sprintf("%s[%s] %s: [file: %s - download failed]\n", indent, ts, senderName, fileBody.FileName))
				} else {
					mt := detectMimeType(fileData)
					*files = append(*files, core.FileAttachment{MimeType: mt, Data: fileData, FileName: fileBody.FileName})
					sb.WriteString(fmt.Sprintf("%s[%s] %s: [file: %s]\n", indent, ts, senderName, fileBody.FileName))
				}
			}

		case "merge_forward":
			sb.WriteString(fmt.Sprintf("%s[%s] %s: [forwarded messages]\n", indent, ts, senderName))
			p.formatMergeForwardTree(msgID, childrenMap, nameMap, sb, images, files, depth+1)

		default:
			sb.WriteString(fmt.Sprintf("%s[%s] %s: [%s message]\n", indent, ts, senderName, msgType))
		}
	}
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	content = p.resolveMentionsInContent(ctx, rc.chatID, content)
	msgType, msgBody := buildReplyContent(content)

	if !p.shouldUseThreadOrReplyAPI(rc) {
		return p.sendNewMessageToChat(ctx, rc, msgType, msgBody)
	}
	return p.replyMessage(ctx, rc, msgType, msgBody)
}

// Send sends a message. When the original message ID is available, the message
// is sent as a reply (quoting the original) so the conversation stays threaded.
// Falls back to creating a standalone message when no message ID exists.
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	if p.shouldUseThreadOrReplyAPI(rc) {
		return p.Reply(ctx, rctx, content)
	}

	content = p.resolveMentionsInContent(ctx, rc.chatID, content)
	msgType, msgBody := buildReplyContent(content)
	return p.sendNewMessageToChat(ctx, rc, msgType, msgBody)
}

func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendImage: invalid reply context type %T", p.tag(), rctx)
	}

	var uploadResp *larkim.CreateImageResp
	if err := p.withTransientRetry(ctx, "upload image", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "upload image", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			req := larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType("message").
					Image(bytes.NewReader(img.Data)).
					Build()).
				Build()
			var err error
			uploadResp, err = client.Im.Image.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: upload image: %w", p.tag(), err)
			}
			if !uploadResp.Success() {
				return fmt.Errorf("%s: upload image code=%d msg=%s", p.tag(), uploadResp.Code, uploadResp.Msg)
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
		return fmt.Errorf("%s: upload image: no image_key returned", p.tag())
	}

	imageContent, err := (&larkim.MessageImage{ImageKey: *uploadResp.Data.ImageKey}).String()
	if err != nil {
		return fmt.Errorf("%s: build image message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeImage, imageContent)
}

func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendFile: invalid reply context type %T", p.tag(), rctx)
	}

	fileName := file.FileName
	if fileName == "" {
		fileName = "attachment"
	}
	fileType := detectFeishuFileType(file.MimeType, fileName)
	var uploadResp *larkim.CreateFileResp
	if err := p.withTransientRetry(ctx, "upload file", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "upload file", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			req := larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(fileType).
					FileName(fileName).
					File(bytes.NewReader(file.Data)).
					Build()).
				Build()
			var err error
			uploadResp, err = client.Im.File.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: upload file: %w", p.tag(), err)
			}
			if !uploadResp.Success() {
				return fmt.Errorf("%s: upload file code=%d msg=%s", p.tag(), uploadResp.Code, uploadResp.Msg)
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("%s: upload file: no file_key returned", p.tag())
	}

	fileContent, err := (&larkim.MessageFile{FileKey: *uploadResp.Data.FileKey}).String()
	if err != nil {
		return fmt.Errorf("%s: build file message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeFile, fileContent)
}

func (p *Platform) sendMediaMessage(ctx context.Context, rc replyContext, msgType, content string) error {
	if p.shouldUseThreadOrReplyAPI(rc) {
		return p.replyMessage(ctx, rc, msgType, content)
	}
	return p.createMessage(ctx, rc.chatID, msgType, content, "send media message")
}

func detectFeishuFileType(mimeType, fileName string) string {
	name := strings.ToLower(fileName)
	switch {
	case mimeType == "application/pdf" || strings.HasSuffix(name, ".pdf"):
		return larkim.FileTypePdf
	case strings.HasSuffix(name, ".doc") || strings.HasSuffix(name, ".docx"):
		return larkim.FileTypeDoc
	case strings.HasSuffix(name, ".xls") || strings.HasSuffix(name, ".xlsx") || strings.HasSuffix(name, ".csv"):
		return larkim.FileTypeXls
	case strings.HasSuffix(name, ".ppt") || strings.HasSuffix(name, ".pptx"):
		return larkim.FileTypePpt
	case mimeType == "video/mp4" || strings.HasSuffix(name, ".mp4"):
		return larkim.FileTypeMp4
	case mimeType == "audio/ogg" || mimeType == "audio/opus" || strings.HasSuffix(name, ".opus"):
		return larkim.FileTypeOpus
	default:
		return larkim.FileTypeStream
	}
}

func (p *Platform) downloadImage(messageID, imageKey string) ([]byte, string, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build())
	if err != nil {
		return nil, "", fmt.Errorf("%s: image API: %w", p.tag(), err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("%s: image API code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return nil, "", fmt.Errorf("%s: image API returned nil file body", p.tag())
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("%s: read image: %w", p.tag(), err)
	}

	mimeType := detectMimeType(data)
	slog.Debug(p.tag()+": downloaded image", "key", imageKey, "size", len(data), "mime", mimeType)
	return data, mimeType, nil
}

func (p *Platform) downloadResource(messageID, fileKey, resType string) ([]byte, error) {
	resp, err := p.client.Im.MessageResource.Get(context.Background(),
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type(resType).
			Build())
	if err != nil {
		return nil, fmt.Errorf("%s: resource API: %w", p.tag(), err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("%s: resource API code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return nil, fmt.Errorf("%s: resource API returned nil file body", p.tag())
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, fmt.Errorf("%s: read resource: %w", p.tag(), err)
	}
	slog.Debug(p.tag()+": downloaded resource", "key", fileKey, "type", resType, "size", len(data))
	return data, nil
}

func detectMimeType(data []byte) string {
	if len(data) >= 8 {
		if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
			return "image/png"
		}
		if data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
		if string(data[:4]) == "GIF8" {
			return "image/gif"
		}
		if string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
			return "image/webp"
		}
	}
	return "image/png"
}

// predictMsgType returns the message type that buildReplyContent will choose,
// without actually building the content. Used to select the correct at syntax
// before building.
func predictMsgType(content string) string {
	if !containsMarkdown(content) {
		return larkim.MsgTypeText
	}
	if countMarkdownTables(content) <= maxCardTables {
		return larkim.MsgTypeInteractive
	}
	return larkim.MsgTypePost
}

func buildReplyContent(content string) (msgType string, body string) {
	if !containsMarkdown(content) {
		b, _ := json.Marshal(map[string]string{"text": content})
		return larkim.MsgTypeText, string(b)
	}
	// Prefer card for all markdown content — card schema 2.0 has the best
	// markdown rendering (headings, blockquotes, code blocks, tables, links,
	// strikethrough, etc.). Only fall back to post md tag when the content
	// exceeds the card table limit (Feishu API error 11310: max 5 tables).
	if countMarkdownTables(content) > maxCardTables {
		return larkim.MsgTypePost, buildPostMdJSON(content)
	}
	return larkim.MsgTypeInteractive, buildCardJSON(sanitizeMarkdownURLs(preprocessFeishuMarkdown(content)))
}

// hasComplexMarkdown detects code blocks or tables that require card rendering.
func hasComplexMarkdown(s string) bool {
	if strings.Contains(s, "```") {
		return true
	}
	// Table: line starting and ending with |
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 1 && trimmed[0] == '|' && trimmed[len(trimmed)-1] == '|' {
			return true
		}
	}
	return false
}

// maxCardTables is the Feishu interactive card limit for table components.
// A single card supports at most 5 tables; exceeding this causes API error 11310.
const maxCardTables = 5

// countMarkdownTables counts the number of distinct markdown tables in s.
// A table is a group of consecutive lines where each line starts and ends with '|'.
func countMarkdownTables(s string) int {
	count := 0
	inTable := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		isTableLine := len(trimmed) > 1 && trimmed[0] == '|' && trimmed[len(trimmed)-1] == '|'
		if isTableLine && !inTable {
			count++
			inTable = true
		} else if !isTableLine {
			inTable = false
		}
	}
	return count
}

// buildPostMdJSON builds a Feishu post message using the md tag,
// which renders markdown at normal chat font size.
func buildPostMdJSON(content string) string {
	content = sanitizeMarkdownURLs(content)
	post := map[string]any{
		"zh_cn": map[string]any{
			"content": [][]map[string]any{
				{
					{"tag": "md", "text": content},
				},
			},
		},
	}
	b, _ := json.Marshal(post)
	return string(b)
}

// preprocessFeishuMarkdown ensures code fences have a newline before them,
// which prevents rendering issues in Feishu card markdown.
// Tables, headings, blockquotes, etc. are rendered natively by the card markdown element.
func preprocessFeishuMarkdown(md string) string {
	// Ensure ``` has a newline before it (unless at start of text)
	var b strings.Builder
	b.Grow(len(md) + 32)
	for i := 0; i < len(md); i++ {
		if i > 0 && md[i] == '`' && i+2 < len(md) && md[i+1] == '`' && md[i+2] == '`' && md[i-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteByte(md[i])
	}
	return b.String()
}

var markdownIndicators = []string{
	"```", "**", "~~", "`", "\n- ", "\n* ", "\n1. ", "\n# ", "---",
}

func containsMarkdown(s string) bool {
	for _, ind := range markdownIndicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

// buildPostJSON converts markdown content to Feishu post (rich text) format.
func buildPostJSON(content string) string {
	lines := strings.Split(content, "\n")
	var postLines [][]map[string]any
	inCodeBlock := false
	var codeLines []string
	codeLang := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeLang = strings.TrimPrefix(trimmed, "```")
				codeLines = nil
			} else {
				inCodeBlock = false
				postLines = append(postLines, []map[string]any{{
					"tag":      "code_block",
					"language": codeLang,
					"text":     strings.Join(codeLines, "\n"),
				}})
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Convert # headers to bold
		headerLine := line
		for level := 6; level >= 1; level-- {
			prefix := strings.Repeat("#", level) + " "
			if strings.HasPrefix(line, prefix) {
				headerLine = "**" + strings.TrimPrefix(line, prefix) + "**"
				break
			}
		}

		elements := parseInlineMarkdown(headerLine)
		if len(elements) > 0 {
			postLines = append(postLines, elements)
		} else {
			postLines = append(postLines, []map[string]any{{"tag": "text", "text": ""}})
		}
	}

	// Handle unclosed code block
	if inCodeBlock && len(codeLines) > 0 {
		postLines = append(postLines, []map[string]any{{
			"tag":      "code_block",
			"language": codeLang,
			"text":     strings.Join(codeLines, "\n"),
		}})
	}

	post := map[string]any{
		"zh_cn": map[string]any{
			"content": postLines,
		},
	}
	b, _ := json.Marshal(post)
	return string(b)
}

// isValidFeishuHref checks whether a URL is acceptable as a Feishu post href.
// Feishu rejects non-HTTP(S) URLs with "invalid href" (code 230001).
func isValidFeishuHref(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// sanitizeMarkdownURLs rewrites markdown links with non-HTTP(S) schemes
// to plain text, preventing Feishu API rejection (code 230001).
func sanitizeMarkdownURLs(md string) string {
	return mdLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		if isValidFeishuHref(parts[2]) {
			return match
		}
		// Convert invalid-scheme link to "text (url)" plain text
		return parts[1] + " (" + parts[2] + ")"
	})
}

// parseInlineMarkdown parses a single line of markdown into Feishu post elements.
// Supports **bold** and `code` inline formatting.
func parseInlineMarkdown(line string) []map[string]any {
	type markerDef struct {
		pattern string
		tag     string
		style   string // for text elements with style
	}
	markers := []markerDef{
		{pattern: "**", tag: "text", style: "bold"},
		{pattern: "~~", tag: "text", style: "lineThrough"},
		{pattern: "`", tag: "text", style: "code"},
		{pattern: "*", tag: "text", style: "italic"},
	}

	var elements []map[string]any
	remaining := line

	for len(remaining) > 0 {
		// Check for link [text](url)
		linkIdx := strings.Index(remaining, "[")
		if linkIdx >= 0 {
			parenClose := -1
			bracketClose := strings.Index(remaining[linkIdx:], "](")
			if bracketClose >= 0 {
				bracketClose += linkIdx
				parenClose = strings.Index(remaining[bracketClose+2:], ")")
				if parenClose >= 0 {
					parenClose += bracketClose + 2
				}
			}
			if parenClose >= 0 {
				// Check if any marker comes before this link
				foundEarlierMarker := false
				for _, m := range markers {
					idx := strings.Index(remaining, m.pattern)
					if idx >= 0 && idx < linkIdx {
						foundEarlierMarker = true
						break
					}
				}
				if !foundEarlierMarker {
					linkText := remaining[linkIdx+1 : bracketClose]
					linkURL := remaining[bracketClose+2 : parenClose]
					if isValidFeishuHref(linkURL) {
						if linkIdx > 0 {
							elements = append(elements, map[string]any{"tag": "text", "text": remaining[:linkIdx]})
						}
						elements = append(elements, map[string]any{
							"tag":  "a",
							"text": linkText,
							"href": linkURL,
						})
						remaining = remaining[parenClose+1:]
						continue
					}
				}
			}
		}

		// Find the earliest formatting marker
		bestIdx := -1
		var bestMarker markerDef
		for _, m := range markers {
			idx := strings.Index(remaining, m.pattern)
			if idx < 0 {
				continue
			}
			// For single * marker, skip if it's actually ** (bold)
			if m.pattern == "*" && idx+1 < len(remaining) && remaining[idx+1] == '*' {
				idx = findSingleAsterisk(remaining)
				if idx < 0 {
					continue
				}
			}
			if bestIdx < 0 || idx < bestIdx {
				bestIdx = idx
				bestMarker = m
			}
		}

		if bestIdx < 0 {
			if remaining != "" {
				elements = append(elements, map[string]any{"tag": "text", "text": remaining})
			}
			break
		}

		if bestIdx > 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": remaining[:bestIdx]})
		}
		remaining = remaining[bestIdx+len(bestMarker.pattern):]

		closeIdx := strings.Index(remaining, bestMarker.pattern)
		// For single *, make sure we don't match ** as close
		if bestMarker.pattern == "*" {
			closeIdx = findSingleAsterisk(remaining)
		}
		if closeIdx < 0 {
			elements = append(elements, map[string]any{"tag": "text", "text": bestMarker.pattern + remaining})
			remaining = ""
			break
		}

		inner := remaining[:closeIdx]
		remaining = remaining[closeIdx+len(bestMarker.pattern):]

		elements = append(elements, map[string]any{
			"tag":   bestMarker.tag,
			"text":  inner,
			"style": []string{bestMarker.style},
		})
	}

	return elements
}

// findSingleAsterisk finds the index of a single '*' not part of '**' in s.
func findSingleAsterisk(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			if i+1 < len(s) && s[i+1] == '*' {
				i++ // skip **
				continue
			}
			return i
		}
	}
	return -1
}

// fetchBotOpenID retrieves the bot's open_id via the Feishu bot info API.
func (p *Platform) fetchBotOpenID() (string, error) {
	resp, err := p.client.Get(context.Background(),
		"/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("api code=%d", result.Code)
	}
	return result.Bot.OpenID, nil
}

func isBotMentioned(mentions []*larkim.MentionEvent, botOpenID string) bool {
	for _, m := range mentions {
		if m.Id != nil && m.Id.OpenId != nil && *m.Id.OpenId == botOpenID {
			return true
		}
	}
	return false
}

// stripMentions processes @mention placeholders (e.g. @_user_1) in text.
// The bot's own mention is removed; other user mentions are replaced with
// their display name so the agent can see who was referenced.
func stripMentions(text string, mentions []*larkim.MentionEvent, botOpenID string) string {
	if len(mentions) == 0 {
		return text
	}
	for _, m := range mentions {
		if m.Key == nil {
			continue
		}
		if botOpenID != "" && m.Id != nil && m.Id.OpenId != nil && *m.Id.OpenId == botOpenID {
			text = strings.ReplaceAll(text, *m.Key, "")
		} else if m.Name != nil && *m.Name != "" {
			text = strings.ReplaceAll(text, *m.Key, "@"+*m.Name)
		} else {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	return strings.TrimSpace(text)
}

// TODO: Session-key derivation and reply-thread behavior are split across multiple code paths here.
// Should revisit thread/root handling without changing thread_isolation=false behavior.
func (p *Platform) makeSessionKey(msg *larkim.EventMessage, chatID, userID string) string {
	if p.threadIsolation && msg != nil && stringValue(msg.ChatType) == "group" {
		rootID := stringValue(msg.RootId)
		if rootID == "" {
			rootID = stringValue(msg.MessageId)
		}
		if rootID != "" {
			return fmt.Sprintf("%s:%s:root:%s", p.tag(), chatID, rootID)
		}
	}
	if p.shareSessionInChannel {
		return fmt.Sprintf("%s:%s", p.tag(), chatID)
	}
	return fmt.Sprintf("%s:%s:%s", p.tag(), chatID, userID)
}

func (p *Platform) sessionKeyFromCardAction(chatID, userID string, value map[string]any) string {
	if value != nil {
		if sessionKey, _ := value["session_key"].(string); sessionKey != "" {
			return sessionKey
		}
	}
	if p.shareSessionInChannel {
		return fmt.Sprintf("%s:%s", p.tag(), chatID)
	}
	return fmt.Sprintf("%s:%s:%s", p.tag(), chatID, userID)
}

func (p *Platform) shouldReplyInThread(rc replyContext) bool {
	if rc.messageID == "" {
		return false
	}
	return p.threadIsolation && isThreadSessionKey(rc.sessionKey)
}

// shouldUseThreadOrReplyAPI is true when we should call Im.Message.Reply (optionally with ReplyInThread).
func (p *Platform) shouldUseThreadOrReplyAPI(rc replyContext) bool {
	if rc.messageID == "" {
		return false
	}
	return !p.noReplyToTrigger
}

func (p *Platform) sendNewMessageToChat(ctx context.Context, rc replyContext, msgType, content string) error {
	if rc.chatID == "" {
		return fmt.Errorf("%s: chatID is empty, cannot send new message", p.tag())
	}
	return p.createMessage(ctx, rc.chatID, msgType, content, "send")
}

func (p *Platform) buildReplyMessageReqBody(rc replyContext, msgType, content string) *larkim.ReplyMessageReqBody {
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType(msgType).
		Content(content)
	if p.shouldReplyInThread(rc) {
		body.ReplyInThread(true)
	}
	return body.Build()
}

func (p *Platform) replyMessage(ctx context.Context, rc replyContext, msgType, content string) error {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rc.messageID).
		Body(p.buildReplyMessageReqBody(rc, msgType, content)).
		Build()
	return p.withTransientRetry(ctx, "reply", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "reply", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Reply(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: reply api call: %w", p.tag(), err)
			}
			if !resp.Success() {
				return fmt.Errorf("%s: reply failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
			}
			return nil
		})
	})
}

func (p *Platform) createMessage(ctx context.Context, chatID, msgType, content, op string) error {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()
	return p.withTransientRetry(ctx, op, func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, op, func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: %s api call: %w", p.tag(), op, err)
			}
			if !resp.Success() {
				return fmt.Errorf("%s: %s failed code=%d msg=%s", p.tag(), op, resp.Code, resp.Msg)
			}
			return nil
		})
	})
}

func (p *Platform) withFreshTenantAccessTokenRetry(ctx context.Context, operation string, fn feishuRequestFunc) error {
	err := fn(p.client)
	if !isTenantAccessTokenInvalid(err) {
		return err
	}

	freshToken, refreshErr := p.fetchFreshTenantAccessToken(ctx)
	if refreshErr != nil {
		return fmt.Errorf("%s: %s failed after token refresh attempt: %w (original error: %v)", p.tag(), operation, refreshErr, err)
	}

	slog.Warn(p.tag()+": retrying request with fresh tenant access token", "operation", operation)
	return fn(p.replayAPIClient(), larkcore.WithTenantAccessToken(freshToken))
}

func (p *Platform) fetchFreshTenantAccessToken(ctx context.Context) (string, error) {
	resp, err := p.replayAPIClient().GetTenantAccessTokenBySelfBuiltApp(ctx, &larkcore.SelfBuiltTenantAccessTokenReq{
		AppID:     p.appID,
		AppSecret: p.appSecret,
	})
	if err != nil {
		return "", fmt.Errorf("%s: fetch tenant access token: %w", p.tag(), err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s: fetch tenant access token code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.TenantAccessToken) == "" {
		return "", fmt.Errorf("%s: fetch tenant access token returned empty token", p.tag())
	}
	return resp.TenantAccessToken, nil
}

func (p *Platform) replayAPIClient() *lark.Client {
	p.replayClientMu.Lock()
	defer p.replayClientMu.Unlock()
	if p.replayClient == nil {
		p.replayClient = newFeishuReplayClient(p.appID, p.appSecret, p.domain)
	}
	return p.replayClient
}

func newFeishuReplayClient(appID, appSecret, domain string) *lark.Client {
	var opts []lark.ClientOptionFunc
	opts = append(opts, lark.WithEnableTokenCache(false))
	if domain != "" && domain != lark.FeishuBaseUrl {
		opts = append(opts, lark.WithOpenBaseUrl(domain))
	}
	return lark.NewClient(appID, appSecret, opts...)
}

func isTenantAccessTokenInvalid(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "99991663") || strings.Contains(msg, "invalid access token")
}

// Transient retry constants for network-level failures.
const (
	maxTransientRetries    = 3
	transientRetryInitial  = 500 * time.Millisecond
	transientRetryMaxDelay = 5 * time.Second
)

// isTransientError returns true if the error is a transient network error
// that warrants a retry (connection reset, timeout, EOF, etc.).
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	// Typed syscall checks — more robust than string matching.
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// net.Error covers timeouts and temporary errors from the stdlib.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// EOF usually means the server closed the connection mid-response.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Unwrapped string checks for common transient symptoms that may
	// appear in wrapped Feishu SDK errors.
	msg := err.Error()
	for _, substr := range []string{
		"connection reset by peer",
		"broken pipe",
		"i/o timeout",
		"TLS handshake timeout",
		"server misbehaving",
		"connection refused",
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// withTransientRetry wraps an operation with exponential-backoff retry on
// transient network errors. Non-transient errors are returned immediately.
// Jitter (up to +25% of delay) is added to prevent thundering-herd retries.
func (p *Platform) withTransientRetry(ctx context.Context, operation string, fn func() error) error {
	var lastErr error
	delay := transientRetryInitial
	for attempt := 0; attempt <= maxTransientRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			if attempt > 0 {
				slog.Info(p.tag()+": transient retry succeeded",
					"operation", operation,
					"attempt", attempt+1,
				)
			}
			return nil
		}
		if !isTransientError(lastErr) {
			return lastErr
		}
		if attempt == maxTransientRetries {
			break
		}
		// Add jitter: up to +25% of delay to spread out concurrent retries.
		jitter := time.Duration(rand.Int64N(int64(delay / 4)))
		actualDelay := delay + jitter
		slog.Warn(p.tag()+": transient error, retrying",
			"operation", operation,
			"attempt", attempt+1,
			"max_retries", maxTransientRetries,
			"delay", actualDelay,
			"error", lastErr,
		)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %s retry cancelled: %w (last error: %v)", p.tag(), operation, ctx.Err(), lastErr)
		case <-time.After(actualDelay):
		}
		delay = min(delay*2, transientRetryMaxDelay)
	}
	return fmt.Errorf("%s failed after %d retries: %w", operation, maxTransientRetries, lastErr)
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// {platformName}:{chatID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != p.platformName {
		return nil, fmt.Errorf("%s: invalid session key %q", p.tag(), sessionKey)
	}
	rc := replyContext{chatID: parts[1], sessionKey: sessionKey}
	if len(parts) == 3 {
		if rootID, ok := parseThreadRootID(parts[2]); ok {
			rc.messageID = rootID
		}
	}
	return rc, nil
}

func parseThreadRootID(sessionTail string) (string, bool) {
	for _, prefix := range []string{"root:", "thread:"} {
		if strings.HasPrefix(sessionTail, prefix) {
			rootID := strings.TrimPrefix(sessionTail, prefix)
			if rootID != "" {
				return rootID, true
			}
			return "", false
		}
	}
	return "", false
}

func isThreadSessionKey(sessionKey string) bool {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) != 3 {
		return false
	}
	_, ok := parseThreadRootID(parts[2])
	return ok
}

// feishuPreviewHandle stores the message ID for an editable preview message.
type feishuPreviewHandle struct {
	messageID string
	chatID    string
}

// buildCardJSON builds a Feishu interactive card JSON string with a markdown element.
// Uses schema 2.0 which supports code blocks, tables, and inline formatting.
// Card font is inherently smaller than Post/Text — this is a Feishu platform limitation.
func buildCardJSON(content string) string {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

func isZhLikeProgressLang(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return strings.HasPrefix(l, "zh")
}

func progressAgentLabel(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return "Agent"
	}
	return agent
}

func progressStateMeta(state core.ProgressCardState, lang string, agent string) (title string, template string, footer string) {
	zh := isZhLikeProgressLang(lang)
	switch state {
	case core.ProgressCardStateCompleted:
		if zh {
			return fmt.Sprintf("%s · 已完成", agent), "green", "本过程卡片已停止更新，完整答复见下一条消息。"
		}
		return fmt.Sprintf("%s · Completed", agent), "green", "This progress card is no longer updating. Full response is in the next message."
	case core.ProgressCardStateFailed:
		if zh {
			return fmt.Sprintf("%s · 失败", agent), "red", "本过程卡片已停止更新（失败），完整错误说明见下一条消息。"
		}
		return fmt.Sprintf("%s · Failed", agent), "red", "This progress card has stopped (failed). See the next message for details."
	default:
		if zh {
			return fmt.Sprintf("%s · 进行中", agent), "blue", ""
		}
		return fmt.Sprintf("%s · Running", agent), "blue", ""
	}
}

func progressKindLabel(kind core.ProgressCardEntryKind, lang string) string {
	zh := isZhLikeProgressLang(lang)
	switch kind {
	case core.ProgressEntryThinking:
		if zh {
			return "思考"
		}
		return "Thinking"
	case core.ProgressEntryToolUse:
		if zh {
			return "工具调用"
		}
		return "Tool"
	case core.ProgressEntryToolResult:
		if zh {
			return "工具结果"
		}
		return "Result"
	case core.ProgressEntryError:
		if zh {
			return "错误"
		}
		return "Error"
	default:
		if zh {
			return "更新"
		}
		return "Update"
	}
}

func normalizeProgressItems(payload *core.ProgressCardPayload) []core.ProgressCardEntry {
	if payload == nil {
		return nil
	}
	if len(payload.Items) > 0 {
		return payload.Items
	}
	out := make([]core.ProgressCardEntry, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kind := core.ProgressEntryInfo
		switch {
		case strings.HasPrefix(entry, "💭"):
			kind = core.ProgressEntryThinking
		case strings.HasPrefix(entry, "🔧"), strings.Contains(entry, "**Tool #"):
			kind = core.ProgressEntryToolUse
		case strings.HasPrefix(entry, "🧾"):
			kind = core.ProgressEntryToolResult
		case strings.HasPrefix(entry, "❌"):
			kind = core.ProgressEntryError
		}
		out = append(out, core.ProgressCardEntry{Kind: kind, Text: entry})
	}
	return out
}

func inlineCodeText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "`", "'")
}

func isBashToolName(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "shell", "run_shell_command":
		return true
	default:
		return false
	}
}

func isTodoWriteToolName(toolName string) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "todowrite")
}

// todoItem represents a single todo item from TodoWrite tool input.
type todoItem struct {
	ActiveForm string `json:"activeForm"`
	Content    string `json:"content"`
	Status     string `json:"status"`
}

// todoWriteInput represents the TodoWrite tool input structure.
type todoWriteInput struct {
	Todos []todoItem `json:"todos"`
}

// formatTodoWriteInput formats TodoWrite JSON input into a readable markdown list.
// Returns empty string if parsing fails or input is invalid.
func formatTodoWriteInput(text string, lang string) string {
	var input todoWriteInput
	if err := json.Unmarshal([]byte(text), &input); err != nil {
		return "" // Fall back to default formatting
	}
	if len(input.Todos) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, todo := range input.Todos {
		var icon string
		switch strings.ToLower(strings.TrimSpace(todo.Status)) {
		case "completed":
			icon = "✅"
		case "in_progress":
			icon = "🔄"
		case "pending":
			icon = "⏳"
		default:
			icon = "•"
		}

		content := strings.TrimSpace(todo.Content)
		if content == "" {
			continue
		}

		// Escape markdown special characters
		content = strings.ReplaceAll(content, "`", "'")

		sb.WriteString(icon)
		sb.WriteString(" ")
		sb.WriteString(content)

		activeForm := strings.TrimSpace(todo.ActiveForm)
		if activeForm != "" && activeForm != content {
			sb.WriteString(" _(")
			sb.WriteString(strings.ReplaceAll(activeForm, "`", "'"))
			sb.WriteString(")_")
		}
		sb.WriteString("\n")
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

func formatProgressToolInput(toolName, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Special handling for TodoWrite tool - format JSON as readable list
	if isTodoWriteToolName(toolName) {
		if formatted := formatTodoWriteInput(text, ""); formatted != "" {
			return formatted
		}
		// JSON parsing failed or empty todos - show raw input as text block
		return fmt.Sprintf("```text\n%s\n```", text)
	}

	text = preprocessFeishuMarkdown(sanitizeMarkdownURLs(text))
	if strings.Contains(text, "```") {
		return text
	}
	if isBashToolName(toolName) {
		return fmt.Sprintf("```bash\n%s\n```", text)
	}
	if strings.Contains(text, "\n") || len(text) > 180 {
		return fmt.Sprintf("```text\n%s\n```", text)
	}
	return fmt.Sprintf("`%s`", inlineCodeText(text))
}

func formatProgressToolResult(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = preprocessFeishuMarkdown(sanitizeMarkdownURLs(text))
	if strings.Contains(text, "```") {
		return text
	}
	if strings.Contains(text, "\n") || len(text) > 220 {
		return fmt.Sprintf("```\n%s\n```", text)
	}
	return text
}

func progressNoOutputText(lang string) string {
	if isZhLikeProgressLang(lang) {
		return "无输出"
	}
	return "No output"
}

func progressResultDot(item core.ProgressCardEntry) string {
	if item.Success != nil {
		if *item.Success {
			return "🟢"
		}
		return "🔴"
	}
	if item.ExitCode != nil {
		if *item.ExitCode == 0 {
			return "🟢"
		}
		return "🔴"
	}
	if strings.EqualFold(strings.TrimSpace(item.Status), "completed") || strings.EqualFold(strings.TrimSpace(item.Status), "success") || strings.EqualFold(strings.TrimSpace(item.Status), "succeeded") || strings.EqualFold(strings.TrimSpace(item.Status), "ok") {
		return "🟢"
	}
	if strings.EqualFold(strings.TrimSpace(item.Status), "failed") || strings.EqualFold(strings.TrimSpace(item.Status), "error") {
		return "🔴"
	}
	return "⚪"
}

func renderProgressEntryElement(item core.ProgressCardEntry, lang string) map[string]any {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		text = " "
	}
	switch item.Kind {
	case core.ProgressEntryThinking:
		return map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":        "plain_text",
				"content":    "💭 " + inlineCodeText(text),
				"text_size":  "notation",
				"text_color": "grey",
			},
		}
	case core.ProgressEntryToolUse:
		toolName := strings.TrimSpace(item.Tool)
		if toolName == "" {
			toolName = "Tool"
		}
		content := fmt.Sprintf("<text_tag color='blue'>%s</text_tag> `%s`", progressKindLabel(item.Kind, lang), inlineCodeText(toolName))
		if body := formatProgressToolInput(toolName, text); body != "" {
			content += "\n" + body
		}
		return map[string]any{
			"tag":     "markdown",
			"content": content,
		}
	case core.ProgressEntryToolResult:
		toolName := strings.TrimSpace(item.Tool)
		content := fmt.Sprintf("<text_tag color='turquoise'>%s</text_tag>", progressKindLabel(item.Kind, lang))
		if toolName != "" {
			content += " `" + inlineCodeText(toolName) + "`"
		}
		dot := progressResultDot(item)
		meta := dot
		if item.ExitCode != nil {
			meta += fmt.Sprintf(" exit code: `%d`", *item.ExitCode)
		}
		content += "\n" + meta
		if body := formatProgressToolResult(item.Text); body != "" {
			content += "\n" + body
		} else {
			content += "\n_" + progressNoOutputText(lang) + "_"
		}
		return map[string]any{
			"tag":     "markdown",
			"content": content,
		}
	case core.ProgressEntryError:
		content := fmt.Sprintf("<text_tag color='red'>%s</text_tag>\n%s", progressKindLabel(item.Kind, lang), preprocessFeishuMarkdown(sanitizeMarkdownURLs(text)))
		return map[string]any{
			"tag":     "markdown",
			"content": content,
		}
	default:
		return map[string]any{
			"tag":     "markdown",
			"content": preprocessFeishuMarkdown(sanitizeMarkdownURLs(text)),
		}
	}
}

func buildProgressCardJSONFromPayload(payload *core.ProgressCardPayload) string {
	items := normalizeProgressItems(payload)
	if len(items) == 0 {
		return buildCardJSON(" ")
	}

	agent := progressAgentLabel(payload.Agent)
	title, template, footer := progressStateMeta(payload.State, payload.Lang, agent)

	elements := make([]map[string]any, 0, len(items)+3)
	if payload.Truncated {
		truncatedText := "Showing latest updates only."
		if isZhLikeProgressLang(payload.Lang) {
			truncatedText = "仅显示最近更新。"
		}
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":        "plain_text",
				"content":    truncatedText,
				"text_size":  "notation",
				"text_color": "grey",
			},
		})
		elements = append(elements, map[string]any{"tag": "hr"})
	}

	for i, item := range items {
		elements = append(elements, renderProgressEntryElement(item, payload.Lang))
		if i < len(items)-1 {
			elements = append(elements, map[string]any{"tag": "hr"})
		}
	}
	if footer != "" {
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":        "plain_text",
				"content":    footer,
				"text_size":  "notation",
				"text_color": "grey",
			},
		})
	}

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
			"template": template,
		},
		"body": map[string]any{
			"elements": elements,
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

func buildPreviewCardJSON(content string) string {
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		return buildProgressCardJSONFromPayload(payload)
	}
	return buildCardJSON(sanitizeMarkdownURLs(content))
}

// SendPreviewStart sends a new card message and returns a handle for subsequent edits.
// Using card (interactive) type for both preview and final message so updates
// are in-place without needing to delete and resend.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	if !p.useInteractiveCard {
		return nil, core.ErrNotSupported
	}

	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	chatID := rc.chatID
	if chatID == "" {
		return nil, fmt.Errorf("%s: chatID is empty", p.tag())
	}

	cardJSON := buildPreviewCardJSON(content)

	var msgID string
	if p.shouldUseThreadOrReplyAPI(rc) {
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(rc.messageID).
			Body(p.buildReplyMessageReqBody(rc, larkim.MsgTypeInteractive, cardJSON)).
			Build()
		var resp *larkim.ReplyMessageResp
		if err := p.withTransientRetry(ctx, "send preview", func() error {
			return p.withFreshTenantAccessTokenRetry(ctx, "send preview", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
				var err error
				resp, err = client.Im.Message.Reply(ctx, req, options...)
				if err != nil {
					return fmt.Errorf("%s: send preview (reply): %w", p.tag(), err)
				}
				if !resp.Success() {
					return fmt.Errorf("%s: send preview (reply) code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
				}
				return nil
			})
		}); err != nil {
			return nil, err
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	} else {
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeInteractive).
				Content(cardJSON).
				Build()).
			Build()
		var resp *larkim.CreateMessageResp
		if err := p.withTransientRetry(ctx, "send preview", func() error {
			return p.withFreshTenantAccessTokenRetry(ctx, "send preview", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
				var err error
				resp, err = client.Im.Message.Create(ctx, req, options...)
				if err != nil {
					return fmt.Errorf("%s: send preview: %w", p.tag(), err)
				}
				if !resp.Success() {
					return fmt.Errorf("%s: send preview code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
				}
				return nil
			})
		}); err != nil {
			return nil, err
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
	}

	if msgID == "" {
		return nil, fmt.Errorf("%s: send preview: no message ID returned", p.tag())
	}

	return &feishuPreviewHandle{messageID: msgID, chatID: chatID}, nil
}

// UpdateMessage edits an existing card message identified by previewHandle.
// Uses the Patch API (HTTP PATCH) which is required for interactive card messages.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	if !p.useInteractiveCard {
		return core.ErrNotSupported
	}

	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("%s: invalid preview handle type %T", p.tag(), previewHandle)
	}

	cardJSON := ""
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		cardJSON = buildProgressCardJSONFromPayload(payload)
	} else {
		processed := content
		if containsMarkdown(content) {
			processed = preprocessFeishuMarkdown(content)
		}
		cardJSON = buildCardJSON(sanitizeMarkdownURLs(processed))
	}
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(h.messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build()
	return p.withTransientRetry(ctx, "patch message", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "patch message", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Patch(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: patch message: %w", p.tag(), err)
			}
			if !resp.Success() {
				return fmt.Errorf("%s: patch message code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
			}
			return nil
		})
	})
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	// Stop webhook server if running (Lark international version)
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.server.Shutdown(ctx); err != nil {
			slog.Error(p.tag()+": webhook server shutdown error", "error", err)
		}
	}
	return nil
}

// DeletePreviewMessage removes a preview message so the caller can send a
// separate final message without leaving a stale interactive card behind.
func (p *Platform) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	if !p.useInteractiveCard {
		return core.ErrNotSupported
	}

	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("%s: invalid preview handle type %T", p.tag(), previewHandle)
	}

	req := larkim.NewDeleteMessageReqBuilder().
		MessageId(h.messageID).
		Build()
	return p.withTransientRetry(ctx, "delete preview message", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "delete preview message", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Delete(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: delete preview message: %w", p.tag(), err)
			}
			if !resp.Success() {
				return fmt.Errorf("%s: delete preview message code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
			}
			return nil
		})
	})
}

// SendAudio uploads audio bytes to Feishu and sends a voice message.
// Implements core.AudioSender interface.
// Feishu audio messages require opus format; non-opus input is converted via ffmpeg.
func (p *Platform) SendAudio(ctx context.Context, rctx any, audio []byte, format string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendAudio: invalid reply context type %T", p.tag(), rctx)
	}

	if format != "opus" {
		converted, err := core.ConvertAudioToOpus(ctx, audio, format)
		if err != nil {
			return fmt.Errorf("%s: convert to opus: %w", p.tag(), err)
		}
		audio = converted
		format = "opus"
	}

	var uploadResp *larkim.CreateFileResp
	if err := p.withTransientRetry(ctx, "upload audio", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "upload audio", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			req := larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(larkim.FileTypeOpus).
					FileName("tts_audio.opus").
					File(bytes.NewReader(audio)).
					Build()).
				Build()
			var err error
			uploadResp, err = client.Im.File.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: upload audio: %w", p.tag(), err)
			}
			if !uploadResp.Success() {
				return fmt.Errorf("%s: upload audio code=%d msg=%s", p.tag(), uploadResp.Code, uploadResp.Msg)
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("%s: upload audio: no file_key returned", p.tag())
	}
	fileKey := *uploadResp.Data.FileKey

	slog.Debug(p.tag()+": audio uploaded", "file_key", fileKey, "format", format, "size", len(audio))

	audioMsg := larkim.MessageAudio{FileKey: fileKey}
	audioContent, err := audioMsg.String()
	if err != nil {
		return fmt.Errorf("%s: build audio message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeAudio, audioContent)
}

type postElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	Language string `json:"language,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
	Href     string `json:"href,omitempty"`
}

type postLang struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

// parsePostContent handles both formats of feishu post content:
// 1. {"title":"...", "content":[[...]]}  (receive event)
// 2. {"zh_cn":{"title":"...", "content":[[...]]}}  (some SDK versions)
func (p *Platform) parsePostContent(messageID, raw string) ([]string, []core.ImageAttachment) {
	// try flat format first
	var flat postLang
	if err := json.Unmarshal([]byte(raw), &flat); err == nil && flat.Content != nil {
		return p.extractPostParts(messageID, &flat)
	}
	// try language-keyed format
	var langMap map[string]postLang
	if err := json.Unmarshal([]byte(raw), &langMap); err == nil {
		for _, lang := range langMap {
			return p.extractPostParts(messageID, &lang)
		}
	}
	slog.Error(p.tag()+": failed to parse post content", "raw", raw)
	return nil, nil
}

func (p *Platform) extractPostParts(messageID string, post *postLang) ([]string, []core.ImageAttachment) {
	var textParts []string
	var images []core.ImageAttachment
	if post.Title != "" {
		textParts = append(textParts, post.Title)
	}
	for _, line := range post.Content {
		for _, elem := range line {
			switch elem.Tag {
			case "text":
				if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "a":
				if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "code_block":
				if elem.Text != "" {
					lang := elem.Language
					textParts = append(textParts, "```"+lang+"\n"+elem.Text+"\n```")
				}
			case "img":
				if elem.ImageKey != "" {
					imgData, mimeType, err := p.downloadImage(messageID, elem.ImageKey)
					if err != nil {
						slog.Error(p.tag()+": download post image failed", "error", err, "key", elem.ImageKey)
						continue
					}
					images = append(images, core.ImageAttachment{MimeType: mimeType, Data: imgData})
				}
			}
		}
	}
	return textParts, images
}

// onBotMenu handles bot custom menu click events. When a menu item's
// event_key starts with "/", it is dispatched as a slash command.
// This allows users to configure menu items in the Feishu developer
// console with event_key set to commands like "/help", "/status", etc.
func (p *Platform) onBotMenu(event *larkapplication.P2BotMenuV6) error {
	if event == nil || event.Event == nil || event.Event.EventKey == nil {
		return nil
	}
	eventKey := *event.Event.EventKey

	userID := ""
	if event.Event.Operator != nil && event.Event.Operator.OperatorId != nil && event.Event.Operator.OperatorId.OpenId != nil {
		userID = *event.Event.Operator.OperatorId.OpenId
	}
	if userID == "" {
		slog.Debug(p.tag()+": bot menu event without user id", "event_key", eventKey)
		return nil
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug(p.tag()+": menu event from unauthorized user", "user", userID, "event_key", eventKey)
		return nil
	}

	slog.Info(p.tag()+": bot menu clicked", "event_key", eventKey, "user", userID)

	content := eventKey
	if !strings.HasPrefix(content, "/") {
		content = "/" + content
	}

	userName := p.resolveUserName(userID)
	sessionKey := p.platformName + ":" + userID + ":" + userID

	p.handler(p.dispatchPlatform(), &core.Message{
		SessionKey: sessionKey,
		Platform:   p.platformName,
		Content:    content,
		UserID:     userID,
		UserName:   userName,
		ReplyCtx:   replyContext{chatID: userID, sessionKey: sessionKey},
	})
	return nil
}
