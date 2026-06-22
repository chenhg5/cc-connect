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
	"net/netip"
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
	mu                         sync.RWMutex
	platformName               string
	domain                     string
	appID                      string
	appSecret                  string
	progressStyle              string
	useInteractiveCard         bool
	self                       core.Platform
	reactionEmoji              string
	doneEmoji                  string
	allowFrom                  string
	allowChat                  string
	groupOnly                  bool
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
	dedup            *core.MessageDedup
	botOpenID        string
	peerBots         map[string]string // app_id -> friendly alias, for quoted-reply attribution
	userNameCache    sync.Map          // open_id -> display name
	chatNameCache    sync.Map          // chat_id -> chat name
	chatMemberCache  sync.Map          // chatID -> *chatMemberEntry
	recalledMu       sync.Mutex
	recalledMsgIDs   map[string]time.Time // message_id -> recall time, short TTL race guard
	// Webhook mode fields (for Lark international version)
	server       *http.Server
	port         string
	callbackPath string
	encryptKey   string
	eventHandler *dispatcher.EventDispatcher
	sharedGroup  *sharedWSGroup // non-nil when sharing WebSocket with other platforms
	isWSPrimary  bool           // true if this platform owns the shared WebSocket connection
	// cardActionMessageIDs tracks the most recent card-action messageID per
	// session key, enabling async card refreshes via the Patch API.
	cardActionMsgMu  sync.Mutex
	cardActionMsgIDs map[string]string // sessionKey → messageID
	// activeThreadSessions tracks thread sessionKeys that have already been
	// accepted by the bot. In group chats with thread_isolation, once a thread
	// has been engaged (the first @bot message), subsequent attachment-only
	// messages (image/file/audio) inside the same thread are passed through
	// without requiring another @bot mention. Value is the last-seen time so
	// stale entries can be expired by a future TTL sweep if needed.
	activeThreadSessions sync.Map // sessionKey -> time.Time

	richCardImageMu         sync.Mutex
	richCardImageResolved   map[string]string
	richCardImagePending    map[string]*richCardImageUpload
	richCardImageFailed     map[string]struct{}
	richCardImageUploadFunc func(context.Context, string) (string, error)

	// imageBatch coalesces consecutive image messages from the same session
	// arriving within imageBatchWindow. Without this, sending N images in rapid
	// succession from the Feishu mobile client (which posts each as a separate
	// message) caused the first (oldest create_time) image to be dropped by
	// core/engine's create_time watermark (PR #1168), so only N-1 images were
	// ever delivered to the agent (issue #1395).
	imageBatchMu sync.Mutex
	imageBatch   map[string]*imageBatchEntry
}

// imageBatchWindow is the quiet period after the last image in a session
// before the buffered batch is dispatched as a single multi-image message.
// 150ms comfortably covers Feishu mobile's batch-send timing (typically
// <100ms between images) while staying responsive for sequential single
// image sends.
const imageBatchWindow = 150 * time.Millisecond

// imageBatchEntry holds image data accumulated for one session while we wait
// to see if more images are coming. timer is stopped and replaced on every
// new image to coalesce the window; the pointer is captured by the timer
// callback so an in-flight (or stale) timer can detect that its entry has
// been superseded and exit without dispatching.
type imageBatchEntry struct {
	sessionKey   string
	userID       string
	userName     string
	chatName     string
	rctx         replyContext
	quoted       quotedMessage
	images       []core.ImageAttachment
	messageIDs   []string
	createTimeMs int64
	parentID     string
	timer        *time.Timer
}

type interactivePlatform struct {
	*Platform
}

type richCardImageUpload struct {
	done chan struct{}
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
	doneEmoji, _ := opts["done_emoji"].(string)
	if doneEmoji == "none" {
		doneEmoji = ""
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom(name, allowFrom)
	allowChat, _ := opts["allow_chat"].(string)
	groupOnly, _ := opts["group_only"].(bool)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	// require_mention = false is equivalent to group_reply_all = true:
	// both mean "respond to all group messages without needing an @mention".
	if v, ok := opts["require_mention"].(bool); ok && !v {
		groupReplyAll = true
	}
	respondToAtEveryoneAndHere, _ := opts["respond_to_at_everyone_and_here"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	threadIsolation, _ := opts["thread_isolation"].(bool)
	resolveMentionsOpt, _ := opts["resolve_mentions"].(bool)
	noReplyToTrigger := false
	if v, ok := opts["reply_to_trigger"].(bool); ok && !v {
		noReplyToTrigger = true
	}

	peerBots := map[string]string{}
	if raw, ok := opts["peer_bots"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				peerBots[k] = s
			}
		}
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
		doneEmoji:                  doneEmoji,
		allowFrom:                  allowFrom,
		allowChat:                  allowChat,
		groupOnly:                  groupOnly,
		groupReplyAll:              groupReplyAll,
		respondToAtEveryoneAndHere: respondToAtEveryoneAndHere,
		shareSessionInChannel:      shareSessionInChannel,
		threadIsolation:            threadIsolation,
		resolveMentions:            resolveMentionsOpt,
		noReplyToTrigger:           noReplyToTrigger,
		client:                     lark.NewClient(appID, appSecret, clientOpts...),
		replayClient:               newFeishuReplayClient(appID, appSecret, domain),
		dedup:                      &core.MessageDedup{},
		port:                       port,
		callbackPath:               callbackPath,
		encryptKey:                 encryptKey,
		peerBots:                   peerBots,
		imageBatch:                 make(map[string]*imageBatchEntry),
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

func (p *Platform) getHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) getCancel() context.CancelFunc {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cancel
}

func (p *Platform) getServer() *http.Server {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.server
}

func (p *Platform) getBotOpenID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.botOpenID
}

func (p *Platform) KeepPreviewOnFinish() bool {
	return p.useInteractiveCard
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()

	// In webhook mode (private/self-hosted Feishu/Lark), startup must not depend
	// on a successful bot-info API call. Older private deployments may not support
	// the same auth/bootstrap flow as the public SDK path, but the webhook server
	// can still receive events and operate correctly. We therefore only attempt
	// bot open_id discovery eagerly for WebSocket mode.
	if !p.shouldUseWebhookMode() {
		if openID, err := p.fetchBotOpenID(); err != nil {
			slog.Warn(p.platformName+": failed to get bot open_id, group chat filtering disabled", "error", err)
		} else {
			p.mu.Lock()
			p.botOpenID = openID
			p.mu.Unlock()
			slog.Info(p.platformName+": bot identified", "open_id", openID)
		}
	}

	// Register for shared WebSocket: multiple projects using the same app_id
	// share a single WebSocket connection to avoid Feishu's server-side
	// load-balancing which randomly routes messages across connections.
	group, isPrimary := registerSharedWS(p)
	p.sharedGroup = group
	p.isWSPrimary = isPrimary

	// Secondary platforms skip connection creation — the primary's connection
	// fans out events to all platforms in the shared group.
	if !isPrimary {
		return nil
	}

	p.eventHandler = dispatcher.NewEventDispatcher("", p.encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			// Fan out to all platforms sharing this WebSocket connection.
			// Each platform's onMessage applies its own allow_chat filter.
			for _, sibling := range p.sharedGroup.allPlatforms() {
				if err := sibling.onMessage(ctx, event); err != nil {
					slog.Error("shared ws: onMessage error", "err", err)
				}
			}
			return nil
		}).
		OnP2MessageRecalledV1(func(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
			for _, sibling := range p.sharedGroup.allPlatforms() {
				if err := sibling.onMessageRecalled(ctx, event); err != nil {
					slog.Error("shared ws: onMessageRecalled error", "err", err)
				}
			}
			return nil
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
			// Fan out card actions: try each platform, return first non-nil response.
			// Each platform's onCardAction checks allow_chat before processing.
			for _, sibling := range p.sharedGroup.allPlatforms() {
				resp, err := sibling.onCardAction(event)
				if err != nil {
					return nil, err
				}
				if resp != nil {
					return resp, nil
				}
			}
			return nil, nil
		}).
		OnP2BotMenuV6(func(ctx context.Context, event *larkapplication.P2BotMenuV6) error {
			for _, sibling := range p.sharedGroup.allPlatforms() {
				if err := sibling.onBotMenu(event); err != nil {
					slog.Error("shared ws: onBotMenu error", "err", err)
				}
			}
			return nil
		})

	if p.useInteractiveCard {
		slog.Info(p.platformName + ": interactive card mode enabled, ensure card.action.trigger event is subscribed in Feishu console")
	}

	if p.shouldUseWebhookMode() {
		return p.startWebhookMode()
	}

	return p.startWebSocketMode()
}

func (p *Platform) shouldUseWebhookMode() bool {
	return strings.TrimSpace(p.encryptKey) != ""
}

// startWebSocketMode starts the WebSocket long connection mode.
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
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()

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

	p.mu.Lock()
	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: mux,
	}

	_, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

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

	// Check allow_chat filter: skip card actions from chats this platform doesn't own.
	if event.Event.Context != nil && event.Event.Context.OpenChatID != "" {
		if !core.AllowList(p.allowChat, event.Event.Context.OpenChatID) {
			return nil, nil
		}
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
			done := make(chan *core.Card, 1)
			go func() {
				done <- p.cardNavHandler(actionVal, sessionKey)
			}()

			select {
			case card := <-done:
				if card != nil {
					return &callback.CardActionTriggerResponse{
						Card: &callback.Card{
							Type: "raw",
							Data: renderCardMap(card, sessionKey),
						},
					}, nil
				}
			case <-time.After(cardNavTimeout):
				go func() {
					card := <-done
					if card == nil {
						return
					}
					if refresher, ok := p.self.(core.CardRefresher); ok {
						if err := refresher.RefreshCard(context.Background(), sessionKey, card); err != nil {
							slog.Warn(p.tag()+": async card refresh failed", "action", actionVal, "err", err)
						}
					}
				}()
				return &callback.CardActionTriggerResponse{
					Toast: &callback.Toast{
						Type:    "info",
						Content: "⏳ Loading... / 加载中...",
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
		h := p.getHandler()
		go h(p.dispatchPlatform(), &core.Message{
			SessionKey:           sessionKey,
			Platform:             p.platformName,
			UserID:               userID,
			UserName:             p.resolveUserName(userID),
			ChatName:             p.resolveChatName(chatID),
			Content:              responseText,
			ReplyCtx:             rctx,
			IsPermissionResponse: true,
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
		h := p.getHandler()
		go h(p.dispatchPlatform(), &core.Message{
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

	// cmd: — async command dispatch, with optional in-place card replacement
	if strings.HasPrefix(actionVal, "cmd:") {
		cmdText := strings.TrimPrefix(actionVal, "cmd:")
		rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}

		slog.Info(p.tag()+": card action dispatched as command", "cmd", cmdText, "user", userID)

		h := p.getHandler()
		go h(p.dispatchPlatform(), &core.Message{
			SessionKey: sessionKey,
			Platform:   p.platformName,
			UserID:     userID,
			UserName:   p.resolveUserName(userID),
			ChatName:   p.resolveChatName(chatID),
			Content:    cmdText,
			ReplyCtx:   rctx,
		})

		if ac, ok := event.Event.Action.Value["after_click"].(map[string]any); ok {
			if title, _ := ac["title"].(string); title != "" {
				color, _ := ac["color"].(string)
				if color == "" {
					color = "green"
				}
				cb := core.NewCard().Title(title, color)
				if md, _ := ac["markdown"].(string); md != "" {
					cb.Markdown(md)
				}
				if linkText, _ := ac["link_text"].(string); linkText != "" {
					if linkURL, _ := ac["link_url"].(string); linkURL != "" {
						cb.Divider().Markdown("[" + linkText + "](" + linkURL + ")")
					}
				}
				return &callback.CardActionTriggerResponse{
					Card: &callback.Card{
						Type: "raw",
						Data: renderCardMap(cb.Build(), sessionKey),
					},
				}, nil
			}
		}
	}

	return nil, nil
}

func (p *Platform) addReaction(messageID string) string {
	return p.addReactionWithEmoji(messageID, p.reactionEmoji)
}

func (p *Platform) addReactionWithEmoji(messageID, emojiType string) string {
	if emojiType == "" {
		return ""
	}
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

// AddDoneReaction adds a "done" emoji reaction so the user gets a push
// notification when the agent finishes a multi-round turn in quiet mode.
func (p *Platform) AddDoneReaction(rctx any) {
	if p.doneEmoji == "" {
		return
	}
	rc, ok := rctx.(replyContext)
	if !ok || rc.messageID == "" {
		return
	}
	go p.addReactionWithEmoji(rc.messageID, p.doneEmoji)
}

const recalledMessageTTL = 10 * time.Minute

const cardNavTimeout = 2500 * time.Millisecond

func (p *Platform) markMessageRecalled(messageID string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}

	now := time.Now()
	p.recalledMu.Lock()
	defer p.recalledMu.Unlock()

	if p.recalledMsgIDs == nil {
		p.recalledMsgIDs = make(map[string]time.Time)
	}
	for id, markedAt := range p.recalledMsgIDs {
		if now.Sub(markedAt) > recalledMessageTTL {
			delete(p.recalledMsgIDs, id)
		}
	}
	p.recalledMsgIDs[messageID] = now
}

func (p *Platform) isMessageRecalled(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}

	now := time.Now()
	p.recalledMu.Lock()
	defer p.recalledMu.Unlock()

	markedAt, ok := p.recalledMsgIDs[messageID]
	if !ok {
		return false
	}
	if now.Sub(markedAt) > recalledMessageTTL {
		delete(p.recalledMsgIDs, messageID)
		return false
	}
	return true
}

func isMessageWithdrawnCode(code int, msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	if code == 230011 {
		return true
	}
	for _, needle := range []string{"withdrawn", "recalled", "recall", "deleted", "not found", "not exist", "撤回"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (p *Platform) IsMessageRecalled(ctx context.Context, rctx any) (bool, error) {
	rc, ok := rctx.(replyContext)
	if !ok || strings.TrimSpace(rc.messageID) == "" {
		return false, nil
	}
	messageID := strings.TrimSpace(rc.messageID)
	if p.isMessageRecalled(messageID) {
		return true, nil
	}
	if p.client == nil {
		return false, fmt.Errorf("%s: client not initialized", p.tag())
	}

	req := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		UserIdType(larkim.UserIdTypeGetMessageOpenId).
		Build()

	var resp *larkim.GetMessageResp
	if err := p.withTransientRetry(ctx, "get message", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "get message", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			var err error
			resp, err = client.Im.Message.Get(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: get message api call: %w", p.tag(), err)
			}
			if !resp.Success() {
				return fmt.Errorf("%s: get message failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
			}
			return nil
		})
	}); err != nil {
		if resp != nil && isMessageWithdrawnCode(resp.Code, resp.Msg) {
			p.markMessageRecalled(messageID)
			return true, nil
		}
		if isMessageWithdrawnError(err) {
			p.markMessageRecalled(messageID)
			return true, nil
		}
		return false, err
	}

	if resp == nil || resp.Data == nil || len(resp.Data.Items) == 0 {
		p.markMessageRecalled(messageID)
		return true, nil
	}
	for _, item := range resp.Data.Items {
		if item != nil && item.Deleted != nil && *item.Deleted {
			p.markMessageRecalled(messageID)
			return true, nil
		}
	}
	return false, nil
}

func isMessageWithdrawnError(err error) bool {
	if err == nil {
		return false
	}
	return isMessageWithdrawnCode(0, err.Error())
}

func (p *Platform) dispatchCoreMessage(msg *core.Message) {
	h := p.getHandler()
	if msg == nil || h == nil {
		return
	}
	if p.isMessageRecalled(msg.MessageID) {
		slog.Debug(p.tag()+": recalled message dispatch dropped", "message_id", msg.MessageID)
		return
	}
	h(p.dispatchPlatform(), msg)
}

// bufferImage adds a freshly-downloaded image to the per-session batch buffer.
// Consecutive image-only messages from the same session (same chatID + userID
// + parentID) coalesce into a single multi-image dispatch after imageBatchWindow
// of quiet time. A context mismatch (different parentID or userID) flushes the
// existing batch immediately before the new one starts.
//
// The timer callback captures the entry pointer; flushImageBatchByRef verifies
// the entry hasn't been superseded before dispatching, so a stale timer firing
// after a replace or Stop will be a safe no-op.
func (p *Platform) bufferImage(sessionKey string, entry *imageBatchEntry) {
	p.imageBatchMu.Lock()
	if p.imageBatch == nil {
		// Lazy-init so tests that construct &Platform{} directly can still
		// exercise the image path without remembering to allocate the map.
		p.imageBatch = make(map[string]*imageBatchEntry)
	}

	// If a batch for this session exists with different context (parentID or
	// userID), flush it first so we never mix unrelated batches.
	var toFlush *imageBatchEntry
	if existing, ok := p.imageBatch[sessionKey]; ok {
		if existing.parentID != entry.parentID || existing.userID != entry.userID {
			if existing.timer != nil {
				existing.timer.Stop()
			}
			delete(p.imageBatch, sessionKey)
			toFlush = existing
		}
	}

	if existing, ok := p.imageBatch[sessionKey]; ok {
		// Append into the existing batch and reset its timer.
		if existing.timer != nil {
			existing.timer.Stop()
		}
		existing.images = append(existing.images, entry.images...)
		existing.messageIDs = append(existing.messageIDs, entry.messageIDs...)
		if entry.createTimeMs > existing.createTimeMs {
			existing.createTimeMs = entry.createTimeMs
		}
		ref := existing
		existing.timer = time.AfterFunc(imageBatchWindow, func() {
			p.flushImageBatchByRef(sessionKey, ref)
		})
	} else {
		// Start a fresh batch with its own timer.
		ref := entry
		entry.timer = time.AfterFunc(imageBatchWindow, func() {
			p.flushImageBatchByRef(sessionKey, ref)
		})
		p.imageBatch[sessionKey] = entry
	}

	p.imageBatchMu.Unlock()

	if toFlush != nil {
		p.dispatchImageBatchEntry(toFlush)
	}
}

// flushImageBatchByRef dispatches the batch iff the map still points at the
// same entry pointer (i.e. the timer wasn't superseded). Called by the
// AfterFunc timer callback.
func (p *Platform) flushImageBatchByRef(sessionKey string, ref *imageBatchEntry) {
	p.imageBatchMu.Lock()
	current, ok := p.imageBatch[sessionKey]
	if !ok || current != ref {
		p.imageBatchMu.Unlock()
		return
	}
	if current.timer != nil {
		current.timer.Stop()
	}
	delete(p.imageBatch, sessionKey)
	p.imageBatchMu.Unlock()

	p.dispatchImageBatchEntry(current)
}

// flushImageBatches synchronously dispatches any pending image batches.
// Intended to be called from Stop() so buffered images aren't lost when
// cc-connect shuts down.
func (p *Platform) flushImageBatches() {
	p.imageBatchMu.Lock()
	pending := p.imageBatch
	p.imageBatch = make(map[string]*imageBatchEntry)
	p.imageBatchMu.Unlock()

	for _, entry := range pending {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		p.dispatchImageBatchEntry(entry)
	}
}

// dispatchImageBatchEntry emits a single core.Message carrying all images
// that were buffered into this batch entry. The newest create_time is used
// as UserMessageTimeMs so the merged message preserves the user's intended
// ordering, and the newest message_id is used as the canonical id.
func (p *Platform) dispatchImageBatchEntry(entry *imageBatchEntry) {
	if len(entry.images) == 0 {
		return
	}
	lastIdx := len(entry.messageIDs) - 1
	canonicalID := entry.messageIDs[lastIdx]
	for _, mid := range entry.messageIDs {
		if p.isMessageRecalled(mid) {
			slog.Debug(p.tag()+": recalled image batch member dropped",
				"message_id", mid, "batch_size", len(entry.images))
		}
	}
	slog.Info(p.tag()+": dispatched image batch",
		"session_key", entry.sessionKey,
		"image_count", len(entry.images),
		"message_ids", entry.messageIDs,
	)
	p.dispatchCoreMessage(&core.Message{
		SessionKey: entry.sessionKey, Platform: p.platformName,
		MessageID: canonicalID,
		UserID:    entry.userID, UserName: entry.userName, ChatName: entry.chatName,
		Content:           "",
		ExtraContent:      entry.quoted.text,
		Images:            append(entry.quoted.images, entry.images...),
		ReplyCtx:          entry.rctx,
		UserMessageTimeMs: entry.createTimeMs,
	})
}

func (p *Platform) onMessageRecalled(_ context.Context, event *larkim.P2MessageRecalledV1) error {
	if event == nil || event.Event == nil {
		return nil
	}

	messageID := stringValue(event.Event.MessageId)
	chatID := stringValue(event.Event.ChatId)
	if messageID == "" {
		slog.Debug(p.tag()+": recall event without message id", "chat_id", chatID)
		return nil
	}
	if chatID != "" && !core.AllowList(p.allowChat, chatID) {
		slog.Debug(p.tag()+": recall event from unauthorized chat", "chat_id", chatID, "message_id", messageID)
		return nil
	}

	p.markMessageRecalled(messageID)
	slog.Info(p.tag()+": message recalled",
		"message_id", messageID,
		"chat_id", chatID,
		"recall_type", stringValue(event.Event.RecallType),
	)

	h := p.getHandler()
	if h == nil {
		return nil
	}
	h(p.dispatchPlatform(), &core.Message{
		Platform:  p.platformName,
		MessageID: messageID,
		Recalled:  true,
		ReplyCtx:  replyContext{messageID: messageID, chatID: chatID},
	})
	return nil
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
	userID := userIDFromEvent(sender.SenderId)
	// userName and chatName are resolved in dispatchMessage to avoid blocking
	// the SDK dispatcher goroutine with synchronous HTTP calls.

	messageID := ""
	if msg.MessageId != nil {
		messageID = *msg.MessageId
	}

	if p.isMessageRecalled(messageID) {
		slog.Debug(p.tag()+": recalled message ignored before dispatch", "message_id", messageID)
		return nil
	}

	if p.dedup.IsDuplicate(messageID) {
		slog.Debug(p.tag()+": duplicate message ignored", "message_id", messageID)
		return nil
	}

	var createTimeMs int64
	if msg.CreateTime != nil {
		if ms, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			msgTime := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if core.IsOldMessage(msgTime) {
				slog.Debug(p.tag()+": ignoring old message after restart", "create_time", *msg.CreateTime)
				return nil
			}
			createTimeMs = ms
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

	// Pre-compute sessionKey so the @bot filter below can consult the active
	// thread set; sessionKey is also used downstream for dispatch.
	sessionKey := p.makeSessionKey(msg, chatID, userID)

	if chatType == "group" && !p.groupReplyAll && p.getBotOpenID() != "" {
		if !isBotMentioned(msg.Mentions, p.getBotOpenID()) {
			switch {
			// Feishu @all sends {"text":"@_all"} with 0 mentions.
			case p.respondToAtEveryoneAndHere && msg.Content != nil && strings.Contains(*msg.Content, "@_all"):
				slog.Debug(p.tag()+": responding to @all message", "chat_id", chatID)
			// Once a thread has been engaged via @bot, allow follow-up
			// attachment-only messages (image/file/audio) in the same thread
			// through without re-mentioning the bot. Plain text and rich-text
			// posts still require an explicit @bot to avoid pulling in
			// unrelated chatter.
			case p.threadIsolation && isAttachmentMsgType(msgType) && p.isActiveThreadSession(sessionKey):
				slog.Debug(p.tag()+": passing attachment through active thread without mention",
					"chat_id", chatID, "session_key", sessionKey, "msg_type", msgType, "message_id", messageID)
			default:
				slog.Debug(p.tag()+": ignoring group message without bot mention", "chat_id", chatID)
				return nil
			}
		}
	}

	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug(p.tag()+": message from unauthorized user", "user", userID)
		p.replyUnauthorizedAccess(ctx, replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey})
		return nil
	}

	if chatType == "group" && !core.AllowList(p.allowChat, chatID) {
		slog.Debug(p.tag()+": message from unauthorized chat", "chat_id", chatID)
		return nil
	}
	if chatType != "group" && p.groupOnly {
		slog.Debug(p.tag()+": p2p message skipped (group_only=true)", "chat_type", chatType)
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

	rctx := replyContext{messageID: messageID, chatID: chatID, sessionKey: sessionKey}
	slog.Debug(p.tag()+": routed inbound message",
		"message_id", messageID,
		"session_key", sessionKey,
		"reply_in_thread", p.shouldReplyInThread(rctx),
	)

	// Mark this thread as bot-engaged so subsequent attachment-only messages
	// in the same thread can pass through without re-mentioning the bot.
	p.markThreadSessionActive(sessionKey)

	// Dispatch message handling asynchronously so the SDK event loop is not
	// blocked by IO-heavy operations (image/audio download, handler HTTP calls).
	// The dedup and old-message checks above remain synchronous to guarantee
	// correctness before spawning the goroutine.
	go p.dispatchMessage(ctx, msgType, content, mentions, messageID, sessionKey, userID, chatID, rctx, parentID, createTimeMs)

	return nil
}

func (p *Platform) replyUnauthorizedAccess(ctx context.Context, rctx replyContext) {
	if rctx.messageID == "" && rctx.chatID == "" {
		return
	}
	if err := p.Reply(ctx, rctx, core.UnauthorizedAccessMessage); err != nil {
		slog.Warn(p.tag()+": unauthorized reply failed", "error", err)
	}
}

// dispatchMessage handles the message content parsing, media download, and
// handler invocation. It runs in its own goroutine so that onMessage returns
// quickly and does not block the SDK event loop.
func (p *Platform) dispatchMessage(ctx context.Context, msgType, content string, mentions []*larkim.MentionEvent, messageID, sessionKey, userID, chatID string, rctx replyContext, parentID string, createTimeMs int64) {
	if p.isMessageRecalled(messageID) {
		slog.Debug(p.tag()+": recalled message ignored in async dispatch", "message_id", messageID)
		return
	}

	// Resolve user and chat names asynchronously so SDK dispatcher is not blocked.
	userName := ""
	if userID != "" {
		userName = p.resolveUserName(userID)
	}
	chatName := p.resolveChatName(chatID)

	// If this message is a reply to another message, fetch the quoted content
	// and prepend it so the agent has full context.
	// Skip quote injection when thread_isolation is enabled and the message is
	// inside a thread — the thread already provides conversational context, and
	// long quoted prefixes can drown out the user's actual text (issue #764).
	var quoted quotedMessage
	if parentID != "" && !(p.threadIsolation && isThreadSessionKey(sessionKey)) {
		quoted = p.fetchQuotedMessage(ctx, parentID)
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
		text := stripMentions(textBody.Text, mentions, p.getBotOpenID())
		if text == "" && quoted.text == "" && len(quoted.images) == 0 {
			slog.Debug(p.tag()+": dropping empty text after mention stripping",
				"message_id", messageID,
				"raw_text_len", len(textBody.Text),
				"mentions", len(mentions),
			)
			return
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content: text, ExtraContent: quoted.text, Images: quoted.images, ReplyCtx: rctx,
			UserMessageTimeMs: createTimeMs,
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
			if sendErr := p.Send(ctx, rctx, "⚠️ Image download failed (network error). Please resend."); sendErr != nil {
				slog.Error(p.tag()+": failed to notify user about image download failure", "error", sendErr)
			}
			return
		}
		// Batch consecutive image-only messages from the same session into a
		// single multi-image dispatch. Feishu mobile sends N batch-selected
		// images as N separate events with very close create_time values;
		// dispatching each immediately causes core/engine's create_time
		// watermark (PR #1168) to drop the oldest image (issue #1395).
		// We only coalesce plain image messages (no quoted context) because
		// quoted images are usually a single image replying to a prior text.
		if parentID == "" {
			p.bufferImage(sessionKey, &imageBatchEntry{
				sessionKey:   sessionKey,
				userID:       userID,
				userName:     userName,
				chatName:     chatName,
				rctx:         rctx,
				images:       []core.ImageAttachment{{MimeType: mimeType, Data: imgData}},
				messageIDs:   []string{messageID},
				createTimeMs: createTimeMs,
				parentID:     parentID,
			})
			return
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content:           "",
			ExtraContent:      quoted.text,
			Images:            append(quoted.images, core.ImageAttachment{MimeType: mimeType, Data: imgData}),
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
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
			if sendErr := p.Send(ctx, rctx, "⚠️ Voice message download failed (network error). Please resend."); sendErr != nil {
				slog.Error(p.tag()+": failed to notify user about audio download failure", "error", sendErr)
			}
			return
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Audio: &core.AudioAttachment{
				MimeType: "audio/opus",
				Data:     audioData,
				Format:   "ogg",
				Duration: audioBody.Duration / 1000,
			},
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
		})

	case "post":
		textParts, images := p.parsePostContent(messageID, content)
		text := stripMentions(strings.Join(textParts, "\n"), mentions, p.getBotOpenID())
		if text == "" && len(images) == 0 && quoted.text == "" && len(quoted.images) == 0 {
			return
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content: text, ExtraContent: quoted.text, Images: append(quoted.images, images...),
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
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
			if sendErr := p.Send(ctx, rctx, "⚠️ File download failed (network error). Please resend."); sendErr != nil {
				slog.Error(p.tag()+": failed to notify user about file download failure", "error", sendErr)
			}
			return
		}
		slog.Debug(p.tag()+": file downloaded", "file_name", fileBody.FileName, "size", len(fileData))
		mimeType := detectMimeType(fileData)
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Files: []core.FileAttachment{{
				MimeType: mimeType,
				Data:     fileData,
				FileName: fileBody.FileName,
			}},
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
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
			Content:           text,
			Images:            images,
			Files:             files,
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
		}
		p.dispatchCoreMessage(coreMsg)

	case "sticker":
		var stickerBody struct {
			FileKey string `json:"file_key"`
		}
		if err := json.Unmarshal([]byte(content), &stickerBody); err != nil {
			slog.Error(p.tag()+": failed to parse sticker content", "error", err)
			return
		}
		slog.Info(p.tag()+": sticker received", "user", userID, "file_key", stickerBody.FileKey)
		imgData, mimeType, err := p.downloadImage(messageID, stickerBody.FileKey)
		if err != nil {
			slog.Warn(p.tag()+": download sticker failed, falling back to placeholder", "error", err)
			p.dispatchCoreMessage(&core.Message{
				SessionKey: sessionKey, Platform: p.platformName,
				MessageID: messageID,
				UserID:    userID, UserName: userName, ChatName: chatName,
				Content: "[sticker]", ExtraContent: quoted.text, ReplyCtx: rctx,
				UserMessageTimeMs: createTimeMs,
			})
			return
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Images:            []core.ImageAttachment{{MimeType: mimeType, Data: imgData}},
			ReplyCtx:          rctx,
			UserMessageTimeMs: createTimeMs,
		})

	case "media":
		var mediaBody struct {
			FileKey  string `json:"file_key"`
			ImageKey string `json:"image_key"`
			FileName string `json:"file_name"`
			Duration int    `json:"duration"`
		}
		if err := json.Unmarshal([]byte(content), &mediaBody); err != nil {
			slog.Error(p.tag()+": failed to parse media content", "error", err)
			return
		}
		slog.Info(p.tag()+": media received", "user", userID, "file_key", mediaBody.FileKey, "file_name", mediaBody.FileName)
		text := "[video"
		if mediaBody.FileName != "" {
			text += ": " + mediaBody.FileName
		}
		if mediaBody.Duration > 0 {
			text += fmt.Sprintf(", %ds", mediaBody.Duration/1000)
		}
		text += "]"
		var images []core.ImageAttachment
		if mediaBody.ImageKey != "" {
			if thumbData, thumbMime, err := p.downloadImage(messageID, mediaBody.ImageKey); err == nil {
				images = append(images, core.ImageAttachment{MimeType: thumbMime, Data: thumbData})
			} else {
				slog.Warn(p.tag()+": download media thumbnail failed", "error", err)
			}
		}
		p.dispatchCoreMessage(&core.Message{
			SessionKey: sessionKey, Platform: p.platformName,
			MessageID: messageID,
			UserID:    userID, UserName: userName, ChatName: chatName,
			Content: text, ExtraContent: quoted.text, Images: images, ReplyCtx: rctx,
			UserMessageTimeMs: createTimeMs,
		})

	default:
		slog.Debug(p.tag()+": ignoring unsupported message type", "type", msgType)
	}
}

// resolveUserName fetches a user's display name via the Contact API, with caching.
func (p *Platform) resolveUserName(openID string) string {
	if !isValidFeishuLookupID(openID) {
		return openID
	}
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

func userIDFromEvent(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	if id.OpenId != nil && *id.OpenId != "" {
		return *id.OpenId
	}
	if id.UserId != nil && *id.UserId != "" {
		return *id.UserId
	}
	if id.UnionId != nil && *id.UnionId != "" {
		return *id.UnionId
	}
	return ""
}

func isValidFeishuLookupID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
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

// chainMessage holds extracted data from one message in a reply chain.
type chainMessage struct {
	senderName string
	senderType string // "user" or "app"
	text       string
	images     []core.ImageAttachment
	parentID   string
}

type quotedMessage struct {
	text   string
	images []core.ImageAttachment
}

// maxReplyChainDepth is the maximum number of parent messages to traverse
// when building a reply chain. This limits API calls per inbound reply.
const maxReplyChainDepth = 5

// fetchQuotedMessage retrieves the content of a parent message that the user
// is replying to, and returns formatted context plus downloaded attachments.
// For multi-level reply chains, it traces parent_id links up to maxReplyChainDepth
// levels and returns the full conversation chain.
// Returns empty content on any failure (graceful degradation — the user's own
// message is still delivered without the quote).
func (p *Platform) fetchQuotedMessage(ctx context.Context, parentID string) quotedMessage {
	chain := p.fetchReplyChain(ctx, parentID, maxReplyChainDepth)
	if len(chain) == 0 {
		return quotedMessage{}
	}
	return quotedMessage{text: formatReplyChain(chain), images: collectReplyChainImages(chain)}
}

// resolveBotSenderName returns a display name for a bot sender in a quoted
// reply chain. Feishu sets sender.id to the bot's app_id (globally stable,
// not an open_id). We consult the peer_bots config to map app_id → alias;
// if the app is unknown, we surface the app_id so operators can add it to
// the config rather than seeing an ambiguous "Bot".
func (p *Platform) resolveBotSenderName(appID string) string {
	if appID == "" {
		return "Bot"
	}
	if alias := p.peerBots[appID]; alias != "" {
		return alias
	}
	return "Bot[" + appID + "]"
}

// fetchSingleMessage retrieves one message by ID from the Feishu API and
// returns its extracted content as a chainMessage. Returns nil on any failure.
func (p *Platform) fetchSingleMessage(ctx context.Context, messageID string) *chainMessage {
	apiPath := fmt.Sprintf("/open-apis/im/v1/messages/%s?card_msg_content_type=raw_card_content", messageID)
	apiResp, err := p.client.Get(ctx, apiPath, nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		slog.Debug(p.tag()+": fetch single message failed", "message_id", messageID, "error", err)
		return nil
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				MsgType  string `json:"msg_type"`
				ParentID string `json:"parent_id"`
				Sender   struct {
					ID         string `json:"id"`
					SenderType string `json:"sender_type"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
				Mentions []*larkim.Mention `json:"mentions"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil || resp.Code != 0 || len(resp.Data.Items) == 0 {
		slog.Debug(p.tag()+": fetch single message: parse failed or no data", "message_id", messageID)
		return nil
	}

	item := resp.Data.Items[0]
	content := item.Body.Content
	if content == "" {
		return nil
	}

	// Extract plain text based on message type.
	var text string
	var images []core.ImageAttachment
	switch item.MsgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &textBody); err == nil {
			text = replaceMentions(textBody.Text, item.Mentions)
		}
	case "post":
		textParts, postImages := p.parsePostContent(messageID, content)
		text = replaceMentions(strings.Join(textParts, "\n"), item.Mentions)
		images = postImages
		if text == "" && len(images) > 0 {
			text = "[image]"
		}
	case "image":
		text = "[image]"
		var imgBody struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(content), &imgBody); err == nil && imgBody.ImageKey != "" {
			imgData, mimeType, err := p.downloadImage(messageID, imgBody.ImageKey)
			if err != nil {
				slog.Error(p.tag()+": download quoted image failed", "error", err, "message_id", messageID, "key", imgBody.ImageKey)
			} else {
				images = append(images, core.ImageAttachment{MimeType: mimeType, Data: imgData})
			}
		}
	case "interactive":
		text = extractInteractiveCardText(content)
	default:
		text = fmt.Sprintf("[%s]", item.MsgType)
	}
	if text == "" {
		return nil
	}

	// Resolve sender name.
	senderName := ""
	if item.Sender.SenderType == "app" {
		senderName = p.resolveBotSenderName(item.Sender.ID)
	} else if item.Sender.ID != "" {
		resolved := p.resolveUserName(item.Sender.ID)
		if resolved != item.Sender.ID {
			senderName = resolved
		} else {
			senderName = "User"
		}
	}
	if senderName == "" {
		senderName = "unknown"
	}

	return &chainMessage{
		senderName: senderName,
		senderType: item.Sender.SenderType,
		text:       text,
		images:     images,
		parentID:   item.ParentID,
	}
}

func collectReplyChainImages(chain []chainMessage) []core.ImageAttachment {
	var images []core.ImageAttachment
	for _, msg := range chain {
		images = append(images, msg.images...)
	}
	return images
}

// fetchReplyChain iteratively traverses parent_id links to build a reply chain.
// Returns messages in chronological order (oldest first). Stops on any failure,
// circular reference, or when maxDepth is reached.
func (p *Platform) fetchReplyChain(ctx context.Context, parentID string, maxDepth int) []chainMessage {
	var chain []chainMessage
	visited := make(map[string]struct{})
	currentID := parentID

	for currentID != "" && len(chain) < maxDepth {
		if _, seen := visited[currentID]; seen {
			slog.Debug(p.tag()+": reply chain: circular reference detected", "message_id", currentID)
			break
		}
		visited[currentID] = struct{}{}

		msg := p.fetchSingleMessage(ctx, currentID)
		if msg == nil {
			break
		}
		chain = append(chain, *msg)
		currentID = msg.parentID
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// formatReplyChain formats a slice of chain messages into a readable string.
// Single-message chains use the legacy format for backward compatibility.
// Multi-message chains use a numbered format with role labels.
func formatReplyChain(chain []chainMessage) string {
	if len(chain) == 0 {
		return ""
	}

	// Single message: backward-compatible format.
	if len(chain) == 1 {
		return fmt.Sprintf("[Quoted message from %s]:\n%s\n\n", chain[0].senderName, chain[0].text)
	}

	// Multi-message: numbered chain format.
	var b strings.Builder
	fmt.Fprintf(&b, "--- Reply chain (%d messages) ---\n", len(chain))
	for i, msg := range chain {
		role := "user"
		if msg.senderType == "app" {
			role = "assistant"
		}
		fmt.Fprintf(&b, "[%d] %s (%s):\n%s\n\n", i+1, msg.senderName, role, msg.text)
	}
	b.WriteString("---\n\n")
	return b.String()
}

// extractPostPlainText extracts plain text from a Lark post (rich text) JSON content.
func extractPostPlainText(content string) string {
	var post struct {
		Content [][]struct {
			Tag      string `json:"tag"`
			Text     string `json:"text"`
			Href     string `json:"href,omitempty"`
			Language string `json:"language,omitempty"`
			UserId   string `json:"user_id,omitempty"`
			UserName string `json:"user_name,omitempty"`
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
			case "a":
				if elem.Text != "" && elem.Href != "" {
					line = append(line, fmt.Sprintf("[%s](%s)", elem.Text, elem.Href))
				} else if elem.Text != "" {
					line = append(line, elem.Text)
				}
			case "markdown":
				if elem.Text != "" {
					line = append(line, elem.Text)
				}
			case "at":
				switch {
				case elem.UserId == "all":
					line = append(line, "@all")
				case elem.UserName != "":
					line = append(line, "@"+elem.UserName)
				case elem.UserId != "":
					line = append(line, "@user")
				}
			case "img":
				line = append(line, "[image]")
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
// code_span, code_block (with tokenized contents), text_tag, hr, button (with open_url), etc.
func extractCardElements(elements []json.RawMessage, parts *[]string) {
	for _, raw := range elements {
		var elem struct {
			Tag      string `json:"tag"`
			Content  string `json:"content"`
			Property struct {
				Content   string            `json:"content"`
				Contents  json.RawMessage   `json:"contents"`
				Language  string            `json:"language"`
				Elements  []json.RawMessage `json:"elements"`
				Text      json.RawMessage   `json:"text"`
				Items     json.RawMessage   `json:"items"`
				Columns   json.RawMessage   `json:"columns"`
				Rows      json.RawMessage   `json:"rows"`
				Behaviors json.RawMessage   `json:"behaviors"`
			} `json:"property"`
		}
		if json.Unmarshal(raw, &elem) != nil {
			continue
		}
		switch elem.Tag {
		case "button":
			// Extract button label text and open_url from behaviors.
			label := elem.Property.Content
			if label == "" {
				// label may be in property.text.property.content
				var textElem struct {
					Property struct {
						Content string `json:"content"`
					} `json:"property"`
				}
				if json.Unmarshal(elem.Property.Text, &textElem) == nil {
					label = textElem.Property.Content
				}
			}
			var openURL string
			if len(elem.Property.Behaviors) > 0 {
				var behaviors []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				}
				if json.Unmarshal(elem.Property.Behaviors, &behaviors) == nil {
					for _, b := range behaviors {
						if b.Type == "open_url" && b.URL != "" {
							openURL = b.URL
							break
						}
					}
				}
			}
			if label != "" && openURL != "" {
				*parts = append(*parts, fmt.Sprintf("[%s](%s)", label, openURL))
			} else if label != "" {
				*parts = append(*parts, label)
			}
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

// SendWithStatusFooter implements core.StatusFooterSender: send a reply with
// the body content followed by a small/dim status-footer block. Always uses
// the interactive card path so the footer can render with text_size:
// "notation". Falls back to plain Send when the footer is empty.
func (p *Platform) SendWithStatusFooter(ctx context.Context, rctx any, content, footer string) error {
	if strings.TrimSpace(footer) == "" {
		return p.Send(ctx, rctx, content)
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}
	content = p.resolveMentionsInContent(ctx, rc.chatID, content)
	processedBody := sanitizeMarkdownURLs(preprocessFeishuMarkdown(content))
	processedFooter := sanitizeMarkdownURLs(preprocessFeishuMarkdown(footer))
	cardJSON := buildCardJSONWithStatusFooter(processedBody, processedFooter)
	if p.shouldUseThreadOrReplyAPI(rc) {
		return p.replyMessage(ctx, rc, larkim.MsgTypeInteractive, cardJSON)
	}
	return p.sendNewMessageToChat(ctx, rc, larkim.MsgTypeInteractive, cardJSON)
}

func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendImage: invalid reply context type %T", p.tag(), rctx)
	}

	imageKey, err := p.uploadImageKey(ctx, img.Data)
	if err != nil {
		return err
	}
	imageContent, err := (&larkim.MessageImage{ImageKey: imageKey}).String()
	if err != nil {
		return fmt.Errorf("%s: build image message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeImage, imageContent)
}

func (p *Platform) uploadImageKey(ctx context.Context, data []byte) (string, error) {
	var uploadResp *larkim.CreateImageResp
	if err := p.withTransientRetry(ctx, "upload image", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "upload image", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			req := larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType("message").
					Image(bytes.NewReader(data)).
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
		return "", err
	}
	if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
		return "", fmt.Errorf("%s: upload image: no image_key returned", p.tag())
	}

	return *uploadResp.Data.ImageKey, nil
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

	msgType := detectFeishuFileMessageType(fileType)
	fileContent, err := buildFeishuFileMessageContent(msgType, *uploadResp.Data.FileKey)
	if err != nil {
		return fmt.Errorf("%s: build file message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, msgType, fileContent)
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
	// Feishu's file API only has "mp4" as the video type. We map all common
	// video MIME types and extensions to FileTypeMp4 so the message renders
	// as a native video player bubble rather than a generic file download.
	// Actual playback compatibility (e.g. webm/mkv) depends on the Feishu
	// client platform; mp4 H.264 has the broadest support.
	case strings.HasPrefix(mimeType, "video/") ||
		strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".mov") ||
		strings.HasSuffix(name, ".avi") || strings.HasSuffix(name, ".m4v") ||
		strings.HasSuffix(name, ".mkv") || strings.HasSuffix(name, ".webm"):
		return larkim.FileTypeMp4
	case mimeType == "audio/ogg" || mimeType == "audio/opus" || mimeType == "application/ogg" || strings.HasSuffix(name, ".ogg") || strings.HasSuffix(name, ".opus"):
		return larkim.FileTypeOpus
	default:
		return larkim.FileTypeStream
	}
}

func detectFeishuFileMessageType(fileType string) string {
	switch fileType {
	case larkim.FileTypeOpus:
		return larkim.MsgTypeAudio
	case larkim.FileTypeMp4:
		return larkim.MsgTypeMedia
	default:
		return larkim.MsgTypeFile
	}
}

func buildFeishuFileMessageContent(msgType, fileKey string) (string, error) {
	switch msgType {
	case larkim.MsgTypeAudio:
		return (&larkim.MessageAudio{FileKey: fileKey}).String()
	case larkim.MsgTypeMedia:
		return (&larkim.MessageMedia{FileKey: fileKey}).String()
	default:
		return (&larkim.MessageFile{FileKey: fileKey}).String()
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
	var b strings.Builder
	last := 0
	for _, loc := range mdLinkRe.FindAllStringSubmatchIndex(md, -1) {
		if len(loc) < 6 {
			continue
		}
		start, end := loc[0], loc[1]
		// `![alt](img_xxx)` is Feishu's native card-image syntax. The link
		// regex also matches the `[alt](...)` suffix, so image references must
		// be left alone for the card markdown sanitizer to handle.
		if start > 0 && md[start-1] == '!' {
			continue
		}
		b.WriteString(md[last:start])
		text := md[loc[2]:loc[3]]
		href := md[loc[4]:loc[5]]
		match := md[start:end]
		if isValidFeishuHref(href) {
			b.WriteString(match)
		} else {
			// Convert invalid-scheme link to "text (url)" plain text.
			b.WriteString(text + " (" + href + ")")
		}
		last = end
	}
	if last == 0 {
		return md
	}
	b.WriteString(md[last:])
	return b.String()
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

// isAttachmentMsgType reports whether a Feishu message type carries only an
// attachment payload (no free-form text the user could use to address another
// human). These are the message types we are willing to admit into an
// already-engaged thread without an explicit @bot mention.
func isAttachmentMsgType(msgType string) bool {
	switch msgType {
	case "image", "file", "audio", "media":
		return true
	}
	return false
}

// markThreadSessionActive records that a thread sessionKey has been engaged
// by an @bot message, enabling attachment-only follow-ups inside the thread.
// No-op when thread isolation is disabled or sessionKey is not a thread key.
func (p *Platform) markThreadSessionActive(sessionKey string) {
	if !p.threadIsolation || !isThreadSessionKey(sessionKey) {
		return
	}
	p.activeThreadSessions.Store(sessionKey, time.Now())
}

// isActiveThreadSession reports whether the given sessionKey corresponds to a
// thread that has previously been engaged by an @bot message.
func (p *Platform) isActiveThreadSession(sessionKey string) bool {
	if !p.threadIsolation || !isThreadSessionKey(sessionKey) {
		return false
	}
	_, ok := p.activeThreadSessions.Load(sessionKey)
	return ok
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
// Card 2.0 path needs mu/status/lastContent to let SetPreviewStatus patch
// the header color without re-rendering the whole card.
//
// Card 2.0 + cardkit-v1 streaming text path additionally needs cardID and a
// monotonically increasing sequence counter. cardID is empty when the
// preview was created via the legacy inline-card-JSON path (Create Card
// Entity failed → fallback), in which case streamRichCardText must NOT be
// called and the engine falls back to full-card Patch via UpdateMessage.
type feishuPreviewHandle struct {
	mu          sync.Mutex
	messageID   string
	chatID      string
	cardID      string // cardkit-v1 entity id (empty = no streaming text path)
	sequence    int    // cardkit-v1 streaming text monotonic counter (++ before use; first call = 1)
	status      core.CardStatus
	lastContent string
}

// buildCardJSON builds a Feishu interactive card JSON string with a markdown element.
// Uses schema 2.0 which supports code blocks, tables, and inline formatting.
// Card font is inherently smaller than Post/Text — this is a Feishu platform limitation.
func buildCardJSON(content string) string {
	content = sanitizeCardMarkdownForCard(content)
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

// buildCardJSONWithStatusFooter builds an interactive card with a body
// markdown element followed by a small/dim status-footer markdown element
// (Lark `text_size: "notation"`). Empty footer falls through to buildCardJSON.
func buildCardJSONWithStatusFooter(content, footer string) string {
	if strings.TrimSpace(footer) == "" {
		return buildCardJSON(content)
	}
	segments := sanitizeCardMarkdownSegmentsForCard([]string{content, footer})
	content = segments[0]
	footer = segments[1]
	elements := []map[string]any{
		{
			"tag":     "markdown",
			"content": content,
		},
		{
			"tag": "hr",
		},
		{
			"tag":       "markdown",
			"content":   footer,
			"text_size": "notation",
		},
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"body": map[string]any{
			"elements": elements,
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

func progressToolElement(iconToken string, content string) map[string]any {
	elem := map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":     "lark_md",
			"content": content,
		},
	}
	if iconToken != "" {
		elem["icon"] = map[string]any{"tag": "standard_icon", "token": iconToken}
	}
	return elem
}

func renderProgressEntryElement(item core.ProgressCardEntry, lang string) map[string]any {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		text = " "
	}
	switch item.Kind {
	case core.ProgressEntryThinking:
		return map[string]any{
			"tag":  "div",
			"icon": map[string]any{"tag": "standard_icon", "token": reasoningToolIcon},
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
		display := buildToolDisplay(toolName, text)
		content := fmt.Sprintf("<text_tag color='blue'>%s</text_tag> **%s**", progressKindLabel(item.Kind, lang), inlineCodeText(display.Title))
		if body := formatProgressToolInput(toolName, display.Detail); body != "" {
			content += "\n" + body
		}
		return progressToolElement(display.IconToken, content)
	case core.ProgressEntryToolResult:
		toolName := strings.TrimSpace(item.Tool)
		display := buildToolDisplay(toolName, "")
		content := fmt.Sprintf("<text_tag color='turquoise'>%s</text_tag> **%s**", progressKindLabel(item.Kind, lang), inlineCodeText(display.Title))
		dot := progressResultDot(item)
		meta := dot
		if item.ExitCode != nil {
			meta += fmt.Sprintf(" exit code: `%d`", *item.ExitCode)
		}
		content += "\n" + meta
		if body := formatProgressToolResult(sanitizeToolDetail(toolSanitizerGeneric, item.Text)); body != "" {
			content += "\n" + body
		} else {
			content += "\n_" + progressNoOutputText(lang) + "_"
		}
		return progressToolElement(display.IconToken, content)
	case core.ProgressEntryError:
		content := fmt.Sprintf("<text_tag color='red'>%s</text_tag>\n%s", progressKindLabel(item.Kind, lang), sanitizeCardMarkdownForCard(text))
		return map[string]any{
			"tag":     "markdown",
			"content": content,
		}
	default:
		return map[string]any{
			"tag":     "markdown",
			"content": sanitizeCardMarkdownForCard(text),
		}
	}
}

func splitProgressItemsByLane(items []core.ProgressCardEntry) (reasoning []core.ProgressCardEntry, tools []core.ProgressCardEntry, others []core.ProgressCardEntry) {
	for _, item := range items {
		switch item.Kind {
		case core.ProgressEntryThinking:
			reasoning = append(reasoning, item)
		case core.ProgressEntryToolUse, core.ProgressEntryToolResult:
			tools = append(tools, item)
		default:
			others = append(others, item)
		}
	}
	return reasoning, tools, others
}

func progressPanelTitle(label string, count int, lang string) string {
	if isZhLikeProgressLang(lang) {
		switch label {
		case "Reasoning":
			label = "思考"
		case "Tools":
			label = "工具"
		case "Updates":
			label = "更新"
		}
	}
	if count > 0 {
		return fmt.Sprintf("%s (%d)", label, count)
	}
	return label
}

func buildProgressPanel(title string, expanded bool, elements []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         expanded,
		"background_color": "grey",
		"header": map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": title},
		},
		"border":           map[string]any{"color": "grey"},
		"vertical_spacing": "8px",
		"padding":          "4px 8px",
		"elements":         elements,
	}
}

func buildProgressPanelElements(items []core.ProgressCardEntry, lang string) []map[string]any {
	elements := make([]map[string]any, 0, len(items))
	for _, item := range items {
		elements = append(elements, renderProgressEntryElement(item, lang))
	}
	return elements
}

func appendProgressGroupedElements(elements []map[string]any, items []core.ProgressCardEntry, lang string, running bool) []map[string]any {
	reasoning, tools, others := splitProgressItemsByLane(items)
	if len(reasoning) > 0 {
		elements = append(elements, buildProgressPanel(
			progressPanelTitle("Reasoning", len(reasoning), lang),
			running,
			buildProgressPanelElements(reasoning, lang),
		))
	}
	if len(tools) > 0 {
		elements = append(elements, buildProgressPanel(
			progressPanelTitle("Tools", len(tools), lang),
			running,
			buildProgressPanelElements(tools, lang),
		))
	}
	if len(others) > 0 {
		elements = append(elements, buildProgressPanel(
			progressPanelTitle("Updates", len(others), lang),
			running,
			buildProgressPanelElements(others, lang),
		))
	}
	return elements
}

func buildProgressCardJSONFromPayload(payload *core.ProgressCardPayload) string {
	items := normalizeProgressItems(payload)
	if len(items) == 0 {
		return buildCardJSON(" ")
	}

	agent := progressAgentLabel(payload.Agent)
	title, template, footer := progressStateMeta(payload.State, payload.Lang, agent)
	running := payload.State == core.ProgressCardStateRunning

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

	elements = appendProgressGroupedElements(elements, items, payload.Lang, running)
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
//
// Card 2.0 + cardkit-v1 path (when content is a rich card JSON and we're NOT
// in thread/reply mode): runs a two-step flow that captures a card_id usable
// for streaming text updates:
//
//  1. POST /open-apis/cardkit/v1/cards with {type:"card_json", data:<cardJSON>}
//     → returns card_id (numeric string, 14-day TTL).
//  2. Im.Message.Create with content {"type":"card","data":{"card_id":"..."}}
//     → returns message_id; both ids are stored on feishuPreviewHandle.
//
// If step (1) fails OR we're in thread/reply mode (Reply API doesn't accept
// card_id reference), we fall back to the inline-card-JSON path. The handle's
// cardID stays empty in that case and the engine routes EventText through the
// full-card Patch path (= original #657 behavior, no typewriter).
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

	// Card 2.0 path: engine passes a pre-built rich card JSON; pass it through.
	var cardJSON string
	var sendContent string // what goes into the Im.Message.Create / Reply content field
	var cardID string      // cardkit-v1 entity id (empty = no streaming text path)
	if isCardJSON(content) {
		cardJSON = content
		// Try cardkit-v1 two-step flow regardless of Reply vs Create. Both
		// Im.Message.Reply and Im.Message.Create accept the {type:card,data:{card_id}}
		// content schema (verified by direct API call); skipping Reply mode would
		// disable cardkit-v1 streaming on every @-mention turn (the dominant case).
		if id, err := p.createCardEntity(ctx, cardJSON); err == nil {
			cardID = id
			sendContent = fmt.Sprintf(`{"type":"card","data":{"card_id":"%s"}}`, id)
		} else {
			slog.Info(p.tag()+": create card entity failed, falling back to inline card JSON",
				"error", err)
			sendContent = cardJSON
		}
	} else {
		cardJSON = buildPreviewCardJSON(content)
		sendContent = cardJSON
	}

	var msgID string
	if p.shouldUseThreadOrReplyAPI(rc) {
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(rc.messageID).
			Body(p.buildReplyMessageReqBody(rc, larkim.MsgTypeInteractive, sendContent)).
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
				Content(sendContent).
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

	return &feishuPreviewHandle{messageID: msgID, chatID: chatID, cardID: cardID}, nil
}

// createCardEntity calls the cardkit-v1 Create Card Entity API
// (POST /open-apis/cardkit/v1/cards) and returns the card_id.
//
// The card_id is required to drive the streaming text update path
// (PUT /open-apis/cardkit/v1/cards/{card_id}/elements/{element_id}/content).
// If this call fails the caller should fall back to inline card JSON via the
// regular Im.Message.Create path; the rich card will still render but without
// native typewriter streaming.
func (p *Platform) createCardEntity(ctx context.Context, cardJSON string) (string, error) {
	body := map[string]any{
		"type": "card_json",
		"data": cardJSON,
	}
	var apiResp *larkcore.ApiResp
	if err := p.withFreshTenantAccessTokenRetry(ctx, "create card entity", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		var err error
		apiResp, err = client.Post(ctx, "/open-apis/cardkit/v1/cards", body, larkcore.AccessTokenTypeTenant, options...)
		return err
	}); err != nil {
		return "", fmt.Errorf("%s: create card entity: %w", p.tag(), err)
	}
	if apiResp == nil || apiResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: create card entity: HTTP status %d", p.tag(), apiResp.StatusCode)
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
		return "", fmt.Errorf("%s: create card entity: parse response: %w", p.tag(), err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("%s: %w", p.tag(), classifyFeishuCardAPIError("create card entity", resp.Code, resp.Msg))
	}
	if resp.Data.CardID == "" {
		return "", fmt.Errorf("%s: create card entity: empty card_id in response", p.tag())
	}
	return resp.Data.CardID, nil
}

// StreamRichCardText implements core.RichCardTextStreamer. Pushes the latest
// fullText to the rich card's main_text element via cardkit-v1 streaming text
// update API. The Lark client renders the increment between consecutive PUTs
// with a typewriter animation (controlled by the card's streaming_config).
//
// Returns ErrNotSupported when the handle has no cardID (preview was created
// via the inline-card-JSON fallback path; engine should fall back to full-card
// Patch).
func (p *Platform) StreamRichCardText(ctx context.Context, previewHandle any, fullText string) error {
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("%s: StreamRichCardText: invalid preview handle type %T", p.tag(), previewHandle)
	}

	// Serialize all PUTs for one card so the monotonic sequence counter is
	// preserved across concurrent EventText calls; rate-limit headroom is
	// huge (Lark allows 50 QPS per element).
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cardID == "" {
		return core.ErrNotSupported
	}

	h.sequence++
	apiPath := fmt.Sprintf("/open-apis/cardkit/v1/cards/%s/elements/%s/content",
		h.cardID, richCardMainTextElementID)
	body := map[string]any{
		"content":  fullText,
		"sequence": h.sequence,
	}

	var apiResp *larkcore.ApiResp
	if err := p.withFreshTenantAccessTokenRetry(ctx, "stream rich card text", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		var err error
		apiResp, err = client.Put(ctx, apiPath, body, larkcore.AccessTokenTypeTenant, options...)
		return err
	}); err != nil {
		return fmt.Errorf("%s: stream rich card text: %w", p.tag(), err)
	}
	if apiResp == nil || apiResp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: stream rich card text: HTTP status %d", p.tag(), apiResp.StatusCode)
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
		return fmt.Errorf("%s: stream rich card text: parse response: %w", p.tag(), err)
	}
	if resp.Code != 0 {
		err := classifyFeishuCardAPIError("stream rich card text", resp.Code, resp.Msg)
		if errors.Is(err, errFeishuCardRateLimited) {
			slog.Debug(p.tag()+": stream rich card text rate limited; skipping frame", "code", resp.Code)
			return nil
		}
		return fmt.Errorf("%s: %w", p.tag(), err)
	}
	return nil
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
	if isCardJSON(content) {
		// Card 2.0: engine passes full card JSON directly, skip all processing.
		cardJSON = content
		h.mu.Lock()
		h.lastContent = content
		h.mu.Unlock()
	} else if payload, ok := core.ParseProgressCardPayload(content); ok {
		cardJSON = buildProgressCardJSONFromPayload(payload)
	} else {
		processed := content
		if containsMarkdown(content) {
			processed = preprocessFeishuMarkdown(content)
		}
		cardJSON = buildCardJSON(sanitizeMarkdownURLs(processed))
	}
	// Route card-entity-bound messages to cardkit-v1 full-card update API.
	// Im.Message.Patch on entity-referenced messages is silently no-op for the
	// card body / header — only inline card JSON messages can be patched that way.
	h.mu.Lock()
	cardID := h.cardID
	h.mu.Unlock()
	if cardID != "" {
		return p.updateCardEntity(ctx, h, cardJSON)
	}
	return p.patchCardMessage(ctx, h.messageID, cardJSON)
}

// UpdateMessageWithStatusFooter implements core.StatusFooterUpdater: edit an
// existing card to render the body markdown plus a small/dim status-footer
// block (Lark `text_size: "notation"`). Falls through to UpdateMessage when
// the footer is empty.
func (p *Platform) UpdateMessageWithStatusFooter(ctx context.Context, previewHandle any, content, footer string) error {
	if !p.useInteractiveCard {
		return core.ErrNotSupported
	}
	if strings.TrimSpace(footer) == "" {
		return p.UpdateMessage(ctx, previewHandle, content)
	}
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return fmt.Errorf("%s: invalid preview handle type %T", p.tag(), previewHandle)
	}
	// Mirror UpdateMessage's existing behavior: it does not resolve
	// @mentions on the card-edit path either. SendWithStatusFooter does
	// resolve since the matching Send path resolves on the chat-thread API.
	processedBody := sanitizeMarkdownURLs(preprocessFeishuMarkdown(content))
	processedFooter := sanitizeMarkdownURLs(preprocessFeishuMarkdown(footer))
	cardJSON := buildCardJSONWithStatusFooter(processedBody, processedFooter)
	// Same card-entity routing as UpdateMessage above.
	h.mu.Lock()
	cardID := h.cardID
	h.mu.Unlock()
	if cardID != "" {
		return p.updateCardEntity(ctx, h, cardJSON)
	}
	return p.patchCardMessage(ctx, h.messageID, cardJSON)
}

func (p *Platform) patchCardMessage(ctx context.Context, messageID, cardJSON string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
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

// updateCardEntity performs a full-card replacement on a cardkit-v1 entity via
// PUT /open-apis/cardkit/v1/cards/{card_id}. Required for messages that were
// sent as card_id references (rich-mode path) — Im.Message.Patch does not
// affect the rendered content of such messages.
//
// Reuses h.sequence as the monotonic ordering counter (shared with
// streamRichCardText so any sequence on any element/card is monotonic).
func (p *Platform) updateCardEntity(ctx context.Context, h *feishuPreviewHandle, cardJSON string) error {
	h.mu.Lock()
	if h.cardID == "" {
		h.mu.Unlock()
		return fmt.Errorf("%s: updateCardEntity: cardID not set", p.tag())
	}
	h.sequence++
	cardID := h.cardID
	seq := h.sequence
	h.mu.Unlock()

	apiPath := fmt.Sprintf("/open-apis/cardkit/v1/cards/%s", cardID)
	body := map[string]any{
		"card": map[string]any{
			"type": "card_json",
			"data": cardJSON,
		},
		"sequence": seq,
	}
	var apiResp *larkcore.ApiResp
	if err := p.withFreshTenantAccessTokenRetry(ctx, "update card entity", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		var err error
		apiResp, err = client.Put(ctx, apiPath, body, larkcore.AccessTokenTypeTenant, options...)
		return err
	}); err != nil {
		return fmt.Errorf("%s: update card entity: %w", p.tag(), err)
	}
	if apiResp == nil || apiResp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: update card entity: HTTP status %d", p.tag(), apiResp.StatusCode)
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
		return fmt.Errorf("%s: update card entity: parse response: %w", p.tag(), err)
	}
	if resp.Code != 0 {
		return fmt.Errorf("%s: %w", p.tag(), classifyFeishuCardAPIError("update card entity", resp.Code, resp.Msg))
	}
	return nil
}

func (p *Platform) Stop() error {
	if p.isWSPrimary {
		remaining := unregisterSharedWS(p)
		if remaining > 0 {
			slog.Warn(p.tag()+": primary shutting down, secondary platforms will lose event source",
				"remaining", remaining)
		}
		if cancel := p.getCancel(); cancel != nil {
			cancel()
		}
	} else {
		unregisterSharedWS(p)
	}
	// Stop webhook server if running (Lark international version)
	if svr := p.getServer(); svr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svr.Shutdown(ctx); err != nil {
			slog.Error(p.tag()+": webhook server shutdown error", "error", err)
		}
	}
	// Flush any pending image batches so buffered images aren't lost on shutdown.
	p.flushImageBatches()
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

	// Diagnostic for QA-reported intermittent "audio rendered as file" in
	// P2P reply mode (see internal task t-20260615-cqjbk1). Tracking the
	// API path lets operators correlate Feishu client renders to whether
	// we used Reply (in-thread) or Create (new message) when issues
	// recur. The Reply path has historically had narrower MsgType
	// support on some Feishu desktop client versions.
	if p.shouldUseThreadOrReplyAPI(rc) {
		slog.Debug(p.tag()+": SendAudio using Reply API",
			"file_key", fileKey, "msg_id", rc.messageID, "format", format)
	} else {
		slog.Debug(p.tag()+": SendAudio using Create API",
			"file_key", fileKey, "chat_id", rc.chatID, "format", format)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeAudio, audioContent)
}

// SendVideo uploads video bytes to Feishu and sends a native video
// (MsgTypeMedia) message. Implements core.VideoSender.
//
// Feishu's File API only recognises "mp4" as the video file_type, so
// every input is uploaded under that label. Actual playback depends on
// the Feishu client (mp4 H.264 has the broadest support); other
// containers like webm / mkv typically still upload but may render as
// a download tile on some clients. The fallback path to SendFile in
// engine.go preserves at least delivery when this happens.
func (p *Platform) SendVideo(ctx context.Context, rctx any, video []byte, format string, fileName string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: SendVideo: invalid reply context type %T", p.tag(), rctx)
	}
	if fileName == "" {
		if format != "" {
			fileName = "video." + format
		} else {
			fileName = "video.mp4"
		}
	}

	var uploadResp *larkim.CreateFileResp
	if err := p.withTransientRetry(ctx, "upload video", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "upload video", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			req := larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(larkim.FileTypeMp4).
					FileName(fileName).
					File(bytes.NewReader(video)).
					Build()).
				Build()
			var err error
			uploadResp, err = client.Im.File.Create(ctx, req, options...)
			if err != nil {
				return fmt.Errorf("%s: upload video: %w", p.tag(), err)
			}
			if !uploadResp.Success() {
				return fmt.Errorf("%s: upload video code=%d msg=%s", p.tag(), uploadResp.Code, uploadResp.Msg)
			}
			return nil
		})
	}); err != nil {
		return err
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("%s: upload video: no file_key returned", p.tag())
	}
	fileKey := *uploadResp.Data.FileKey

	slog.Debug(p.tag()+": video uploaded", "file_key", fileKey, "format", format, "size", len(video))

	mediaMsg := larkim.MessageMedia{FileKey: fileKey}
	mediaContent, err := mediaMsg.String()
	if err != nil {
		return fmt.Errorf("%s: build video message: %w", p.tag(), err)
	}

	return p.sendMediaMessage(ctx, rc, larkim.MsgTypeMedia, mediaContent)
}

type postElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	Language string `json:"language,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
	Href     string `json:"href,omitempty"`
	UserId   string `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
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
				if elem.Text != "" && elem.Href != "" {
					textParts = append(textParts, fmt.Sprintf("[%s](%s)", elem.Text, elem.Href))
				} else if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "code_block":
				if elem.Text != "" {
					lang := elem.Language
					textParts = append(textParts, "```"+lang+"\n"+elem.Text+"\n```")
				}
			case "markdown":
				if elem.Text != "" {
					textParts = append(textParts, elem.Text)
				}
			case "at":
				if p.getBotOpenID() != "" && elem.UserId == p.getBotOpenID() {
					continue
				}
				switch {
				case elem.UserId == "all":
					textParts = append(textParts, "@all")
				case elem.UserName != "":
					textParts = append(textParts, "@"+elem.UserName)
				case elem.UserId != "":
					textParts = append(textParts, "@"+p.resolveUserName(elem.UserId))
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
	if p.groupOnly {
		slog.Debug(p.tag()+": bot menu skipped (group_only=true)", "user", userID)
		return nil
	}

	slog.Info(p.tag()+": bot menu clicked", "event_key", eventKey, "user", userID)

	content := eventKey
	if !strings.HasPrefix(content, "/") {
		content = "/" + content
	}

	userName := p.resolveUserName(userID)
	sessionKey := p.platformName + ":" + userID + ":" + userID

	p.getHandler()(p.dispatchPlatform(), &core.Message{
		SessionKey: sessionKey,
		Platform:   p.platformName,
		Content:    content,
		UserID:     userID,
		UserName:   userName,
		ReplyCtx:   replyContext{chatID: userID, sessionKey: sessionKey},
	})
	return nil
}

// ═══════════════════════════════════════════════════════════════
// Card 2.0 rich card support (based on upstream PR #309 + #306,
// extended with "agent reply elapsed time" in the footer).
// ═══════════════════════════════════════════════════════════════

const (
	defaultToolIcon      = "app-default_outlined"
	reasoningToolIcon    = "mindmap_outlined"
	feishuCardTableLimit = 3
)

var (
	errFeishuCardRateLimited = errors.New("feishu card rate limited")
	errFeishuCardTableLimit  = errors.New("feishu card table limit")
)

type feishuCardAPIError struct {
	API     string
	Code    int
	Msg     string
	SubCode int
}

func (e *feishuCardAPIError) Error() string {
	if e == nil {
		return "feishu card api error"
	}
	if e.SubCode != 0 {
		return fmt.Sprintf("feishu card %s failed: code=%d sub_code=%d msg=%s", e.API, e.Code, e.SubCode, e.Msg)
	}
	return fmt.Sprintf("feishu card %s failed: code=%d msg=%s", e.API, e.Code, e.Msg)
}

func (e *feishuCardAPIError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case errFeishuCardRateLimited:
		return e.Code == 230020
	case errFeishuCardTableLimit:
		return e.Code == 230099 && e.SubCode == 11310 && strings.Contains(strings.ToLower(e.Msg), "table number over limit")
	default:
		return false
	}
}

var feishuCardSubCodePattern = regexp.MustCompile(`ErrCode:\s*(\d+)`)

func classifyFeishuCardAPIError(api string, code int, msg string) error {
	subCode := 0
	if m := feishuCardSubCodePattern.FindStringSubmatch(msg); len(m) == 2 {
		if parsed, err := strconv.Atoi(m[1]); err == nil {
			subCode = parsed
		}
	}
	return &feishuCardAPIError{API: api, Code: code, Msg: msg, SubCode: subCode}
}

// richCardMainTextElementID is the fixed element_id assigned to the markdown
// body block of every rich card. The cardkit-v1 streaming text update API
// targets card elements by this id (PUT /open-apis/cardkit/v1/cards/{card_id}/elements/{element_id}/content).
// Hardcoded because each rich card has exactly one streaming-text element.
const richCardMainTextElementID = "main_text"

type toolSanitizer string

const (
	toolSanitizerGeneric toolSanitizer = "generic"
	toolSanitizerSkill   toolSanitizer = "skill"
	toolSanitizerPath    toolSanitizer = "path"
	toolSanitizerSearch  toolSanitizer = "search"
	toolSanitizerURL     toolSanitizer = "url"
	toolSanitizerCommand toolSanitizer = "command"
)

type toolDescriptor struct {
	Aliases         []string
	IconToken       string
	Title           string
	Sanitizer       toolSanitizer
	ParamKeys       []string
	SummaryPatterns []*regexp.Regexp
}

type toolDisplay struct {
	Title     string
	IconToken string
	Detail    string
}

var toolDescriptors = []toolDescriptor{
	{
		Aliases:         []string{"skill"},
		IconToken:       "app-default_outlined",
		Title:           "Load skill",
		Sanitizer:       toolSanitizerSkill,
		ParamKeys:       []string{"skill", "name"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:load|use)\s+skill\s+(.+)$`)},
	},
	{
		Aliases:         []string{"read", "open"},
		IconToken:       "file-link-text_outlined",
		Title:           "Read",
		Sanitizer:       toolSanitizerPath,
		ParamKeys:       []string{"file_path", "path", "file"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:read|open)\s+(?:file\s+)?(.+)$`)},
	},
	{
		Aliases:         []string{"write", "edit", "patch", "apply_patch", "file_change", "filechange"},
		IconToken:       "edit_outlined",
		Title:           "Edit",
		Sanitizer:       toolSanitizerPath,
		ParamKeys:       []string{"file_path", "path", "file"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:edit|write|patch)\s+(?:file\s+)?(.+)$`)},
	},
	{
		Aliases:   []string{"tool_search", "tool_search_tool", "find_tools", "search_tools"},
		IconToken: "search_outlined",
		Title:     "Search tools",
		Sanitizer: toolSanitizerSearch,
		ParamKeys: []string{"query", "q"},
	},
	{
		Aliases:         []string{"web_search", "websearch", "web-search", "web.run", "web_run", "search"},
		IconToken:       "search_outlined",
		Title:           "Search web",
		Sanitizer:       toolSanitizerSearch,
		ParamKeys:       []string{"query", "q"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+(?:web\s+)?(?:for|about)|query)\s+(.+)$`)},
	},
	{
		Aliases:         []string{"web_fetch", "webfetch", "web-fetch", "fetch"},
		IconToken:       "language_outlined",
		Title:           "Fetch web page",
		Sanitizer:       toolSanitizerURL,
		ParamKeys:       []string{"url"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:fetch|open)\s+(?:web\s+page\s+)?(?:from\s+)?(.+)$`)},
	},
	{
		Aliases:         []string{"grep"},
		IconToken:       "doc-search_outlined",
		Title:           "Search text",
		Sanitizer:       toolSanitizerGeneric,
		ParamKeys:       []string{"pattern"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+text(?:\s+by\s+pattern)?|grep)\s+(.+)$`)},
	},
	{
		Aliases:         []string{"glob", "file_search", "filesearch"},
		IconToken:       "folder_outlined",
		Title:           "Search files",
		Sanitizer:       toolSanitizerGeneric,
		ParamKeys:       []string{"pattern", "query"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+files(?:\s+by\s+pattern)?|glob)\s+(.+)$`)},
	},
	{
		Aliases:   []string{"write_stdin", "stdin", "command_input"},
		IconToken: "keyboard_outlined",
		Title:     "Command I/O",
		Sanitizer: toolSanitizerCommand,
		ParamKeys: []string{"chars"},
	},
	{
		Aliases:         []string{"exec", "exec_command", "bash", "shell", "run_shell_command", "command", "run"},
		IconToken:       "command_outlined",
		Title:           "Run command",
		Sanitizer:       toolSanitizerCommand,
		ParamKeys:       []string{"cmd", "command", "script", "description"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:run|execute)\s+(?:command|script)?\s*(.+)$`)},
	},
	{
		Aliases:         []string{"browser", "playwright", "navigate"},
		IconToken:       "browser-mac_outlined",
		Title:           "Browser",
		Sanitizer:       toolSanitizerURL,
		ParamKeys:       []string{"url"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:open|browse|visit|navigate\s+to)\s+(.+)$`)},
	},
	{
		Aliases:         []string{"agent", "task", "spawn", "spawn_agent", "wait_agent", "close_agent", "send_input", "resume_agent"},
		IconToken:       "robot_outlined",
		Title:           "Run sub-agent",
		Sanitizer:       toolSanitizerGeneric,
		ParamKeys:       []string{"task", "description", "prompt"},
		SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:run\s+sub-?agent|spawn\s+agent)\s+(.+)$`)},
	},
	{
		Aliases:   []string{"multi_tool_use", "multi_tool_use.parallel", "parallel"},
		IconToken: "list-check_outlined",
		Title:     "Run tools",
		Sanitizer: toolSanitizerGeneric,
		ParamKeys: []string{"description", "prompt"},
	},
	{
		Aliases:   []string{"update_plan", "plan_update"},
		IconToken: "list-check_outlined",
		Title:     "Update plan",
		Sanitizer: toolSanitizerGeneric,
		ParamKeys: []string{"target", "subject", "description"},
	},
	{
		Aliases:   []string{"check", "determine", "verify", "todowrite", "todo_write"},
		IconToken: "list-check_outlined",
		Title:     "Check",
		Sanitizer: toolSanitizerGeneric,
		ParamKeys: []string{"target", "subject", "description"},
	},
	{
		Aliases:   []string{"summarize", "analyze", "prepare"},
		IconToken: "report_outlined",
		Title:     "Analyze",
		Sanitizer: toolSanitizerGeneric,
		ParamKeys: []string{"target", "subject", "description"},
	},
	{
		Aliases:   []string{"mcp", "mcp_tool", "mcptool"},
		IconToken: "code_outlined",
		Title:     "MCP tool",
		Sanitizer: toolSanitizerGeneric,
		ParamKeys: []string{"tool", "name", "server"},
	},
	{
		Aliases:   []string{"permissions", "permission"},
		IconToken: "safe-settings_outlined",
		Title:     "Permissions",
		Sanitizer: toolSanitizerGeneric,
	},
	{
		Aliases:   []string{"computer_use", "computeruse"},
		IconToken: "robot_outlined",
		Title:     "Computer use",
		Sanitizer: toolSanitizerGeneric,
	},
	{
		Aliases:   []string{"code_interpreter", "codeinterpreter"},
		IconToken: "code_outlined",
		Title:     "Code interpreter",
		Sanitizer: toolSanitizerGeneric,
	},
	{
		Aliases:   []string{"ask_user_question", "askuserquestion", "request_user_input"},
		IconToken: "robot_outlined",
		Title:     "Ask user",
		Sanitizer: toolSanitizerGeneric,
	},
	{
		Aliases:   []string{"lsp"},
		IconToken: "code_outlined",
		Title:     "LSP",
		Sanitizer: toolSanitizerGeneric,
	},
}

func normalizeToolNameForDisplay(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func compactToolNameForDisplay(name string) string {
	name = normalizeToolNameForDisplay(name)
	replacer := strings.NewReplacer("_", "", "-", "", " ", "", ".", "")
	return replacer.Replace(name)
}

func toolNameDisplayVariants(name string) []string {
	normalized := normalizeToolNameForDisplay(name)
	if normalized == "" {
		return nil
	}
	variants := []string{normalized}
	for _, sep := range []string{".", ":", "/"} {
		if idx := strings.LastIndex(normalized, sep); idx >= 0 && idx+1 < len(normalized) {
			variants = append(variants, strings.TrimSpace(normalized[idx+1:]))
		}
	}
	seen := make(map[string]struct{}, len(variants))
	out := variants[:0]
	for _, variant := range variants {
		if variant == "" {
			continue
		}
		if _, ok := seen[variant]; ok {
			continue
		}
		seen[variant] = struct{}{}
		out = append(out, variant)
	}
	return out
}

func toolDescriptorMatches(desc toolDescriptor, toolName string, allowPrefix bool) bool {
	variants := toolNameDisplayVariants(toolName)
	for _, alias := range desc.Aliases {
		alias = normalizeToolNameForDisplay(alias)
		aliasCompact := compactToolNameForDisplay(alias)
		for _, variant := range variants {
			if variant == alias {
				return true
			}
			compact := compactToolNameForDisplay(variant)
			if compact == aliasCompact {
				return true
			}
			if allowPrefix && (strings.HasPrefix(variant, alias+"_") || strings.HasPrefix(variant, alias+"-") || strings.HasPrefix(compact, aliasCompact)) {
				return true
			}
		}
	}
	return false
}

func resolveToolDescriptor(toolName string) *toolDescriptor {
	for i := range toolDescriptors {
		if toolDescriptorMatches(toolDescriptors[i], toolName, false) {
			return &toolDescriptors[i]
		}
	}
	for i := range toolDescriptors {
		if toolDescriptorMatches(toolDescriptors[i], toolName, true) {
			return &toolDescriptors[i]
		}
	}
	return nil
}

func humanizeToolName(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return "Tool"
	}
	name = strings.NewReplacer("_", " ", "-", " ").Replace(name)
	words := strings.Fields(name)
	if len(words) == 0 {
		return "Tool"
	}
	for i, word := range words {
		if word == strings.ToUpper(word) {
			continue
		}
		lower := strings.ToLower(word)
		words[i] = strings.ToUpper(lower[:1]) + lower[1:]
	}
	return strings.Join(words, " ")
}

func buildToolDisplay(toolName, detail string) toolDisplay {
	desc := resolveToolDescriptor(toolName)
	title := humanizeToolName(toolName)
	icon := defaultToolIcon
	sanitizer := toolSanitizerGeneric
	if desc != nil {
		title = desc.Title
		icon = desc.IconToken
		sanitizer = desc.Sanitizer
	}

	rawDetail := strings.TrimSpace(detail)
	if desc != nil {
		if extracted := extractToolDetailFromJSON(rawDetail, *desc); extracted != "" {
			rawDetail = extracted
		} else if extracted := extractToolDetailFromSummary(rawDetail, *desc); extracted != "" {
			rawDetail = extracted
		}
	}
	cleanDetail := sanitizeToolDetail(sanitizer, rawDetail)
	if desc != nil && desc.Title == "Read" && isSkillPathValue(cleanDetail) {
		title = "Skill Read"
	}
	if desc != nil && desc.Title == "Run command" {
		if commandTitle, commandIcon, ok := classifyCommandToolDetail(cleanDetail); ok {
			title = commandTitle
			icon = commandIcon
		}
	}
	return toolDisplay{Title: title, IconToken: icon, Detail: cleanDetail}
}

func classifyCommandToolDetail(detail string) (title, icon string, ok bool) {
	command := strings.ToLower(strings.TrimSpace(stripToolDisplayQuotes(detail)))
	if command == "" {
		return "", "", false
	}
	if strings.HasPrefix(command, "git ") || command == "git" {
		return "Git", "code_outlined", true
	}
	if strings.HasPrefix(command, "gh ") || command == "gh" {
		return "GitHub", "cloud_outlined", true
	}
	if strings.HasPrefix(command, "cc-connect ") || command == "cc-connect" {
		return "cc-connect", "robot_outlined", true
	}
	if commandHasAnyPrefix(command, "go test", "npm test", "npm run test", "pnpm test", "yarn test", "pytest", "cargo test", "swift test", "xcodebuild test") {
		return "Run tests", "list-check_outlined", true
	}
	if commandHasAnyPrefix(command, "make test", "go build", "make build", "npm run build", "pnpm build", "yarn build", "cargo build", "swift build", "xcodebuild build") {
		return "Build", "codeblock_outlined", true
	}
	if commandHasAnyPrefix(command, "ps ", "kill ", "pkill ", "launchctl ", "lsof ") {
		return "Inspect process", "computer_outlined", true
	}
	if commandHasAnyPrefix(command, "rg ", "grep ", "ag ", "ack ") || strings.Contains(command, " | grep ") || strings.Contains(command, " | rg ") {
		return "Search text", "doc-search_outlined", true
	}
	if commandHasAnyPrefix(command, "ls ", "ls\n", "ls\t", "ls;", "ls &&", "stat ", "pwd", "cat ", "sed ", "awk ", "head ", "tail ", "wc ", "strings ", "find ") {
		return "Inspect files", "file-link-text_outlined", true
	}
	if commandHasAnyPrefix(command, "curl ", "wget ", "ssh ", "scp ", "rsync ") {
		return "Network", "cloud_outlined", true
	}
	if commandHasAnyPrefix(command, "cp ", "mv ", "rm ", "mkdir ", "rmdir ", "chmod ", "chown ", "touch ") {
		return "File operation", "folder_outlined", true
	}
	if commandHasAnyPrefix(command, "sleep ", "osascript -e 'delay ", "osascript -e \"delay ") {
		return "Wait", "alarm-clock_outlined", true
	}
	return "", "", false
}

func commandHasAnyPrefix(command string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func extractToolDetailFromJSON(text string, desc toolDescriptor) string {
	text = strings.TrimSpace(text)
	if text == "" || len(desc.ParamKeys) == 0 {
		return ""
	}
	candidates := []string{text}
	if idx := strings.Index(text, "{"); idx > 0 {
		candidates = append(candidates, text[idx:])
	}
	for _, candidate := range candidates {
		var params map[string]any
		if err := json.Unmarshal([]byte(candidate), &params); err != nil {
			continue
		}
		if desc.Title == "Search text" {
			if pattern := extractScalarText(params["pattern"]); pattern != "" {
				if target := extractScalarText(params["glob"]); target != "" {
					return pattern + " in " + target
				}
				if target := extractScalarText(params["path"]); target != "" {
					return pattern + " in " + target
				}
				if target := extractScalarText(params["file_path"]); target != "" {
					return pattern + " in " + target
				}
				return pattern
			}
		}
		for _, key := range desc.ParamKeys {
			if value := extractScalarText(params[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func extractScalarText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func extractToolDetailFromSummary(text string, desc toolDescriptor) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(stripToolDisplayMarkdown(line))
		if line == "" {
			continue
		}
		if desc.Sanitizer == toolSanitizerURL {
			if urlText := extractFirstURL(line); urlText != "" {
				return urlText
			}
		}
		for _, pattern := range desc.SummaryPatterns {
			if match := pattern.FindStringSubmatch(line); len(match) > 1 {
				return strings.TrimSpace(match[1])
			}
		}
		if code := extractFirstCodeSpan(line); code != "" {
			return code
		}
		if quoted := extractFirstQuotedText(line); quoted != "" {
			return quoted
		}
		return line
	}
	return ""
}

func stripToolDisplayMarkdown(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "- ")
	text = strings.TrimPrefix(text, "* ")
	text = strings.Trim(text, "`")
	text = strings.Trim(text, "_")
	return strings.TrimSpace(text)
}

var (
	firstURLRe        = regexp.MustCompile(`https?://[^\s'"` + "`" + `<>]+`)
	firstCodeSpanRe   = regexp.MustCompile("`([^`]+)`")
	firstQuotedTextRe = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	secretAssignRe    = regexp.MustCompile(`(?i)\b([A-Za-z_][A-Za-z0-9_]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|client[_-]?secret|access[_-]?key)[A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s"'` + "`" + `]+)`)
	authHeaderRe      = regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)([^\s'"` + "`" + `]+)`)
	sensitiveNameRe   = regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|client[_-]?secret|access[_-]?key)`)
)

func extractFirstURL(text string) string {
	return firstURLRe.FindString(text)
}

func extractFirstCodeSpan(text string) string {
	if match := firstCodeSpanRe.FindStringSubmatch(text); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func extractFirstQuotedText(text string) string {
	if match := firstQuotedTextRe.FindStringSubmatch(text); len(match) > 2 {
		if strings.TrimSpace(match[1]) != "" {
			return strings.TrimSpace(match[1])
		}
		return strings.TrimSpace(match[2])
	}
	return ""
}

func sanitizeToolDetail(kind toolSanitizer, value string) string {
	cleaned := sanitizeGenericToolText(value)
	if cleaned == "" {
		return ""
	}
	switch kind {
	case toolSanitizerSkill:
		cleaned = regexp.MustCompile(`(?i)^skill\s+`).ReplaceAllString(cleaned, "")
		cleaned = strings.NewReplacer("-", " ", "_", " ").Replace(cleaned)
		return strings.TrimSpace(cleaned)
	case toolSanitizerSearch:
		return stripToolDisplayQuotes(cleaned)
	case toolSanitizerURL:
		return sanitizeURLText(stripToolDisplayQuotes(cleaned))
	case toolSanitizerCommand:
		return sanitizeCommandLike(cleaned)
	case toolSanitizerPath:
		return redactInlineSecrets(strings.TrimSpace(cleaned))
	default:
		return redactInlineSecrets(cleaned)
	}
}

func sanitizeGenericToolText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return redactInlineSecrets(value)
}

func stripToolDisplayQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func sanitizeURLText(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "from "))
	if value == "" {
		return ""
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		q := u.Query()
		for key := range q {
			if sensitiveNameRe.MatchString(key) {
				q.Set(key, "[redacted]")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	return redactInlineSecrets(value)
}

func sanitizeCommandLike(value string) string {
	value = strings.TrimSpace(stripToolDisplayQuotes(value))
	value = regexp.MustCompile(`(?i)^(?:command|script|description)\s+`).ReplaceAllString(value, "")
	return redactInlineSecrets(value)
}

func redactInlineSecrets(value string) string {
	value = secretAssignRe.ReplaceAllString(value, "$1=[redacted]")
	value = authHeaderRe.ReplaceAllString(value, "$1[redacted]")
	return value
}

func isSkillPathValue(value string) bool {
	return strings.Contains(strings.ToLower(value), "/skills/")
}

var thinkingVerbs = []string{
	"Churning", "Clauding", "Coalescing", "Cogitating", "Computing",
	"Combobulating", "Concocting", "Conjuring", "Considering", "Contemplating",
	"Cooking", "Crafting", "Creating", "Crunching", "Deciphering",
	"Deliberating", "Divining", "Effecting", "Elucidating", "Enchanting",
	"Envisioning", "Finagling", "Forging", "Generating", "Germinating",
	"Hatching", "Ideating", "Imagining", "Incubating", "Inferring",
	"Manifesting", "Marinating", "Meandering", "Mulling", "Musing",
	"Noodling", "Percolating", "Perusing", "Pondering", "Processing",
	"Puzzling", "Reticulating", "Ruminating", "Scheming", "Simmering",
	"Spelunking", "Spinning", "Stewing", "Sussing", "Synthesizing",
	"Thinking", "Tinkering", "Transmuting", "Unfurling", "Unravelling",
	"Vibing", "Wandering", "Whirring", "Wizarding", "Working", "Wrangling",
}

func pickThinkingVerb() string {
	idx := time.Now().Unix() % int64(len(thinkingVerbs))
	return thinkingVerbs[idx] + "..."
}

var markdownTablePattern = regexp.MustCompile(`(?m)^\|.+\|\s*\n\|[\s:|-]+\|\s*\n(?:\|.+\|\s*\n?)+`)

type markdownTextMatch struct {
	start int
	end   int
	raw   string
}

type markdownLine struct {
	text       string
	start, end int
}

var feishuCardImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

const (
	richCardImageFinalWait = 4 * time.Second
	richCardImageMaxBytes  = 10 * 1024 * 1024
	richCardImageMaxCount  = 4
)

var blockedRichCardImagePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// ResolveRichCardMarkdown implements core.RichCardMarkdownResolver. It turns
// remote markdown image URLs into Feishu image keys so Card 2.0 can render them.
// Streaming frames start uploads and strip unresolved images; final frames wait
// briefly so resolved images can be embedded before the Done update.
func (p *Platform) ResolveRichCardMarkdown(ctx context.Context, markdown string, final bool) string {
	if !strings.Contains(markdown, "![") {
		return markdown
	}
	pending := map[*richCardImageUpload]struct{}{}
	resolved := p.replaceRichCardMarkdownImages(ctx, markdown, pending)
	if final && len(pending) > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, richCardImageFinalWait)
		defer cancel()
		for upload := range pending {
			select {
			case <-upload.done:
			case <-waitCtx.Done():
				return p.replaceRichCardMarkdownImages(ctx, markdown, nil)
			}
		}
		resolved = p.replaceRichCardMarkdownImages(ctx, markdown, nil)
	}
	return resolved
}

func (p *Platform) replaceRichCardMarkdownImages(ctx context.Context, markdown string, pending map[*richCardImageUpload]struct{}) string {
	seen := map[string]struct{}{}
	return feishuCardImagePattern.ReplaceAllStringFunc(markdown, func(match string) string {
		parts := feishuCardImagePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return ""
		}
		alt, value := parts[1], parts[2]
		if strings.HasPrefix(value, "img_") {
			return match
		}
		if !isRemoteRichCardImageURL(value) {
			return ""
		}
		if _, ok := seen[value]; !ok {
			if len(seen) >= richCardImageMaxCount {
				return ""
			}
			seen[value] = struct{}{}
		}
		if imageKey, ok := p.richCardImageKey(value); ok {
			return fmt.Sprintf("![%s](%s)", alt, imageKey)
		}
		if p.richCardImageFailedURL(value) {
			return ""
		}
		upload := p.ensureRichCardImageUpload(ctx, value)
		if upload != nil && pending != nil {
			pending[upload] = struct{}{}
		}
		return ""
	})
}

func (p *Platform) richCardImageKey(rawURL string) (string, bool) {
	p.richCardImageMu.Lock()
	defer p.richCardImageMu.Unlock()
	imageKey, ok := p.richCardImageResolved[rawURL]
	return imageKey, ok && imageKey != ""
}

func (p *Platform) richCardImageFailedURL(rawURL string) bool {
	p.richCardImageMu.Lock()
	defer p.richCardImageMu.Unlock()
	_, failed := p.richCardImageFailed[rawURL]
	return failed
}

func (p *Platform) ensureRichCardImageUpload(ctx context.Context, rawURL string) *richCardImageUpload {
	p.richCardImageMu.Lock()
	if p.richCardImageResolved == nil {
		p.richCardImageResolved = map[string]string{}
	}
	if p.richCardImagePending == nil {
		p.richCardImagePending = map[string]*richCardImageUpload{}
	}
	if p.richCardImageFailed == nil {
		p.richCardImageFailed = map[string]struct{}{}
	}
	if imageKey := p.richCardImageResolved[rawURL]; imageKey != "" {
		upload := &richCardImageUpload{done: make(chan struct{})}
		close(upload.done)
		p.richCardImageMu.Unlock()
		return upload
	}
	if _, failed := p.richCardImageFailed[rawURL]; failed {
		p.richCardImageMu.Unlock()
		return nil
	}
	if upload := p.richCardImagePending[rawURL]; upload != nil {
		p.richCardImageMu.Unlock()
		return upload
	}
	upload := &richCardImageUpload{done: make(chan struct{})}
	p.richCardImagePending[rawURL] = upload
	p.richCardImageMu.Unlock()

	go p.finishRichCardImageUpload(ctx, rawURL, upload)
	return upload
}

func (p *Platform) finishRichCardImageUpload(ctx context.Context, rawURL string, upload *richCardImageUpload) {
	imageKey, err := p.uploadRichCardImageURL(ctx, rawURL)

	p.richCardImageMu.Lock()
	defer p.richCardImageMu.Unlock()
	delete(p.richCardImagePending, rawURL)
	if err != nil {
		if p.richCardImageFailed == nil {
			p.richCardImageFailed = map[string]struct{}{}
		}
		p.richCardImageFailed[rawURL] = struct{}{}
		slog.Debug(p.tag()+": rich card image upload failed", "host", richCardImageURLHost(rawURL), "error", err)
	} else {
		if p.richCardImageResolved == nil {
			p.richCardImageResolved = map[string]string{}
		}
		p.richCardImageResolved[rawURL] = imageKey
		slog.Debug(p.tag()+": rich card image uploaded", "host", richCardImageURLHost(rawURL), "image_key", imageKey)
	}
	close(upload.done)
}

func (p *Platform) uploadRichCardImageURL(ctx context.Context, rawURL string) (string, error) {
	if p.richCardImageUploadFunc != nil {
		return p.richCardImageUploadFunc(ctx, rawURL)
	}
	data, mimeType, err := fetchRichCardRemoteImage(ctx, rawURL)
	if err != nil {
		return "", err
	}
	imageKey, err := p.uploadImageKey(ctx, data)
	if err != nil {
		return "", err
	}
	slog.Debug(p.tag()+": rich card image ready", "host", richCardImageURLHost(rawURL), "mime", mimeType, "bytes", len(data))
	return imageKey, nil
}

func isRemoteRichCardImageURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func richCardImageURLHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

func fetchRichCardRemoteImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", errors.New("invalid remote image URL")
	}

	client := &http.Client{
		Timeout: richCardImageFinalWait,
		Transport: &http.Transport{
			DialContext:           dialPublicRichCardImageContext,
			ResponseHeaderTimeout: richCardImageFinalWait,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if !isRemoteRichCardImageURL(req.URL.String()) {
				return errors.New("redirected to unsupported image URL")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "cc-connect-feishu-rich-card-image-resolver/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("feishu: close rich card image response body", "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("remote image HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > richCardImageMaxBytes {
		return nil, "", fmt.Errorf("remote image too large: %d bytes", resp.ContentLength)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, richCardImageMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > richCardImageMaxBytes {
		return nil, "", fmt.Errorf("remote image exceeds %d bytes", richCardImageMaxBytes)
	}
	mimeType := http.DetectContentType(data)
	if !isSupportedRichCardImageMIME(mimeType) {
		return nil, "", fmt.Errorf("unsupported remote image MIME %q", mimeType)
	}
	return data, mimeType, nil
}

func dialPublicRichCardImageContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	var ips []net.IP
	if parsed := net.ParseIP(host); parsed != nil {
		ips = []net.IP{parsed}
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, addr := range resolved {
			ips = append(ips, addr.IP)
		}
	}

	var firstBlocked net.IP
	for _, ip := range ips {
		if isBlockedRichCardImageIP(ip) {
			if firstBlocked == nil {
				firstBlocked = ip
			}
			continue
		}
		dialer := &net.Dialer{Timeout: richCardImageFinalWait}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	if firstBlocked != nil {
		return nil, fmt.Errorf("remote image host resolved to blocked IP %s", firstBlocked.String())
	}
	return nil, errors.New("remote image host resolved to no usable IPs")
}

func isBlockedRichCardImageIP(ip net.IP) bool {
	addr, err := netip.ParseAddr(ip.String())
	if err != nil {
		return true
	}
	addr = addr.Unmap()
	return !addr.IsGlobalUnicast() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		richCardImageIPInBlockedPrefix(addr)
}

func richCardImageIPInBlockedPrefix(addr netip.Addr) bool {
	for _, prefix := range blockedRichCardImagePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func isSupportedRichCardImageMIME(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/gif", "image/bmp", "image/webp":
		return true
	default:
		return false
	}
}

func markdownLinesWithOffsets(text string) []markdownLine {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	lines := make([]markdownLine, 0, len(parts))
	offset := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := offset + len(part)
		lines = append(lines, markdownLine{text: part, start: offset, end: next})
		offset = next
	}
	return lines
}

func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 2 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

func isMarkdownTableSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !isMarkdownTableRow(trimmed) {
		return false
	}
	hasDash := false
	for _, r := range trimmed {
		switch r {
		case '|', '-', ':', ' ':
			if r == '-' {
				hasDash = true
			}
		default:
			return false
		}
	}
	return hasDash
}

func findMarkdownTablesOutsideCodeBlocks(text string) []markdownTextMatch {
	lines := markdownLinesWithOffsets(text)
	var matches []markdownTextMatch
	inCodeBlock := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i].text)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock || i+1 >= len(lines) || !isMarkdownTableRow(lines[i].text) || !isMarkdownTableSeparator(lines[i+1].text) {
			continue
		}
		start := lines[i].start
		end := lines[i+1].end
		j := i + 2
		for j < len(lines) && isMarkdownTableRow(lines[j].text) {
			end = lines[j].end
			j++
		}
		matches = append(matches, markdownTextMatch{
			start: start,
			end:   end,
			raw:   strings.TrimSpace(text[start:end]),
		})
		i = j - 1
	}
	return matches
}

func wrapTablesBeyondLimit(text string, matches []markdownTextMatch, keepCount int) string {
	if len(matches) <= keepCount {
		return text
	}
	if keepCount < 0 {
		keepCount = 0
	}
	result := text
	for i := len(matches) - 1; i >= keepCount; i-- {
		match := matches[i]
		replacement := "```\n" + match.raw + "\n```"
		result = result[:match.start] + replacement + result[match.end:]
	}
	return result
}

func sanitizeCardMarkdownTables(text string, remainingBudget int) (string, int) {
	matches := findMarkdownTablesOutsideCodeBlocks(text)
	if len(matches) <= remainingBudget {
		return text, remainingBudget - len(matches)
	}
	return wrapTablesBeyondLimit(text, matches, remainingBudget), 0
}

func stripInvalidFeishuCardImages(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	return feishuCardImagePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := feishuCardImagePattern.FindStringSubmatch(match)
		if len(parts) == 3 && strings.HasPrefix(parts[2], "img_") {
			return match
		}
		return ""
	})
}

func protectFencedCodeBlocks(text string) (string, []string) {
	var blocks []string
	var b strings.Builder
	for i := 0; i < len(text); {
		start := strings.Index(text[i:], "```")
		if start < 0 {
			b.WriteString(text[i:])
			break
		}
		start += i
		end := strings.Index(text[start+3:], "```")
		if end < 0 {
			b.WriteString(text[i:])
			break
		}
		end += start + 6
		b.WriteString(text[i:start])
		placeholder := fmt.Sprintf("\x00CC_FEISHU_CODE_BLOCK_%d\x00", len(blocks))
		blocks = append(blocks, text[start:end])
		b.WriteString(placeholder)
		i = end
	}
	return b.String(), blocks
}

func restoreFencedCodeBlocks(text string, blocks []string) string {
	for i, block := range blocks {
		placeholder := fmt.Sprintf("\x00CC_FEISHU_CODE_BLOCK_%d\x00", i)
		text = strings.ReplaceAll(text, placeholder, block)
	}
	return text
}

func optimizeFeishuCardMarkdown(text string) string {
	protected, blocks := protectFencedCodeBlocks(text)
	if regexp.MustCompile(`(?m)^#{1,3} `).MatchString(protected) {
		protected = regexp.MustCompile(`(?m)^#{2,6} (.+)$`).ReplaceAllString(protected, "##### $1")
		protected = regexp.MustCompile(`(?m)^# (.+)$`).ReplaceAllString(protected, "#### $1")
	}
	protected = regexp.MustCompile(`\n{3,}`).ReplaceAllString(protected, "\n\n")
	return restoreFencedCodeBlocks(protected, blocks)
}

func sanitizeCardMarkdownSegmentsForCard(texts []string) []string {
	out := make([]string, len(texts))
	remainingBudget := feishuCardTableLimit
	for i, text := range texts {
		prepared := sanitizeMarkdownURLs(preprocessFeishuMarkdown(text))
		prepared = stripInvalidFeishuCardImages(prepared)
		prepared = optimizeFeishuCardMarkdown(prepared)
		prepared, remainingBudget = sanitizeCardMarkdownTables(prepared, remainingBudget)
		out[i] = prepared
	}
	return out
}

func sanitizeCardMarkdownForCard(text string) string {
	return sanitizeCardMarkdownSegmentsForCard([]string{text})[0]
}

func richStepDisplayName(step core.ToolStep) string {
	if step.Kind == core.ToolStepKindThinking {
		return "Thinking"
	}
	return buildToolDisplay(step.Name, step.Summary).Title
}

func richStepBody(step core.ToolStep) string {
	name := richStepDisplayName(step)
	summary := buildToolDisplay(step.Name, step.Summary).Detail
	if summary == "" {
		summary = name
	}
	if step.Kind == core.ToolStepKindThinking {
		return summary
	}

	lines := []string{summary}
	var statusParts []string
	status := strings.TrimSpace(step.Status)
	if status != "" {
		statusParts = append(statusParts, "status: "+status)
	} else if step.Success != nil {
		if *step.Success {
			statusParts = append(statusParts, "status: ok")
		} else {
			statusParts = append(statusParts, "status: failed")
		}
	}
	if step.ExitCode != nil {
		statusParts = append(statusParts, fmt.Sprintf("exit: %d", *step.ExitCode))
	}
	if len(statusParts) > 0 {
		lines = append(lines, strings.Join(statusParts, " | "))
	}
	if result := strings.TrimSpace(step.Result); result != "" {
		lines = append(lines, result)
	}
	return strings.Join(lines, "\n")
}

// isCardJSON returns true if content looks like a complete Feishu card JSON
// (has "schema" and "body"). Used to avoid double-wrapping rich card output.
func isCardJSON(content string) bool {
	if len(content) < 10 || content[0] != '{' {
		return false
	}
	return strings.Contains(content, `"schema"`) && strings.Contains(content, `"body"`)
}

// buildCardJSONWithStatus builds a Feishu card JSON with a colored header
// reflecting the given status. Used as a fallback when rich-card assembly fails.
func buildCardJSONWithStatus(content string, status core.CardStatus) string {
	content = sanitizeCardMarkdownForCard(content)
	template := "grey"
	switch status {
	case core.CardStatusWorking, core.CardStatusThinking:
		template = "blue"
	case core.CardStatusDone:
		template = "green"
	case core.CardStatusError:
		template = "red"
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"width_mode": "default", // schema 2.0 field; was wide_screen_mode (schema 1.0)
		},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": ""},
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

func splitRichStepsByLane(steps []core.ToolStep) (reasoning []core.ToolStep, tools []core.ToolStep) {
	for _, step := range steps {
		if step.Kind == core.ToolStepKindThinking {
			reasoning = append(reasoning, step)
			continue
		}
		tools = append(tools, step)
	}
	return reasoning, tools
}

func richLaneTitle(label string, count int) string {
	if count > 0 {
		return fmt.Sprintf("%s (%d)", label, count)
	}
	return label
}

func richStepRowContent(step core.ToolStep) string {
	body := richStepBody(step)
	if step.Kind == core.ToolStepKindThinking {
		return body
	}
	name := richStepDisplayName(step)
	if body == name || strings.HasPrefix(body, name+"\n") {
		return body
	}
	return name + "\n" + body
}

func richStepElement(step core.ToolStep) map[string]any {
	text := map[string]any{
		"tag":       "plain_text",
		"content":   richStepRowContent(step),
		"text_size": "notation",
	}
	elem := map[string]any{
		"tag":  "div",
		"text": text,
	}
	if step.Kind == core.ToolStepKindThinking {
		text["text_color"] = "grey"
		elem["icon"] = map[string]any{"tag": "standard_icon", "token": reasoningToolIcon}
		return elem
	}
	elem["icon"] = map[string]any{"tag": "standard_icon", "token": buildToolDisplay(step.Name, step.Summary).IconToken}
	return elem
}

func richPlaceholderElement(text string) map[string]any {
	return map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":        "plain_text",
			"content":    text,
			"text_size":  "notation",
			"text_color": "grey",
		},
	}
}

func richPanelElements(steps []core.ToolStep, emptyText string) []map[string]any {
	if len(steps) == 0 {
		return []map[string]any{richPlaceholderElement(emptyText)}
	}
	const maxPanelSteps = 10
	visible := steps
	hidden := 0
	if len(steps) > maxPanelSteps {
		hidden = len(steps) - maxPanelSteps
		visible = steps[hidden:]
	}
	elements := make([]map[string]any, 0, len(visible)+1)
	if hidden > 0 {
		elements = append(elements, richPlaceholderElement(fmt.Sprintf("... %d earlier steps hidden", hidden)))
	}
	for _, step := range visible {
		elements = append(elements, richStepElement(step))
	}
	return elements
}

func buildRichPanel(title string, expanded bool, elements []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         expanded,
		"background_color": "grey",
		"header": map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": title},
		},
		"border":           map[string]any{"color": "grey"},
		"vertical_spacing": "8px",
		"padding":          "4px 8px",
		"elements":         elements,
	}
}

const maxRichCardJSONBytes = 28000

// buildRichCard renders a Card 2.0 "single-card" turn with collapsible
// reasoning/tool panels, streaming markdown body, status-colored header, and a
// pre-composed multi-line statusFooter (engine-owned, includes elapsed).
func buildRichCard(status core.CardStatus, _ string, steps []core.ToolStep, markdown string, streaming bool, statusFooter string) string {
	b, err := buildRichCardJSONBytes(status, steps, markdown, streaming, statusFooter)
	if err != nil {
		slog.Debug("feishu: build rich card marshal failed, fallback to basic card", "error", err)
		return buildCardJSONWithStatus(markdown, status)
	}
	if len(b) <= maxRichCardJSONBytes {
		return string(b)
	}

	// Keep Card 2.0 visible when long tool/reasoning history would exceed the
	// Feishu payload limit. Dropping to a body-only fallback is unsafe because
	// Codex intermediate messages often live only in panels, leaving markdown
	// empty and producing a blank white card on update.
	for _, limit := range []struct {
		perLane int
		textLen int
	}{
		{perLane: 10, textLen: 180},
		{perLane: 6, textLen: 120},
		{perLane: 3, textLen: 80},
	} {
		compactSteps := compactRichStepsForCardSize(steps, limit.perLane, limit.textLen)
		compact, err := buildRichCardJSONBytes(status, compactSteps, markdown, streaming, statusFooter)
		if err == nil && len(compact) <= maxRichCardJSONBytes {
			slog.Debug("feishu: rich card exceeded size limit, compacted panels",
				"original_size", len(b),
				"compacted_size", len(compact),
				"steps", len(steps),
				"compacted_steps", len(compactSteps),
			)
			return string(compact)
		}
	}

	fallbackMarkdown := markdown
	if strings.TrimSpace(fallbackMarkdown) == "" {
		fallbackMarkdown = compactRichFallbackMarkdown(steps)
	}
	slog.Debug("feishu: rich card exceeds size limit, fallback to compact markdown card", "size", len(b))
	return buildCardJSONWithStatus(fallbackMarkdown, status)
}

func buildRichCardJSONBytes(status core.CardStatus, steps []core.ToolStep, markdown string, streaming bool, statusFooter string) ([]byte, error) {
	reasoningSteps, toolSteps := splitRichStepsByLane(steps)
	panelMaps := make([]map[string]any, 0, 2)
	if len(reasoningSteps) > 0 {
		panelMaps = append(panelMaps, buildRichPanel(
			richLaneTitle("Reasoning", len(reasoningSteps)),
			streaming,
			richPanelElements(reasoningSteps, "Thinking..."),
		))
	}
	if len(toolSteps) > 0 {
		panelMaps = append(panelMaps, buildRichPanel(
			richLaneTitle("Tools", len(toolSteps)),
			streaming,
			richPanelElements(toolSteps, "No tool steps"),
		))
	}
	if len(panelMaps) == 0 && streaming {
		panelMaps = append(panelMaps, buildRichPanel("Reasoning", true, richPanelElements(nil, "Thinking...")))
	}

	markdownMap := map[string]any{
		"tag":        "markdown",
		"element_id": richCardMainTextElementID, // required for cardkit-v1 streaming text update
		"content":    sanitizeCardMarkdownForCard(markdown),
	}

	// Footer: engine pre-composes a multi-line statusFooter (lines separated by \n).
	// Each line renders as its own dim "notation"-sized markdown block so they
	// visually sit below the body without being mistaken for content. Skip
	// rendering when statusFooter is empty (footer disabled / nothing to show).
	var footerElements []map[string]any
	if statusFooter != "" {
		for _, line := range strings.Split(statusFooter, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			footerElements = append(footerElements, map[string]any{
				"tag":        "markdown",
				"content":    sanitizeCardMarkdownForCard(line),
				"text_size":  "notation",

			})
		}
	}

	var elements []map[string]any
	if len(panelMaps) > 0 {
		elements = append(elements, panelMaps...)
		elements = append(elements, markdownMap)
	} else {
		elements = append(elements, markdownMap)
	}
	if len(footerElements) > 0 {
		// Insert a horizontal separator between body and footer so the boundary is clear.
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, footerElements...)
	}

	// Header template color follows status.
	headerTemplate := "blue"
	headerTitle := pickThinkingVerb()
	switch status {
	case core.CardStatusDone:
		headerTemplate = "green"
		headerTitle = "Done"
	case core.CardStatusError:
		headerTemplate = "red"
		headerTitle = "Error"
	case core.CardStatusThinking, core.CardStatusWorking:
		headerTemplate = "blue"
		headerTitle = pickThinkingVerb()
	}

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode":             streaming,
			"update_multi":               true,
			"enable_forward_interaction": true,
		},
		"header": map[string]any{
			"template": headerTemplate,
			"title":    map[string]any{"tag": "plain_text", "content": headerTitle},
		},
		"body": map[string]any{"elements": elements},
	}

	return json.Marshal(card)
}

func compactRichStepsForCardSize(steps []core.ToolStep, perLaneLimit, textLimit int) []core.ToolStep {
	if len(steps) == 0 || perLaneLimit <= 0 {
		return nil
	}
	kept := make([]core.ToolStep, 0, min(len(steps), perLaneLimit*2))
	reasoning := 0
	tools := 0
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Kind == core.ToolStepKindThinking {
			if reasoning >= perLaneLimit {
				continue
			}
			reasoning++
		} else {
			if tools >= perLaneLimit {
				continue
			}
			tools++
		}
		kept = append(kept, compactRichStepText(step, textLimit))
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}

func compactRichStepText(step core.ToolStep, textLimit int) core.ToolStep {
	step.Summary = compactRichText(step.Summary, textLimit)
	step.Result = compactRichText(step.Result, textLimit)
	return step
}

func compactRichText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "..."
}

func compactRichFallbackMarkdown(steps []core.ToolStep) string {
	compactSteps := compactRichStepsForCardSize(steps, 3, 120)
	if len(compactSteps) == 0 {
		return ""
	}
	lines := []string{"Card content is large; showing recent activity:"}
	for _, step := range compactSteps {
		line := strings.TrimSpace(richStepRowContent(step))
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "\n", " - ")
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}

func splitMarkdownByTables(md string, maxTables int) []string {
	if maxTables <= 0 {
		return []string{md}
	}
	matches := markdownTablePattern.FindAllStringIndex(md, -1)
	if len(matches) <= maxTables {
		return []string{md}
	}
	parts := make([]string, 0, len(matches)-maxTables+1)
	firstEnd := len(md)
	if len(matches) > maxTables {
		firstEnd = matches[maxTables][0]
	}
	first := strings.TrimSpace(md[:firstEnd])
	if first != "" {
		parts = append(parts, first)
	}
	for _, match := range matches[maxTables:] {
		block := strings.TrimSpace(md[match[0]:match[1]])
		if block != "" {
			parts = append(parts, block)
		}
	}
	return parts
}

// BuildRichCard implements core.RichCardSupporter. The engine pre-composes
// statusFooter (multi-line, '\n'-separated) and passes it through; the renderer
// splits it back into one dim notation block per line.
func (p *Platform) BuildRichCard(status core.CardStatus, title string, steps []core.ToolStep, markdown string, streaming bool, statusFooter string) string {
	return buildRichCard(status, title, steps, markdown, streaming, statusFooter)
}

// SplitMarkdownByTables implements core.MarkdownTableSplitter.
func (p *Platform) SplitMarkdownByTables(md string, maxTables int) []string {
	return splitMarkdownByTables(md, maxTables)
}

// SetPreviewStatus updates the card header color to reflect the agent's current state.
func (p *Platform) SetPreviewStatus(previewHandle any, status core.CardStatus) {
	h, ok := previewHandle.(*feishuPreviewHandle)
	if !ok {
		return
	}

	h.mu.Lock()
	h.status = status
	lastContent := h.lastContent
	h.mu.Unlock()

	if lastContent == "" {
		return
	}
	cardJSON := buildCardJSONWithStatus(lastContent, status)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := p.client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
		MessageId(h.messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build())
	if err != nil {
		slog.Debug("feishu: set preview status patch failed", "error", err)
		return
	}
	if !resp.Success() {
		slog.Debug("feishu: set preview status patch failed", "code", resp.Code, "msg", resp.Msg)
	}
}
