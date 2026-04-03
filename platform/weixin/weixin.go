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
)

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

	syncBufMu   sync.Mutex
	syncBuf     string
	syncBufPath string

	dedupMu sync.Mutex
	dedup   map[string]time.Time

	pauseMu    sync.Mutex
	pauseUntil time.Time

	tokensMu   sync.RWMutex
	tokens     map[string]string
	tokensPath string
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
		tokens:       make(map[string]string),
		dedup:        make(map[string]time.Time),
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

		p.syncBufMu.Lock()
		if resp.GetUpdatesBuf != "" {
			p.persistSyncBuf(resp.GetUpdatesBuf)
		}
		p.syncBufMu.Unlock()

		p.mu.RLock()
		h := p.handler
		p.mu.RUnlock()
		if h == nil {
			continue
		}
		for i := range resp.Msgs {
			p.dispatchInbound(ctx, &resp.Msgs[i], h)
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

	if tok := strings.TrimSpace(m.ContextToken); tok != "" {
		p.setContextToken(from, tok)
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

func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.sendChunks(ctx, replyCtx, content)
}

func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.sendChunks(ctx, replyCtx, content)
}

func (p *Platform) sendChunks(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return fmt.Errorf("weixin: invalid reply context")
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		rc.contextToken = p.getContextToken(rc.peerUserID)
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		return fmt.Errorf("weixin: missing context_token for peer %q", rc.peerUserID)
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	for _, chunk := range splitUTF8(content, maxWeixinChunk) {
		if err := p.api.sendText(ctx, rc.peerUserID, chunk, rc.contextToken, "cc-"+randomHex(6)); err != nil {
			return fmt.Errorf("weixin: send: %w", err)
		}
	}
	return nil
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
)
