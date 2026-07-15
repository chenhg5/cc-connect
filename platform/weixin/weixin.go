package weixin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("weixin", New)
}

const (
	sessionKeyPrefix = "weixin:dm:"
	maxWeixinChunk   = 3800 // stay under typical IM limits

	// weixinSendRetryDelay is the delay between retries when sendMessage fails.
	weixinSendRetryDelay = 500 * time.Millisecond
	// weixinChunkSendDelay is the delay between sending message chunks to avoid rate limiting.
	weixinChunkSendDelay = 100 * time.Millisecond
	// pendingFlushInterval is a best-effort background retry interval for persisted replies.
	pendingFlushInterval = 15 * time.Second
	// pendingRetryCooldown avoids repeatedly trying the same expired context_token.
	pendingRetryCooldown = 30 * time.Second
	// pendingMaxAttemptsPerToken caps background attempts until a new context_token arrives.
	pendingMaxAttemptsPerToken = 3
	// typingTicketTTL is how long a cached typing ticket remains valid.
	typingTicketTTL = 10 * time.Minute
	// typingRepeatInterval is how often to resend the typing status to keep it alive.
	typingRepeatInterval = 5 * time.Second
	// contextTokenFreshTTL is a conservative real-time send window for iLink replies.
	contextTokenFreshTTL = 90 * time.Second
	maxLongPollTimeout   = 60 * time.Second
	maxPendingReplyRunes = 12000
)

type replyContext struct {
	peerUserID             string
	contextToken           string
	contextTokenCapturedAt time.Time
	proactive              bool
	deliveryUnconfirmed    bool
	messageID              string
	sessionKey             string
	userName               string
}

// Platform implements core.Platform for Weixin personal chat via the ilink bot HTTP API
// (same backend as the OpenClaw openclaw-weixin plugin: long-poll getUpdates + sendMessage).
type Platform struct {
	token        string
	baseURL      string
	cdnBaseURL   string
	allowFrom    string
	routeTag     string
	stateDir     string
	longPollMS   int
	accountLabel string

	httpClient    *http.Client
	cdnHttpClient *http.Client // 专用于 CDN 上传/下载，不走代理
	api           *apiClient

	mu       sync.RWMutex
	handler  core.MessageHandler
	cancel   context.CancelFunc
	stopping bool

	// lifecycleHandler receives readiness callbacks once the ilink long-poll
	// actually confirms a working session (first successful getUpdates).
	// This is what distinguishes ready-for-poll from a Start()-time
	// ready-for-publish signal that does not yet mean "messages can flow".
	lifecycleHandler core.PlatformLifecycleHandler

	syncBufMu   sync.Mutex
	syncBuf     string
	syncBufPath string

	dedupMu sync.Mutex
	dedup   map[string]time.Time

	pauseMu    sync.Mutex
	pauseUntil time.Time

	tokensMu   sync.RWMutex
	tokens     map[string]contextTokenEntry
	tokensPath string

	typingMu      sync.RWMutex
	typingTickets map[string]typingTicketEntry // peerUserID → cached ticket

	pendingMu   sync.Mutex
	pendingPath string
}

type typingTicketEntry struct {
	ticket    string
	fetchedAt time.Time
}

type contextTokenEntry struct {
	Token      string `json:"token"`
	AccountID  string `json:"account_id,omitempty"`
	CapturedAt string `json:"captured_at,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	SentCount  int    `json:"sent_count,omitempty"`
}

type pendingReplyEntry struct {
	Peer              string `json:"peer"`
	Content           string `json:"content"`
	Reason            string `json:"reason,omitempty"`
	CreatedAt         string `json:"created_at"`
	Attempts          int    `json:"attempts,omitempty"`
	LastAttemptAt     string `json:"last_attempt_at,omitempty"`
	LastAttemptToken  string `json:"last_attempt_token,omitempty"`
	TokenAttemptCount int    `json:"token_attempt_count,omitempty"`
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '\x00':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// New constructs a Weixin platform. Required options: token.
// Optional: base_url, cdn_base_url (default https://novac2c.cdn.weixin.qq.com/c2c), allow_from, route_tag, account_id, long_poll_timeout_ms,
// state_dir (override persistence dir), proxy, cc_data_dir + cc_project (injected by main).
func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("weixin: token is required (ilink bot Bearer token)")
	}
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("weixin", allowFrom)

	baseURL, _ := opts["base_url"].(string)
	cdnBaseURL, _ := opts["cdn_base_url"].(string)
	if strings.TrimSpace(cdnBaseURL) == "" {
		cdnBaseURL = defaultCDNBaseURL
	}
	cdnBaseURL = strings.TrimRight(strings.TrimSpace(cdnBaseURL), "/")
	routeTag, _ := opts["route_tag"].(string)
	botAgent, _ := opts["bot_agent"].(string)
	accountLabel, _ := opts["account_id"].(string)
	if accountLabel == "" {
		accountLabel = "default"
	}
	lp := sanitizeLongPollTimeoutMS(pickInt(opts["long_poll_timeout_ms"]))

	dataDir, _ := opts["cc_data_dir"].(string)
	project, _ := opts["cc_project"].(string)
	stateDir := ""
	if dataDir != "" && project != "" {
		safeProj := sanitizePathSegment(project)
		stateDir = filepath.Join(dataDir, "weixin", safeProj, sanitizePathSegment(accountLabel))
	}
	if override, _ := opts["state_dir"].(string); strings.TrimSpace(override) != "" {
		stateDir = strings.TrimSpace(override)
	}

	httpClient := &http.Client{Timeout: defaultAPITimeout}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("weixin: invalid proxy URL %q: %w", proxyURL, err)
		}
		proxyUser, _ := opts["proxy_username"].(string)
		proxyPass, _ := opts["proxy_password"].(string)
		if proxyUser != "" {
			u.User = url.UserPassword(proxyUser, proxyPass)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("weixin: using proxy", "proxy", u.Redacted())
	}

	// CDN 客户端：微信国内 CDN 必须直连，绕过环境变量中的代理（如 HTTPS_PROXY）
	cdnHttpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}

	p := &Platform{
		token:         token,
		baseURL:       baseURL,
		cdnBaseURL:    cdnBaseURL,
		allowFrom:     allowFrom,
		routeTag:      routeTag,
		stateDir:      stateDir,
		longPollMS:    lp,
		accountLabel:  accountLabel,
		httpClient:    httpClient,
		cdnHttpClient: cdnHttpClient,
		tokens:        make(map[string]contextTokenEntry),
		dedup:         make(map[string]time.Time),
		typingTickets: make(map[string]typingTicketEntry),
	}
	p.api = newAPIClient(baseURL, token, routeTag, httpClient, botAgent)

	if stateDir != "" {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return nil, fmt.Errorf("weixin: create state dir: %w", err)
		}
		p.syncBufPath = filepath.Join(stateDir, "get_updates.buf")
		p.tokensPath = filepath.Join(stateDir, "context_tokens.json")
		p.pendingPath = filepath.Join(stateDir, "pending_replies.json")
		p.loadSyncBuf()
		p.loadTokens()
	}

	return p, nil
}

func pickInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func sanitizeLongPollTimeoutMS(v int) int {
	if v <= 0 {
		return 0
	}
	maxMS := int(maxLongPollTimeout / time.Millisecond)
	if v > maxMS {
		slog.Warn("weixin: long_poll_timeout_ms too large, using default/server value", "configured_ms", v, "max_ms", maxMS)
		return 0
	}
	return v
}

func (p *Platform) Name() string { return "weixin" }

func (p *Platform) NeedsEarlyInstantReply() bool { return true }

func (p *Platform) HoldIntermediateTextUntilFinal() bool { return true }

func (p *Platform) AuditReplyMetadata(replyCtx any) core.AuditReplyMetadata {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return core.AuditReplyMetadata{}
	}
	return core.AuditReplyMetadata{
		SessionKey:       rc.sessionKey,
		UserID:           rc.peerUserID,
		UserName:         rc.userName,
		ChatName:         rc.userName,
		ChannelKey:       rc.peerUserID,
		ReplyToMessageID: rc.messageID,
		ParentMessageID:  rc.messageID,
		Extra: map[string]any{
			"peer_user_id":      rc.peerUserID,
			"has_context_token": strings.TrimSpace(rc.contextToken) != "",
			"context_token_age": p.replyContextTokenAge(rc).String(),
		},
	}
}

func (p *Platform) loadSyncBuf() {
	if p.syncBufPath == "" {
		return
	}
	b, err := os.ReadFile(p.syncBufPath)
	if err != nil {
		return
	}
	p.syncBuf = string(b)
}

// persistSyncBuf writes buf as the next get_updates cursor (caller must hold syncBufMu).
func (p *Platform) persistSyncBuf(buf string) {
	p.syncBuf = buf
	if p.syncBufPath == "" {
		return
	}
	if err := os.WriteFile(p.syncBufPath, []byte(buf), 0o600); err != nil {
		slog.Warn("weixin: save sync buf failed", "path", p.syncBufPath, "error", err)
	}
}

func (p *Platform) loadTokens() {
	if p.tokensPath == "" {
		return
	}
	b, err := os.ReadFile(p.tokensPath)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	m := make(map[string]contextTokenEntry, len(raw))
	for peer, payload := range raw {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		var legacy string
		if json.Unmarshal(payload, &legacy) == nil {
			if tok := strings.TrimSpace(legacy); tok != "" {
				m[peer] = contextTokenEntry{Token: tok, AccountID: p.accountLabel}
			}
			continue
		}
		var entry contextTokenEntry
		if json.Unmarshal(payload, &entry) != nil {
			continue
		}
		entry.Token = strings.TrimSpace(entry.Token)
		if entry.Token == "" {
			continue
		}
		if strings.TrimSpace(entry.AccountID) == "" {
			entry.AccountID = p.accountLabel
		}
		m[peer] = entry
	}
	p.tokensMu.Lock()
	p.tokens = m
	p.tokensMu.Unlock()
}

func (p *Platform) persistTokens() {
	if p.tokensPath == "" {
		return
	}
	p.tokensMu.RLock()
	out, err := json.MarshalIndent(p.tokens, "", "  ")
	p.tokensMu.RUnlock()
	if err != nil {
		return
	}
	if err := os.WriteFile(p.tokensPath, out, 0o600); err != nil {
		slog.Warn("weixin: save context tokens failed", "path", p.tokensPath, "error", err)
	}
}

func (p *Platform) setContextToken(peer, tok, messageID string, capturedAt time.Time) {
	peer = strings.TrimSpace(peer)
	tok = strings.TrimSpace(tok)
	if peer == "" || tok == "" {
		return
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	p.tokensMu.Lock()
	if p.tokens == nil {
		p.tokens = make(map[string]contextTokenEntry)
	}
	p.tokens[peer] = contextTokenEntry{
		Token:      tok,
		AccountID:  p.accountLabel,
		CapturedAt: capturedAt.Format(time.RFC3339Nano),
		MessageID:  strings.TrimSpace(messageID),
	}
	p.tokensMu.Unlock()
	p.persistTokens()
}

func (p *Platform) getContextToken(peer string) string {
	entry := p.getContextTokenEntry(peer)
	return entry.Token
}

func (p *Platform) getContextTokenEntry(peer string) contextTokenEntry {
	p.tokensMu.RLock()
	defer p.tokensMu.RUnlock()
	return p.tokens[peer]
}

func (p *Platform) markContextSendAccepted(peer, token string) int {
	peer = strings.TrimSpace(peer)
	token = strings.TrimSpace(token)
	if peer == "" || token == "" {
		return 0
	}
	p.tokensMu.Lock()
	entry := p.tokens[peer]
	if entry.Token != token {
		p.tokensMu.Unlock()
		return 0
	}
	entry.SentCount++
	p.tokens[peer] = entry
	count := entry.SentCount
	p.tokensMu.Unlock()
	p.persistTokens()
	return count
}

func (entry contextTokenEntry) capturedTime() (time.Time, bool) {
	if strings.TrimSpace(entry.CapturedAt) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, entry.CapturedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (entry contextTokenEntry) fresh(now time.Time) bool {
	if strings.TrimSpace(entry.Token) == "" {
		return false
	}
	capturedAt, ok := entry.capturedTime()
	if !ok {
		return false
	}
	return now.Sub(capturedAt) >= 0 && now.Sub(capturedAt) <= contextTokenFreshTTL
}

func (entry contextTokenEntry) age(now time.Time) time.Duration {
	capturedAt, ok := entry.capturedTime()
	if !ok {
		return 0
	}
	return now.Sub(capturedAt)
}

func (p *Platform) isPaused() bool {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()
	if p.pauseUntil.IsZero() || time.Now().After(p.pauseUntil) {
		p.pauseUntil = time.Time{}
		return false
	}
	return true
}

func (p *Platform) pauseSession(d time.Duration) {
	if d <= 0 {
		d = time.Hour
	}
	p.pauseMu.Lock()
	p.pauseUntil = time.Now().Add(d)
	p.pauseMu.Unlock()
	slog.Warn("weixin: session paused after gateway error", "duration", d, "account", p.accountLabel)
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return fmt.Errorf("weixin: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.pollLoop(ctx)
	go p.pendingFlushLoop(ctx)
	return nil
}

// SetLifecycleHandler registers a handler that will be notified once the ilink
// long-poll has confirmed a working session. Implements
// core.AsyncRecoverablePlatform so the engine waits for the actual
// ready-for-poll signal before logging "platform ready" and initialising
// platform-level capabilities.
func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.stopping = true
	p.mu.Unlock()
	return nil
}

func (p *Platform) pollLoop(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	nextTimeoutMS := p.longPollMS
	readyNotified := false
	for {
		if ctx.Err() != nil {
			return
		}
		if p.isPaused() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		p.syncBufMu.Lock()
		buf := p.syncBuf
		p.syncBufMu.Unlock()

		resp, err := p.api.getUpdates(ctx, buf, nextTimeoutMS)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("weixin: getUpdates failed", "error", err, "backoff", backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		backoff = time.Second
		if resp.LongpollingTimeoutMs > 0 {
			nextTimeoutMS = sanitizeLongPollTimeoutMS(resp.LongpollingTimeoutMs)
		}

		if resp.Errcode == sessionExpiredErrcode {
			p.pauseSession(time.Hour)
			continue
		}
		if resp.Ret != 0 && resp.Errmsg != "" {
			slog.Warn("weixin: getUpdates ret", "ret", resp.Ret, "errcode", resp.Errcode, "errmsg", resp.Errmsg)
		}

		// First successful getUpdates round-trip: ilink has accepted our token
		// and the long-poll plumbing is alive. Treat this as the authoritative
		// ready-for-poll signal; surface it to the engine so it can finish
		// initialising platform-level capabilities and stop gating the
		// "platform ready" log on a Start()-time promise.
		if !readyNotified {
			readyNotified = true
			p.mu.RLock()
			handler := p.lifecycleHandler
			p.mu.RUnlock()
			if handler != nil {
				slog.Info("weixin: ilink ready-for-poll")
				handler.OnPlatformReady(p)
			}
		}

		p.mu.RLock()
		h := p.handler
		p.mu.RUnlock()
		if h == nil {
			continue
		}
		var wg sync.WaitGroup
		for i := range resp.Msgs {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.dispatchInbound(ctx, &resp.Msgs[i], h)
			}()
		}
		wg.Wait()

		if ctx.Err() == nil && resp.GetUpdatesBuf != "" {
			p.syncBufMu.Lock()
			p.persistSyncBuf(resp.GetUpdatesBuf)
			p.syncBufMu.Unlock()
		}
	}
}

func (p *Platform) dispatchInbound(ctx context.Context, m *weixinMessage, h core.MessageHandler) {
	if m == nil {
		return
	}
	if m.MessageType == messageTypeBot {
		return
	}
	if m.MessageType != 0 && m.MessageType != messageTypeUser {
		return
	}
	from := strings.TrimSpace(m.FromUserID)
	if from == "" {
		return
	}
	if !core.AllowList(p.allowFrom, from) {
		slog.Debug("weixin: sender not in allow_from", "from", from)
		return
	}
	if m.CreateTimeMs > 0 {
		t := time.UnixMilli(m.CreateTimeMs)
		if core.IsOldMessage(t) {
			slog.Debug("weixin: skip old message", "time", t)
			return
		}
	}

	// Include create_time_ms and client_id so (seq,message_id)=(0,0) or duplicates are less likely to collide.
	dedupKey := fmt.Sprintf("%s|%d|%d|%d|%s", from, m.MessageID, m.Seq, m.CreateTimeMs, strings.TrimSpace(m.ClientID))
	p.dedupMu.Lock()
	if p.dedup == nil {
		p.dedup = make(map[string]time.Time)
	}
	now := time.Now()
	for k, ts := range p.dedup {
		if now.Sub(ts) > 5*time.Minute {
			delete(p.dedup, k)
		}
	}
	if _, ok := p.dedup[dedupKey]; ok {
		p.dedupMu.Unlock()
		return
	}
	p.dedup[dedupKey] = now
	p.dedupMu.Unlock()

	msgID := fmt.Sprintf("%d", m.MessageID)
	if m.MessageID == 0 {
		msgID = randomHex(8)
	}
	var contextTokenCapturedAt time.Time
	if tok := strings.TrimSpace(m.ContextToken); tok != "" {
		contextTokenCapturedAt = time.Now()
		p.setContextToken(from, tok, msgID, contextTokenCapturedAt)
		p.refreshTypingTicket(ctx, from, tok)
		p.flushPendingReply(context.Background(), from, tok, true)
	}

	body := bodyFromItemList(m.ItemList)
	images, files, audio := p.collectInboundMedia(ctx, m.ItemList)
	if strings.TrimSpace(body) == "" && len(images) == 0 && len(files) == 0 && audio == nil && mediaOnlyItems(m.ItemList) {
		body = "[收到媒体消息：CDN 下载或解密失败，或未配置 cdn_base_url；请改用文字说明。]"
	}
	if strings.TrimSpace(body) == "" && len(images) == 0 && len(files) == 0 && audio == nil {
		return
	}

	rc := &replyContext{
		peerUserID:             from,
		contextToken:           strings.TrimSpace(m.ContextToken),
		contextTokenCapturedAt: contextTokenCapturedAt,
		messageID:              msgID,
		sessionKey:             sessionKeyPrefix + from,
		userName:               shortWeixinUser(from),
	}

	h(p, &core.Message{
		SessionKey: sessionKeyPrefix + from,
		Platform:   p.Name(),
		MessageID:  msgID,
		UserID:     from,
		UserName:   shortWeixinUser(from),
		ChatName:   shortWeixinUser(from),
		ChannelKey: from,
		Content:    body,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ReplyCtx:   rc,
		AuditExtra: map[string]any{
			"session_id":        m.SessionID,
			"message_type":      m.MessageType,
			"message_state":     m.MessageState,
			"sequence":          m.Seq,
			"create_time_ms":    m.CreateTimeMs,
			"item_count":        len(m.ItemList),
			"has_context_token": strings.TrimSpace(m.ContextToken) != "",
		},
	})
}

func mediaOnlyItems(items []messageItem) bool {
	for _, it := range items {
		switch it.Type {
		case messageItemImage, messageItemVideo, messageItemFile:
			return true
		case messageItemVoice:
			if it.VoiceItem == nil || strings.TrimSpace(it.VoiceItem.Text) == "" {
				return true
			}
		}
	}
	return false
}

func shortWeixinUser(id string) string {
	if len(id) > 32 {
		return id[:32] + "…"
	}
	return id
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	_, err := p.ReplyWithReceipt(ctx, replyCtx, content)
	return err
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	_, err := p.SendWithReceipt(ctx, replyCtx, content)
	return err
}

func (p *Platform) ReplyWithReceipt(ctx context.Context, replyCtx any, content string) (*core.SendReceipt, error) {
	return p.sendChunksWithReceipt(ctx, replyCtx, content)
}

func (p *Platform) SendWithReceipt(ctx context.Context, replyCtx any, content string) (*core.SendReceipt, error) {
	return p.sendChunksWithReceipt(ctx, replyCtx, content)
}

// StartTyping sends a typing indicator to the peer and repeats every few seconds
// until the returned stop function is called. Implements core.TypingIndicator.
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(*replyContext)
	if !ok || rc == nil {
		return func() {}
	}
	peerID := rc.peerUserID
	contextToken := rc.contextToken
	if strings.TrimSpace(contextToken) == "" {
		contextToken = p.getContextToken(peerID)
	}

	ticket := p.getTypingTicket(ctx, peerID, contextToken)
	if ticket == "" {
		return func() {}
	}

	if err := p.api.sendTyping(ctx, peerID, ticket, typingStatusStart); err != nil {
		slog.Debug("weixin: initial typing start failed", "peer", peerID, "error", err)
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(typingRepeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				// Best-effort stop; use background context since ctx may already be cancelled.
				stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := p.api.sendTyping(stopCtx, peerID, ticket, typingStatusStop); err != nil {
					slog.Debug("weixin: typing stop failed", "peer", peerID, "error", err)
				}
				cancel()
				return
			case <-ctx.Done():
				stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := p.api.sendTyping(stopCtx, peerID, ticket, typingStatusStop); err != nil {
					slog.Debug("weixin: typing stop failed (ctx cancelled)", "peer", peerID, "error", err)
				}
				cancel()
				return
			case <-ticker.C:
				if err := p.api.sendTyping(ctx, peerID, ticket, typingStatusStart); err != nil {
					slog.Debug("weixin: typing repeat failed", "peer", peerID, "error", err)
					bestEffortCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_ = p.api.sendTyping(bestEffortCtx, peerID, ticket, typingStatusStop)
					cancel()
					return
				}
			}
		}
	}()

	return func() { close(done) }
}

// getTypingTicket returns a cached typing ticket for the peer, fetching one
// from the getconfig API if the cache is empty or expired.
func (p *Platform) getTypingTicket(ctx context.Context, peerID, contextToken string) string {
	p.typingMu.RLock()
	entry, ok := p.typingTickets[peerID]
	p.typingMu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < typingTicketTTL {
		return entry.ticket
	}

	resp, err := p.api.getConfig(ctx, peerID, contextToken)
	if err != nil {
		slog.Debug("weixin: getConfig for typing ticket failed", "peer", peerID, "error", err)
		return ""
	}
	ticket := strings.TrimSpace(resp.TypingTicket)
	if ticket == "" {
		return ""
	}

	p.typingMu.Lock()
	p.typingTickets[peerID] = typingTicketEntry{ticket: ticket, fetchedAt: time.Now()}
	p.typingMu.Unlock()
	return ticket
}

// refreshTypingTicket proactively fetches and caches a typing ticket when a
// message is received, so that StartTyping can use it without an extra round-trip.
func (p *Platform) refreshTypingTicket(ctx context.Context, peerID, contextToken string) {
	go func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p.getTypingTicket(fetchCtx, peerID, contextToken)
	}()
}

func (p *Platform) sendChunks(ctx context.Context, replyCtx any, content string) error {
	_, err := p.sendChunksWithReceipt(ctx, replyCtx, content)
	return err
}

func (p *Platform) sendChunksWithReceipt(ctx context.Context, replyCtx any, content string) (*core.SendReceipt, error) {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return nil, fmt.Errorf("weixin: invalid reply context")
	}
	if p.refreshReplyContextToken(rc) {
		slog.Debug("weixin: using latest cached context_token before send", "peer", rc.peerUserID)
	}
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		slog.Warn("weixin: context_token unavailable; attempting proactive send without context",
			"peer", rc.peerUserID,
			"proactive", rc.proactive,
			"content_preview", truncatePreview(content, 100))
	} else if !p.replyContextTokenFresh(rc) {
		slog.Warn("weixin: context_token is older than the conservative freshness window; attempting send before deferring",
			"peer", rc.peerUserID,
			"token_age", p.replyContextTokenAge(rc),
			"message_id", rc.messageID)
	}
	chunks := splitUTF8(content, maxWeixinChunk)
	clientIDs := make([]string, 0, len(chunks))
	total := len(chunks)
	for i, chunk := range chunks {
		// Add delay between chunks to avoid rate limiting (except for first chunk)
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(weixinChunkSendDelay):
			}
		}
		// Retry sendText with context_token refresh on failure
		clientID := "cc-" + randomHex(6)
		err := p.sendChunkWithRetry(ctx, rc, chunk, clientID, i+1, total)
		if err != nil {
			slog.Error("weixin: chunk send failed, message incomplete",
				"peer", rc.peerUserID,
				"failed_chunk", fmt.Sprintf("%d/%d", i+1, total),
				"error", err)
			reason := "send_failed"
			if isStaleContextTokenError(err) {
				reason = "stale_context_token"
			}
			p.enqueuePendingReply(rc.peerUserID, content, reason)
			return nil, fmt.Errorf("weixin: send chunk %d/%d: %w", i+1, total, err)
		}
		clientIDs = append(clientIDs, clientID)
	}
	deliveryConfidence := "contextual_api_accepted"
	if rc.deliveryUnconfirmed {
		deliveryConfidence = "context_free_api_accepted_unconfirmed"
		slog.Warn("weixin: message accepted without context_token; downstream delivery is unconfirmed",
			"peer", rc.peerUserID, "proactive", rc.proactive, "chunks", total)
	}
	return &core.SendReceipt{
		ParentMessageID: rc.messageID,
		Extra: map[string]any{
			"peer_user_id":        rc.peerUserID,
			"client_ids":          clientIDs,
			"delivery_confidence": deliveryConfidence,
		},
	}, nil
}

// sendChunkWithRetry sends once with the current context. A stale-token
// rejection gets one context-free fallback, matching Tencent's official
// plugin. HTTP acceptance of that fallback is not a delivery receipt.
// chunkIdx and totalChunks are 1-based indices used for logging context.
func (p *Platform) sendChunkWithRetry(ctx context.Context, rc *replyContext, chunk, clientID string, chunkIdx, totalChunks int) error {
	token := strings.TrimSpace(rc.contextToken)
	err := p.api.sendText(ctx, rc.peerUserID, chunk, token, clientID)
	if err == nil {
		if token == "" {
			rc.deliveryUnconfirmed = true
		} else if count := p.markContextSendAccepted(rc.peerUserID, token); count >= 8 {
			slog.Warn("weixin: reply allowance is near the observed per-context limit",
				"peer", rc.peerUserID, "accepted_count", count)
		}
		return nil
	}
	if !isStaleContextTokenError(err) || token == "" {
		return err
	}

	preview := []rune(chunk)
	if len(preview) > 50 {
		preview = preview[:50]
	}
	slog.Warn("weixin: sendMessage ret=-2; trying one context-free proactive fallback",
		"peer", rc.peerUserID,
		"chunk", fmt.Sprintf("%d/%d", chunkIdx, totalChunks),
		"chunk_runes", utf8.RuneCountInString(chunk),
		"token_age", p.replyContextTokenAge(rc),
		"preview", string(preview))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(weixinSendRetryDelay):
	}
	if fallbackErr := p.api.sendText(ctx, rc.peerUserID, chunk, "", clientID+"-noctx"); fallbackErr != nil {
		return fallbackErr
	}
	rc.contextToken = ""
	rc.contextTokenCapturedAt = time.Time{}
	rc.deliveryUnconfirmed = true
	return nil
}

func (p *Platform) refreshReplyContextToken(rc *replyContext) bool {
	if rc == nil || strings.TrimSpace(rc.peerUserID) == "" {
		return false
	}
	entry := p.getContextTokenEntry(rc.peerUserID)
	freshToken := strings.TrimSpace(entry.Token)
	if freshToken == "" {
		return false
	}
	if capturedAt, ok := entry.capturedTime(); ok {
		if freshToken == strings.TrimSpace(rc.contextToken) {
			if rc.contextTokenCapturedAt.IsZero() || capturedAt.After(rc.contextTokenCapturedAt) {
				rc.contextTokenCapturedAt = capturedAt
				return true
			}
			return false
		}
		rc.contextToken = freshToken
		rc.contextTokenCapturedAt = capturedAt
		return true
	}
	if freshToken == strings.TrimSpace(rc.contextToken) {
		return false
	}
	rc.contextToken = freshToken
	rc.contextTokenCapturedAt = time.Time{}
	return true
}

func (p *Platform) replyContextTokenFresh(rc *replyContext) bool {
	if rc == nil || strings.TrimSpace(rc.contextToken) == "" {
		return false
	}
	if rc.contextTokenCapturedAt.IsZero() {
		return false
	}
	age := time.Since(rc.contextTokenCapturedAt)
	return age >= 0 && age <= contextTokenFreshTTL
}

func (p *Platform) replyContextTokenAge(rc *replyContext) time.Duration {
	if rc == nil || rc.contextTokenCapturedAt.IsZero() {
		return 0
	}
	return time.Since(rc.contextTokenCapturedAt).Round(time.Millisecond)
}

func isStaleContextTokenError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ret=-2")
}

func (p *Platform) loadPendingRepliesLocked() map[string]pendingReplyEntry {
	out := make(map[string]pendingReplyEntry)
	if p.pendingPath == "" {
		return out
	}
	b, err := os.ReadFile(p.pendingPath)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

func (p *Platform) writePendingRepliesLocked(entries map[string]pendingReplyEntry) {
	if p.pendingPath == "" {
		return
	}
	if len(entries) == 0 {
		if err := os.Remove(p.pendingPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("weixin: remove pending replies failed", "path", p.pendingPath, "error", err)
		}
		return
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(p.pendingPath, out, 0o600); err != nil {
		slog.Warn("weixin: save pending replies failed", "path", p.pendingPath, "error", err)
	}
}

func (p *Platform) enqueuePendingReply(peer, content, reason string) {
	peer = strings.TrimSpace(peer)
	content = strings.TrimSpace(content)
	if peer == "" || content == "" || p.pendingPath == "" {
		return
	}
	runes := []rune(content)
	if len(runes) > maxPendingReplyRunes {
		content = string(runes[:maxPendingReplyRunes]) + "\n\n[truncated pending reply]"
	}

	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	entries := p.loadPendingRepliesLocked()
	entry := entries[peer]
	entries[peer] = pendingReplyEntry{
		Peer:              peer,
		Content:           content,
		Reason:            strings.TrimSpace(reason),
		CreatedAt:         time.Now().Format(time.RFC3339),
		Attempts:          entry.Attempts,
		LastAttemptAt:     entry.LastAttemptAt,
		LastAttemptToken:  entry.LastAttemptToken,
		TokenAttemptCount: entry.TokenAttemptCount,
	}
	p.writePendingRepliesLocked(entries)
}

func (p *Platform) pendingFlushLoop(ctx context.Context) {
	if p.pendingPath == "" {
		return
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.flushPendingReplies(ctx, false)
			timer.Reset(pendingFlushInterval)
		}
	}
}

func (p *Platform) flushPendingReplies(ctx context.Context, force bool) {
	if p.pendingPath == "" {
		return
	}
	p.pendingMu.Lock()
	entries := p.loadPendingRepliesLocked()
	peers := make([]string, 0, len(entries))
	for peer := range entries {
		peers = append(peers, peer)
	}
	p.pendingMu.Unlock()

	for _, peer := range peers {
		if ctx.Err() != nil {
			return
		}
		entry := p.getContextTokenEntry(peer)
		if !force && !entry.fresh(time.Now()) {
			continue
		}
		p.flushPendingReply(ctx, peer, entry.Token, force)
	}
}

func (p *Platform) shouldAttemptPendingFlush(entry pendingReplyEntry, contextToken string, force bool, now time.Time) bool {
	if force {
		return true
	}
	if strings.TrimSpace(contextToken) == "" {
		return false
	}
	if entry.LastAttemptToken != contextToken {
		return true
	}
	if entry.TokenAttemptCount >= pendingMaxAttemptsPerToken {
		return false
	}
	if entry.LastAttemptAt == "" {
		return true
	}
	lastAttempt, err := time.Parse(time.RFC3339, entry.LastAttemptAt)
	if err != nil {
		return true
	}
	return now.Sub(lastAttempt) >= pendingRetryCooldown
}

func (p *Platform) flushPendingReply(ctx context.Context, peer, contextToken string, force bool) {
	peer = strings.TrimSpace(peer)
	contextToken = strings.TrimSpace(contextToken)
	if peer == "" || contextToken == "" || p.pendingPath == "" {
		return
	}

	p.pendingMu.Lock()
	entries := p.loadPendingRepliesLocked()
	entry, ok := entries[peer]
	if !ok || strings.TrimSpace(entry.Content) == "" {
		p.pendingMu.Unlock()
		return
	}
	now := time.Now()
	if !p.shouldAttemptPendingFlush(entry, contextToken, force, now) {
		p.pendingMu.Unlock()
		return
	}
	entry.Attempts++
	if entry.LastAttemptToken == contextToken {
		entry.TokenAttemptCount++
	} else {
		entry.LastAttemptToken = contextToken
		entry.TokenAttemptCount = 1
	}
	entry.LastAttemptAt = now.Format(time.RFC3339)
	entries[peer] = entry
	p.writePendingRepliesLocked(entries)
	p.pendingMu.Unlock()

	content := "上次回复因微信 context_token 过期未发出，自动补发：\n\n" + entry.Content
	chunks := splitUTF8(content, maxWeixinChunk)
	clientIDPrefix := "cc-pending-" + randomHex(4)
	for i, chunk := range chunks {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(weixinChunkSendDelay):
			}
		}
		sendCtx, cancel := context.WithTimeout(ctx, defaultAPITimeout)
		err := p.api.sendText(sendCtx, peer, chunk, contextToken, fmt.Sprintf("%s-%d", clientIDPrefix, i+1))
		cancel()
		if err != nil {
			slog.Warn("weixin: pending reply flush failed", "peer", peer, "chunk", i+1, "error", err)
			return
		}
		p.markContextSendAccepted(peer, contextToken)
	}

	p.pendingMu.Lock()
	entries = p.loadPendingRepliesLocked()
	delete(entries, peer)
	p.writePendingRepliesLocked(entries)
	p.pendingMu.Unlock()
	slog.Info("weixin: pending reply flushed", "peer", peer, "chunks", len(chunks))
}

func truncatePreview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func splitUTF8(s string, maxRunes int) []string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		n := maxRunes
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// ReconstructReplyCtx implements core.ReplyContextReconstructor for cron / proactive sends.
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	if !strings.HasPrefix(sessionKey, sessionKeyPrefix) {
		return nil, fmt.Errorf("weixin: not a weixin session key")
	}
	peer := strings.TrimPrefix(sessionKey, sessionKeyPrefix)
	if strings.TrimSpace(peer) == "" {
		return nil, fmt.Errorf("weixin: empty peer in session key")
	}
	entry := p.getContextTokenEntry(peer)
	rc := &replyContext{
		peerUserID:   peer,
		contextToken: entry.Token,
		proactive:    true,
		sessionKey:   sessionKey,
		userName:     shortWeixinUser(peer),
	}
	if capturedAt, ok := entry.capturedTime(); ok {
		rc.contextTokenCapturedAt = capturedAt
	}
	return rc, nil
}

// FormattingInstructions implements core.FormattingInstructionProvider.
func (p *Platform) FormattingInstructions() string {
	return "Replies are delivered as plain text to Weixin. Avoid markdown tables; use short paragraphs."
}

var (
	_ core.Platform                      = (*Platform)(nil)
	_ core.ReplyContextReconstructor     = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
	_ core.ImageSender                   = (*Platform)(nil)
	_ core.FileSender                    = (*Platform)(nil)
	_ core.TypingIndicator               = (*Platform)(nil)
	_ core.EarlyInstantReplyRequester    = (*Platform)(nil)
	_ core.FinalOnlyTextRequester        = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform      = (*Platform)(nil)
)
