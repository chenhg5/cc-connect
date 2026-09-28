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
	"sync/atomic"
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

	// weixinChunkSendDelay is the delay between sending message chunks to avoid rate limiting.
	weixinChunkSendDelay = 100 * time.Millisecond
	// pendingFlushInterval is a best-effort background retry interval for persisted replies.
	pendingFlushInterval = 15 * time.Second
	// pendingRetryCooldown avoids repeatedly trying the same expired context_token.
	pendingRetryCooldown = 30 * time.Second
	// pendingMaxAttemptsPerToken caps background attempts until a new context_token arrives.
	pendingMaxAttemptsPerToken = 3
	defaultBurstLimit          = 4
	defaultBurstWindowSecs     = 86400
	// typingTicketTTL is how long a cached typing ticket remains valid.
	typingTicketTTL = 10 * time.Minute
	// typingRepeatInterval is how often to resend the typing status to keep it alive.
	typingRepeatInterval = 5 * time.Second
	// contextTokenFreshTTL is a conservative real-time send window for iLink replies.
	contextTokenFreshTTL = 90 * time.Second
	maxLongPollTimeout   = 60 * time.Second
	maxPendingReplyRunes = 12000
)

// sendPath labels the call site that reaches the outbound API so the burst
// budget can be scoped to where ilink actually throttles us (issue #1742).
//   - sendPathReply: reactive reply to an inbound user message. The gateway
//     does not throttle replies on interactive deployments, so the push-path
//     budget must NOT consume them.
//   - sendPathPush: proactive push (cron / timer / file transfer / SendImage /
//     SendFile / SendAudio). Counts against burst_limit.
//
// File transfers count as push because they share the same outbound
// sendMessage endpoint and are exactly the kind of "separate-message" traffic
// that the gateway throttles.
type sendPath string

const (
	sendPathReply sendPath = "reply"
	sendPathPush  sendPath = "push"
)

// pushBudgetExceededCounter is incremented every time the push-path burst
// budget is exhausted. Exposed via PushBudgetExceededTotal() for diagnostics
// (`cc-connect doctor` and on-demand queries). Per-process — the budget itself
// is per-account, but the counter is only observability and reset on restart.
var pushBudgetExceededCounter atomic.Int64

// PushBudgetExceededTotal returns how many times the push-path burst budget
// has blocked a send since process start. Replies are not counted because they
// bypass the quota entirely.
func PushBudgetExceededTotal() int64 {
	return pushBudgetExceededCounter.Load()
}

// resetPushBudgetExceededCounter is for tests only.
func resetPushBudgetExceededCounter() {
	pushBudgetExceededCounter.Store(0)
}

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

	// dedupEnabled mirrors the optional dedup_enabled config key (default true for
	// weixin because ilink can retransmit the same message_id on ACK delay —
	// see issue #1667). When false, the dispatch path skips the cache entirely.
	dedupEnabled bool
	// dedup tracks recently seen composite keys to absorb retransmissions.
	dedup *core.MessageDedup

	pauseMu    sync.Mutex
	pauseUntil time.Time

	tokensMu   sync.RWMutex
	tokens     map[string]contextTokenEntry
	tokensPath string

	typingMu      sync.RWMutex
	typingTickets map[string]typingTicketEntry // peerUserID → cached ticket

	pendingMu   sync.Mutex
	pendingPath string
	// Send-volume quota guarding against ilink's burst throttle (see constants).
	sendQuotaMu     sync.Mutex
	sendQuotaTimes  []time.Time
	sendQuotaLimit  int
	sendQuotaWindow time.Duration
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

	// Keep the default only when the option is absent. An explicit 0 disables
	// the local quota, as documented for interactive deployments.
	burstLimit := defaultBurstLimit
	if configuredLimit, ok := opts["burst_limit"]; ok {
		burstLimit = pickInt(configuredLimit)
		if burstLimit < 0 {
			burstLimit = 0
		}
	}
	burstWindow := pickInt(opts["burst_window_secs"])
	if burstWindow < 0 {
		burstWindow = 0
	}

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

	if burstWindow <= 0 {
		burstWindow = defaultBurstWindowSecs
	}

	// dedup_enabled (default true) and dedup_window_seconds (default 300s, the
	// legacy value) — see issue #1667. ilink can retransmit the same message_id
	// on ACK delay; without dedup the bot replies twice.
	dedupEnabled := pickBool(opts["dedup_enabled"])
	if _, ok := opts["dedup_enabled"]; !ok {
		dedupEnabled = true
	}
	dedupWindow := pickInt(opts["dedup_window_seconds"])
	if dedupWindow <= 0 {
		dedupWindow = 300
	}

	p := &Platform{
		token:           token,
		baseURL:         baseURL,
		cdnBaseURL:      cdnBaseURL,
		allowFrom:       allowFrom,
		routeTag:        routeTag,
		stateDir:        stateDir,
		longPollMS:      lp,
		accountLabel:    accountLabel,
		httpClient:      httpClient,
		cdnHttpClient:   cdnHttpClient,
		tokens:          make(map[string]contextTokenEntry),
		dedupEnabled:    dedupEnabled,
		dedup:           core.NewMessageDedup(time.Duration(dedupWindow) * time.Second),
		typingTickets:   make(map[string]typingTicketEntry),
		sendQuotaLimit:  burstLimit,
		sendQuotaWindow: time.Duration(burstWindow) * time.Second,
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

// pickBool interprets an any value as a bool. Numeric values are treated as
// "non-zero => true" so the same key works for both booleans and 0/1 ints.
func pickBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	default:
		return false
	}
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

	// Include create_time_ms and client_id so (seq,message_id)=(0,0) or duplicates
	// are less likely to collide. Dedup window is configurable via
	// dedup_window_seconds; the cache is a thin wrapper over core.MessageDedup
	// (see issue #1667).
	dedupKey := fmt.Sprintf("%s|%d|%d|%d|%s", from, m.MessageID, m.Seq, m.CreateTimeMs, strings.TrimSpace(m.ClientID))
	if p.dedupEnabled && p.dedup != nil && p.dedup.IsDuplicate(dedupKey) {
		slog.Debug("weixin: dropping duplicate message", "from", from, "message_id", m.MessageID, "seq", m.Seq)
		return
	}

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

// Reply sends a reactive text reply to an inbound user message. Replies bypass
// the push-path burst budget (issue #1742): the gateway does not throttle
// replies on interactive deployments, and counting them against the push
// budget silently bricked weixin bots at 4 replies per 24h after the v1.5.0
// default landed.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	_, err := p.ReplyWithReceipt(ctx, replyCtx, content)
	return err
}

// Send handles both foreground agent output and proactive messages. The engine
// uses Send, not Reply, for ordinary agent results, so the reply context must
// decide whether the push budget applies (see #1742).
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	_, err := p.SendWithReceipt(ctx, replyCtx, content)
	return err
}

func (p *Platform) ReplyWithReceipt(ctx context.Context, replyCtx any, content string) (*core.SendReceipt, error) {
	return p.sendChunksWithReceipt(ctx, replyCtx, content, sendPathReply)
}

func (p *Platform) SendWithReceipt(ctx context.Context, replyCtx any, content string) (*core.SendReceipt, error) {
	path := sendPathPush
	if rc, ok := replyCtx.(*replyContext); ok && rc != nil && !rc.proactive {
		path = sendPathReply
	}
	return p.sendChunksWithReceipt(ctx, replyCtx, content, path)
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

// checkSendQuota enforces the bot's separate-message budget so ilink's
// sendmessage throttle (ret=-2 "prepare failed") is not triggered. Live testing
// showed the gateway throttles a bot after roughly 5-6 separate messages per
// long window (about a day; matches the 24h context TTL), regardless of pacing,
// and that attempts made during the penalty escalate it. Multi-chunk sends do
// not count (a chunked message is one logical message). This quota counts
// logical messages in a sliding window and FAILS FAST once the budget is
// exhausted — waiting for the window to slide (up to a day) is useless, and the
// fail-fast philosophy (see sendChunk) applies: do not keep hammering a
// throttled bot. Configure via burst_limit / burst_window_secs platform
// options. A limit of 0 disables the quota.
//
// The quota is intentionally scoped to the PUSH path (proactive cron/timer and
// outbound media). The REPLY path bypasses it entirely: the gateway does not
// throttle replies on interactive deployments, and counting them against the
// push budget silently bricked weixin bots at 4 replies per 24h after the
// v1.5.0 default landed (issue #1742). File transfers count as push because
// they hit the same outbound API. See sendPath values below.
func (p *Platform) checkSendQuota(ctx context.Context, path sendPath) error {
	if path == sendPathReply {
		// Replies are never throttled by ilink in practice; applying the
		// push-path budget to replies would block interactive conversations
		// the moment the proactive-push budget is exhausted. The fixed-cost
		// window-bucket logic is also wrong for replies: a user can easily
		// exchange >4 messages in a day on a busy chat. Issue #1742.
		return nil
	}
	if p.sendQuotaLimit <= 0 || p.sendQuotaWindow <= 0 {
		return nil
	}
	p.sendQuotaMu.Lock()
	now := time.Now()
	cutoff := now.Add(-p.sendQuotaWindow)
	kept := p.sendQuotaTimes[:0]
	for _, t := range p.sendQuotaTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	p.sendQuotaTimes = kept
	used := len(p.sendQuotaTimes)
	if used >= p.sendQuotaLimit {
		p.sendQuotaMu.Unlock()
		pushBudgetExceededCounter.Add(1)
		slog.Error("weixin: push_path_budget_exceeded",
			"path", string(path),
			"used", used,
			"limit", p.sendQuotaLimit,
			"window", p.sendQuotaWindow.String(),
			"hint", "ilink throttles the bot after roughly 5-6 pushes per window — reduce cron/timer pushes or re-login later",
		)
		return fmt.Errorf("weixin: push budget exhausted (%d push messages in the last %s); "+
			"ilink throttles the bot after roughly 5-6 pushes per window — reduce messages or re-login later", p.sendQuotaLimit, p.sendQuotaWindow)
	}
	p.sendQuotaTimes = append(p.sendQuotaTimes, now)
	p.sendQuotaMu.Unlock()
	return nil
}

func (p *Platform) sendChunks(ctx context.Context, replyCtx any, content string, path sendPath) error {
	_, err := p.sendChunksWithReceipt(ctx, replyCtx, content, path)
	return err
}

func (p *Platform) sendChunksWithReceipt(ctx context.Context, replyCtx any, content string, path sendPath) (*core.SendReceipt, error) {
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
		p.enqueuePendingReply(rc.peerUserID, content, "missing_context_token")
		return nil, fmt.Errorf("weixin: missing context_token for peer %q - user must send a message to the bot first", rc.peerUserID)
	}
	if err := p.checkSendQuota(ctx, path); err != nil {
		p.enqueuePendingReply(rc.peerUserID, content, "throttled")
		return nil, err
	}
	if !p.replyContextTokenFresh(rc) {
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
		clientID := "cc-" + randomHex(6)
		err := p.sendChunkWithID(ctx, rc, chunk, clientID)
		if err != nil {
			slog.Error("weixin: chunk send failed, message incomplete",
				"peer", rc.peerUserID,
				"failed_chunk", fmt.Sprintf("%d/%d", i+1, total),
				"error", err)
			reason := "send_failed"
			if isSendThrottled(err) {
				reason = "throttled"
			} else {
				// A throttle rejects the notice too and every attempt worsens the penalty.
				notice := "⚠️ 消息发送不完整，请在终端查看完整结果。"
				if nerr := p.api.sendText(ctx, rc.peerUserID, notice, rc.contextToken, "cc-"+randomHex(6)); nerr != nil {
					slog.Warn("weixin: failed to send incomplete-delivery notice", "peer", rc.peerUserID, "error", nerr)
				}
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
	if entry.Reason == "throttled" {
		if queuedAt, err := time.Parse(time.RFC3339, entry.CreatedAt); err == nil {
			window := p.sendQuotaWindow
			if window <= 0 {
				window = defaultBurstWindowSecs * time.Second
			}
			if now.Sub(queuedAt) < window {
				return false
			}
		}
	}
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

	content := "上次未送达的回复，自动补发：\n\n" + entry.Content
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
			if isSendThrottled(err) {
				p.enqueuePendingReply(peer, entry.Content, "throttled")
			}
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

// isSendThrottled reports whether err is ilink sendmessage's burst-throttle
// response (ret=-2 "prepare failed"). This is a bot-wide rate-limit penalty, not a
// context_token problem: the gateway accepts any (or no) context_token on sends.
func isSendThrottled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ret=-2")
}

// sendChunk sends a single chunk. If ilink throttles the send (ret=-2
// "prepare failed"), it fails fast instead of retrying: live testing showed the
// penalty is escalated by every send attempt made while it is active, so retrying
// (e.g. the old 3×500ms loop plus the extra notice send) only prolongs the outage.
func (p *Platform) sendChunk(ctx context.Context, rc *replyContext, chunk string) error {
	clientID := "cc-" + randomHex(6)
	return p.sendChunkWithID(ctx, rc, chunk, clientID)
}

func (p *Platform) sendChunkWithID(ctx context.Context, rc *replyContext, chunk, clientID string) error {
	err := p.api.sendText(ctx, rc.peerUserID, chunk, rc.contextToken, clientID)
	if err == nil {
		if count := p.markContextSendAccepted(rc.peerUserID, rc.contextToken); count >= 8 {
			slog.Warn("weixin: reply allowance is near the observed per-context limit",
				"peer", rc.peerUserID, "accepted_count", count)
		}
		return nil
	}
	if isSendThrottled(err) {
		return fmt.Errorf("weixin: sendMessage throttled by ilink (ret=-2); "+
			"the bot is rate-limited and sending during the penalty escalates it, retry the message later: %w", err)
	}
	return err
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
