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

	// Send-volume quota that keeps the bot under ilink's burst throttle
	// (sendMessage ret=-2 "prepare failed"). Live testing showed the gateway
	// throttles the bot after roughly 5-6 separate messages within a short window,
	// and that the penalty is escalated by every send attempt made while it is
	// active. We pace separate messages (not chunks: multi-chunk sends are fine)
	// to stay well below the trigger. Configurable via burst_limit /
	// burst_window_secs platform options.
	defaultBurstLimit      = 4     // max separate messages per window
	defaultBurstWindowSecs = 86400 // window length (24h: ilink budgets ~5-6 sends/day)
	// typingTicketTTL is how long a cached typing ticket remains valid.
	typingTicketTTL = 10 * time.Minute
	// typingRepeatInterval is how often to resend the typing status to keep it alive.
	typingRepeatInterval = 5 * time.Second
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
	peerUserID   string
	contextToken string
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
	tokens     map[string]string
	tokensPath string

	typingMu      sync.RWMutex
	typingTickets map[string]typingTicketEntry // peerUserID → cached ticket

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
	accountLabel, _ := opts["account_id"].(string)
	if accountLabel == "" {
		accountLabel = "default"
	}
	lp := pickInt(opts["long_poll_timeout_ms"])

	// Send-volume quota (see defaultBurstLimit constants). An explicitly
	// configured 0 disables the quota as documented in config.example.toml;
	// only an absent key falls back to the default (issue #1716).
	burstLimit := defaultBurstLimit
	if v, ok := opts["burst_limit"]; ok {
		burstLimit = pickInt(v)
	}
	if burstLimit < 0 {
		burstLimit = 0
	}
	burstWindow := defaultBurstWindowSecs
	if v, ok := opts["burst_window_secs"]; ok {
		burstWindow = pickInt(v)
	}
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
		tokens:          make(map[string]string),
		dedupEnabled:    dedupEnabled,
		dedup:           core.NewMessageDedup(time.Duration(dedupWindow) * time.Second),
		typingTickets:   make(map[string]typingTicketEntry),
		sendQuotaLimit:  burstLimit,
		sendQuotaWindow: time.Duration(burstWindow) * time.Second,
	}
	p.api = newAPIClient(baseURL, token, routeTag, httpClient)

	if stateDir != "" {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return nil, fmt.Errorf("weixin: create state dir: %w", err)
		}
		p.syncBufPath = filepath.Join(stateDir, "get_updates.buf")
		p.tokensPath = filepath.Join(stateDir, "context_tokens.json")
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
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return
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

func (p *Platform) setContextToken(peer, tok string) {
	if peer == "" || tok == "" {
		return
	}
	p.tokensMu.Lock()
	if p.tokens == nil {
		p.tokens = make(map[string]string)
	}
	p.tokens[peer] = tok
	p.tokensMu.Unlock()
	p.persistTokens()
}

func (p *Platform) getContextToken(peer string) string {
	p.tokensMu.RLock()
	defer p.tokensMu.RUnlock()
	return p.tokens[peer]
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

		timeoutMs := p.longPollMS
		resp, err := p.api.getUpdates(ctx, buf, timeoutMs)
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

	if tok := strings.TrimSpace(m.ContextToken); tok != "" {
		p.setContextToken(from, tok)
		p.refreshTypingTicket(ctx, from, tok)
	}

	body := bodyFromItemList(m.ItemList)
	images, files, audio := p.collectInboundMedia(ctx, m.ItemList)
	if strings.TrimSpace(body) == "" && len(images) == 0 && len(files) == 0 && audio == nil && mediaOnlyItems(m.ItemList) {
		body = "[收到媒体消息：CDN 下载或解密失败，或未配置 cdn_base_url；请改用文字说明。]"
	}
	if strings.TrimSpace(body) == "" && len(images) == 0 && len(files) == 0 && audio == nil {
		return
	}

	rc := &replyContext{peerUserID: from, contextToken: strings.TrimSpace(m.ContextToken)}
	msgID := fmt.Sprintf("%d", m.MessageID)
	if m.MessageID == 0 {
		msgID = randomHex(8)
	}

	h(p, &core.Message{
		SessionKey: sessionKeyPrefix + from,
		Platform:   p.Name(),
		MessageID:  msgID,
		UserID:     from,
		UserName:   shortWeixinUser(from),
		Content:    body,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ReplyCtx:   rc,
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
	return p.sendChunks(ctx, replyCtx, content, sendPathReply)
}

// Send proactively pushes a message to the user (cron / timer / Relay). Pushes
// count against the burst budget because ilink DOES throttle proactive sends
// (see #1643 / #1742).
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.sendChunks(ctx, replyCtx, content, sendPathPush)
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
	if len(p.sendQuotaTimes) >= p.sendQuotaLimit {
		p.sendQuotaMu.Unlock()
		pushBudgetExceededCounter.Add(1)
		slog.Error("weixin: push_path_budget_exceeded",
			"path", string(path),
			"used", len(p.sendQuotaTimes),
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
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("weixin: invalid reply context")
	}
	if err := p.checkSendQuota(ctx, path); err != nil {
		return err
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		rc.contextToken = p.getContextToken(rc.peerUserID)
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		slog.Error("weixin: cannot send message - missing context_token",
			"peer", rc.peerUserID,
			"content_preview", truncatePreview(content, 100),
			"hint", "user needs to send a message to the bot first so a context_token can be captured")
		return fmt.Errorf("weixin: missing context_token for peer %q - user must send a message to the bot first", rc.peerUserID)
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	chunks := splitUTF8(content, maxWeixinChunk)
	total := len(chunks)
	for i, chunk := range chunks {
		// Add delay between chunks to avoid rate limiting (except for first chunk)
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(weixinChunkSendDelay):
			}
		}
		err := p.sendChunk(ctx, rc, chunk)
		if err != nil {
			slog.Error("weixin: chunk send failed, message incomplete",
				"peer", rc.peerUserID,
				"failed_chunk", fmt.Sprintf("%d/%d", i+1, total),
				"error", err)
			// Notify user that message delivery was incomplete, unless the failure
			// is the ilink throttle: the notice send would be refused too, only
			// adding another throttled request.
			if !isSendThrottled(err) {
				notice := "⚠️ 消息发送不完整，请在终端查看完整结果。"
				noticeID := "cc-" + randomHex(6)
				if nerr := p.api.sendText(ctx, rc.peerUserID, notice, rc.contextToken, noticeID); nerr != nil {
					slog.Warn("weixin: failed to send incomplete-delivery notice", "peer", rc.peerUserID, "error", nerr)
				}
			}
			return fmt.Errorf("weixin: send chunk %d/%d: %w", i+1, total, err)
		}
	}
	return nil
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
	err := p.api.sendText(ctx, rc.peerUserID, chunk, rc.contextToken, clientID)
	if err == nil {
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
	tok := p.getContextToken(peer)
	if tok == "" {
		return nil, fmt.Errorf("weixin: no stored context_token for %q (user must message the bot first)", peer)
	}
	return &replyContext{peerUserID: peer, contextToken: tok}, nil
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
	_ core.AsyncRecoverablePlatform      = (*Platform)(nil)
)
