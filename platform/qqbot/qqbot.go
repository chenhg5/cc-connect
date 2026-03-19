package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("qqbot", New)
}

const (
	apiBaseProduction = "https://api.sgroup.qq.com"
	apiBaseSandbox    = "https://sandbox.api.sgroup.qq.com"
	tokenURL          = "https://bots.qq.com/app/getAppAccessToken"

	// Default intent: GROUP_AND_C2C_EVENT (1 << 25)
	defaultIntents = 1 << 25

	maxReconnectBackoff  = 60 * time.Second
	maxReconnectAttempts = 30
	tokenRefreshMargin   = 5 * time.Minute
	messageMaxLen        = 2000
	msgSeqTTL            = 5 * time.Minute
)

// WebSocket opcodes for the QQ Bot gateway protocol.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// Platform implements core.Platform for the official QQ Bot API v2.
type Platform struct {
	appID                 string
	appSecret             string
	sandbox               bool
	allowFrom             string
	shareSessionInChannel bool
	intents               int
	markdownSupport       bool // enable markdown messages (msg_type: 2)
	handler               core.MessageHandler
	cancel                context.CancelFunc

	// OAuth2 token management
	token       string
	tokenExpiry time.Time
	tokenMu     sync.RWMutex

	// WebSocket state
	wsConn       *websocket.Conn
	wsMu         sync.Mutex
	sessionID    string
	lastSeq      atomic.Int64
	heartbeatMs  int
	heartbeatOK  atomic.Bool
	reconnecting atomic.Bool

	// Message dedup
	dedup core.MessageDedup

	// msg_seq counter per event msg_id (for multiple replies to same event)
	msgSeqMu  sync.Mutex
	msgSeqMap map[string]*msgSeqEntry
}

// msgSeqEntry tracks msg_seq counter with a creation timestamp for TTL eviction.
type msgSeqEntry struct {
	seq       atomic.Int32
	createdAt time.Time
}

// replyContext carries the information needed to reply to a QQ Bot message.
type replyContext struct {
	messageType string // "group" or "c2c"
	groupOpenID string // for group messages
	userOpenID  string // user's openid (member_openid for group, user_openid for c2c)
	eventMsgID  string // msg_id from the incoming event, used for passive reply
}

// New creates a new QQ Bot platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	if appID == "" {
		return nil, fmt.Errorf("qqbot: app_id is required")
	}
	appSecret, _ := opts["app_secret"].(string)
	if appSecret == "" {
		return nil, fmt.Errorf("qqbot: app_secret is required")
	}

	sandbox, _ := opts["sandbox"].(bool)
	allowFrom, _ := opts["allow_from"].(string)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	markdownSupport, _ := opts["markdown_support"].(bool)

	intents := defaultIntents
	if v, ok := opts["intents"].(int); ok && v > 0 {
		intents = v
	} else if v, ok := opts["intents"].(float64); ok && v > 0 {
		intents = int(v)
	}

	core.CheckAllowFrom("qqbot", allowFrom)
	return &Platform{
		appID:                 appID,
		appSecret:             appSecret,
		sandbox:               sandbox,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
		intents:               intents,
		markdownSupport:       markdownSupport,
	}, nil
}

func (p *Platform) Name() string { return "qqbot" }

// Start connects to the QQ Bot gateway and begins receiving events.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	// Get initial access token
	if err := p.refreshToken(); err != nil {
		return fmt.Errorf("qqbot: failed to get access token: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	if err := p.connectGateway(ctx); err != nil {
		cancel()
		return fmt.Errorf("qqbot: failed to connect gateway: %w", err)
	}

	slog.Info("qqbot: connected to QQ Bot gateway", "sandbox", p.sandbox)
	return nil
}

// Reply sends a message as a reply to an incoming message.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.Send(ctx, replyCtx, content)
}

// Send sends a message to the conversation identified by replyCtx.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rctx, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("qqbot: invalid reply context")
	}

	chunks := core.SplitMessageCodeFenceAware(content, messageMaxLen)
	for _, chunk := range chunks {
		if err := p.sendMessage(rctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendImage uploads and sends an image via QQ Bot rich media API.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rctx, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("qqbot: SendImage: invalid reply context type %T", replyCtx)
	}

	fileInfo, err := p.uploadRichMedia(rctx, 1, img.Data)
	if err != nil {
		return fmt.Errorf("qqbot: upload image: %w", err)
	}

	var url string
	switch rctx.messageType {
	case "group":
		url = fmt.Sprintf("%s/v2/groups/%s/messages", p.apiBase(), rctx.groupOpenID)
	case "c2c":
		url = fmt.Sprintf("%s/v2/users/%s/messages", p.apiBase(), rctx.userOpenID)
	default:
		return fmt.Errorf("qqbot: unknown message type %q", rctx.messageType)
	}

	body := map[string]any{
		"msg_type": 7,
		"media":    map[string]any{"file_info": fileInfo},
	}
	if rctx.eventMsgID != "" {
		body["msg_id"] = rctx.eventMsgID
		body["msg_seq"] = p.nextMsgSeq(rctx.eventMsgID)
	}

	return p.apiRequest("POST", url, body)
}

// uploadRichMedia uploads a file to QQ Bot rich media API and returns the file_info.
// fileType: 1=image, 2=video, 3=audio, 4=file.
func (p *Platform) uploadRichMedia(rctx *replyContext, fileType int, data []byte) (string, error) {
	var url string
	switch rctx.messageType {
	case "group":
		url = fmt.Sprintf("%s/v2/groups/%s/files", p.apiBase(), rctx.groupOpenID)
	case "c2c":
		url = fmt.Sprintf("%s/v2/users/%s/files", p.apiBase(), rctx.userOpenID)
	default:
		return "", fmt.Errorf("qqbot: unknown message type %q", rctx.messageType)
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	reqBody := map[string]any{
		"file_type":    fileType,
		"file_data":    b64,
		"srv_send_msg": false,
	}

	var result struct {
		FileInfo string `json:"file_info"`
	}
	if err := p.apiRequestJSON("POST", url, reqBody, &result); err != nil {
		return "", err
	}
	if result.FileInfo == "" {
		return "", fmt.Errorf("qqbot: upload rich media: empty file_info")
	}
	return result.FileInfo, nil
}

// apiRequestJSON is like apiRequest but also decodes the response body into result.
func (p *Platform) apiRequestJSON(method, url string, body any, result any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qqbot: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("qqbot: get token: %w", err)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("qqbot: api request failed: %w", err)
	}
	defer resp.Body.Close()

	// Retry once on 401
	if resp.StatusCode == http.StatusUnauthorized {
		if err := p.refreshToken(); err != nil {
			return fmt.Errorf("qqbot: token refresh on 401: %w", err)
		}
		token, _ = p.getAccessToken()

		if body != nil {
			data, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(data)
		}
		req2, err := http.NewRequest(method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("qqbot: build retry request: %w", err)
		}
		req2.Header.Set("Authorization", "QQBot "+token)
		req2.Header.Set("Content-Type", "application/json")

		resp2, err := core.HTTPClient.Do(req2)
		if err != nil {
			return fmt.Errorf("qqbot: api retry failed: %w", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode >= 300 {
			raw, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("qqbot: api %s %s returned %d (after retry): %s", method, url, resp2.StatusCode, raw)
		}
		if result != nil {
			if err := json.NewDecoder(resp2.Body).Decode(result); err != nil {
				return fmt.Errorf("qqbot: decode response: %w", err)
			}
		}
		return nil
	}

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qqbot: api %s %s returned %d: %s", method, url, resp.StatusCode, raw)
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("qqbot: decode response: %w", err)
		}
	}
	return nil
}

var _ core.ImageSender = (*Platform)(nil)

// Stop shuts down the platform.
func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	if p.wsConn != nil {
		return p.wsConn.Close()
	}
	return nil
}

// ReconstructReplyCtx implements core.ReplyContextReconstructor.
// Session key format: "qqbot:{group_openid}:{member_openid}", "qqbot:g:{group_openid}" or "qqbot:{user_openid}"
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "qqbot" {
		return nil, fmt.Errorf("qqbot: invalid session key %q", sessionKey)
	}
	if len(parts) == 3 {
		if parts[1] == "g" {
			return &replyContext{
				messageType: "group",
				groupOpenID: parts[2],
			}, nil
		}
		return &replyContext{
			messageType: "group",
			groupOpenID: parts[1],
			userOpenID:  parts[2],
		}, nil
	}
	return &replyContext{
		messageType: "c2c",
		userOpenID:  parts[1],
	}, nil
}

// ---------------------------------------------------------------------------
// OAuth2 Token Management
// ---------------------------------------------------------------------------

func (p *Platform) refreshToken() error {
	body, _ := json.Marshal(map[string]string{
		"appId":        p.appID,
		"clientSecret": p.appSecret,
	})

	resp, err := core.HTTPClient.Post(tokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request returned %d: %s", resp.StatusCode, raw)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("token response decode: %w", err)
	}
	if result.AccessToken == "" {
		return fmt.Errorf("empty access_token in response")
	}

	var expiresSec int
	fmt.Sscanf(result.ExpiresIn, "%d", &expiresSec)
	if expiresSec <= 0 {
		expiresSec = 7200
	}

	p.tokenMu.Lock()
	p.token = result.AccessToken
	p.tokenExpiry = time.Now().Add(time.Duration(expiresSec) * time.Second)
	p.tokenMu.Unlock()

	slog.Debug("qqbot: access token refreshed", "expires_in", expiresSec)
	return nil
}

// getAccessToken returns the current token, refreshing if expired or near-expiry.
func (p *Platform) getAccessToken() (string, error) {
	p.tokenMu.RLock()
	token := p.token
	expiry := p.tokenExpiry
	p.tokenMu.RUnlock()

	if token != "" && time.Now().Before(expiry.Add(-tokenRefreshMargin)) {
		return token, nil
	}

	if err := p.refreshToken(); err != nil {
		return "", err
	}

	p.tokenMu.RLock()
	defer p.tokenMu.RUnlock()
	return p.token, nil
}

// ---------------------------------------------------------------------------
// WebSocket Gateway
// ---------------------------------------------------------------------------

func (p *Platform) connectGateway(ctx context.Context) error {
	token, err := p.getAccessToken()
	if err != nil {
		return err
	}

	// Get gateway URL
	gatewayURL, err := p.getGatewayURL(token)
	if err != nil {
		return err
	}

	// Connect WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, nil)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}

	p.wsMu.Lock()
	p.wsConn = conn
	p.wsMu.Unlock()

	// Wait for Hello (op 10)
	if err := p.waitForHello(conn); err != nil {
		conn.Close()
		return err
	}

	// Send Identify (op 2)
	if err := p.sendIdentify(conn, token); err != nil {
		conn.Close()
		return err
	}

	// Wait for READY event
	if err := p.waitForReady(conn); err != nil {
		conn.Close()
		return err
	}

	// Start heartbeat and read loop
	go p.heartbeatLoop(ctx)
	go p.readLoop(ctx)

	return nil
}

func (p *Platform) getGatewayURL(token string) (string, error) {
	req, err := http.NewRequest("GET", p.apiBase()+"/gateway/bot", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("gateway response decode: %w", err)
	}
	if result.URL == "" {
		return "", fmt.Errorf("empty gateway URL in response")
	}
	return result.URL, nil
}

type wsPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

func (p *Platform) waitForHello(conn *websocket.Conn) error {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var msg wsPayload
	if err := conn.ReadJSON(&msg); err != nil {
		return fmt.Errorf("waiting for hello: %w", err)
	}
	if msg.Op != opHello {
		return fmt.Errorf("expected op 10 (Hello), got op %d", msg.Op)
	}

	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(msg.D, &hello); err != nil {
		slog.Warn("qqbot: failed to parse Hello payload", "error", err)
	}
	if hello.HeartbeatInterval > 0 {
		p.heartbeatMs = hello.HeartbeatInterval
	} else {
		p.heartbeatMs = 41250 // sane default
	}
	p.heartbeatOK.Store(true)

	slog.Debug("qqbot: received Hello", "heartbeat_interval", p.heartbeatMs)
	return nil
}

func (p *Platform) sendIdentify(conn *websocket.Conn, token string) error {
	identify := map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":   fmt.Sprintf("QQBot %s", token),
			"intents": p.intents,
			"shard":   [2]int{0, 1},
		},
	}
	p.wsMu.Lock()
	err := conn.WriteJSON(identify)
	p.wsMu.Unlock()
	return err
}

func (p *Platform) waitForReady(conn *websocket.Conn) error {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var msg wsPayload
	if err := conn.ReadJSON(&msg); err != nil {
		return fmt.Errorf("waiting for ready: %w", err)
	}
	if msg.Op != opDispatch || msg.T != "READY" {
		return fmt.Errorf("expected READY event, got op=%d t=%s", msg.Op, msg.T)
	}

	var ready struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(msg.D, &ready); err != nil {
		slog.Warn("qqbot: failed to parse READY payload", "error", err)
	}
	p.sessionID = ready.SessionID
	if msg.S != nil {
		p.lastSeq.Store(*msg.S)
	}

	slog.Info("qqbot: gateway READY", "session_id", p.sessionID)
	return nil
}

func (p *Platform) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(p.heartbeatMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !p.heartbeatOK.Load() {
				slog.Warn("qqbot: no heartbeat ACK received, reconnecting")
				p.triggerReconnect(ctx)
				return
			}
			p.heartbeatOK.Store(false)
			p.sendHeartbeat()
		}
	}
}

func (p *Platform) sendHeartbeat() {
	seq := p.lastSeq.Load()
	var d json.RawMessage
	if seq > 0 {
		d, _ = json.Marshal(seq)
	} else {
		d = json.RawMessage("null")
	}

	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	if p.wsConn != nil {
		if err := p.wsConn.WriteJSON(wsPayload{Op: opHeartbeat, D: d}); err != nil {
			slog.Debug("qqbot: heartbeat send failed", "error", err)
		}
	}
}

func (p *Platform) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		p.wsMu.Lock()
		conn := p.wsConn
		p.wsMu.Unlock()
		if conn == nil {
			return
		}

		var msg wsPayload
		if err := conn.ReadJSON(&msg); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("qqbot: ws read error, reconnecting", "error", err)
			p.triggerReconnect(ctx)
			return
		}

		if msg.S != nil {
			p.lastSeq.Store(*msg.S)
		}

		switch msg.Op {
		case opDispatch:
			p.handleDispatch(msg.T, msg.D)
		case opHeartbeat:
			// Server requested heartbeat
			p.sendHeartbeat()
		case opReconnect:
			slog.Info("qqbot: server requested reconnect")
			p.triggerReconnect(ctx)
			return
		case opInvalidSession:
			var resumable bool
			if err := json.Unmarshal(msg.D, &resumable); err != nil {
				slog.Warn("qqbot: failed to parse InvalidSession payload", "error", err)
			}
			if resumable {
				slog.Info("qqbot: invalid session (resumable), attempting resume")
			} else {
				slog.Info("qqbot: invalid session (not resumable), re-identifying")
				p.sessionID = ""
			}
			p.triggerReconnect(ctx)
			return
		case opHeartbeatACK:
			p.heartbeatOK.Store(true)
		}
	}
}

func (p *Platform) triggerReconnect(ctx context.Context) {
	if p.reconnecting.CompareAndSwap(false, true) {
		go func() {
			defer p.reconnecting.Store(false)
			p.reconnectLoop(ctx)
		}()
	}
}

func (p *Platform) reconnectLoop(ctx context.Context) {
	// Close existing connection
	p.wsMu.Lock()
	if p.wsConn != nil {
		p.wsConn.Close()
		p.wsConn = nil
	}
	p.wsMu.Unlock()

	backoff := time.Second
	for attempt := 1; attempt <= maxReconnectAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(backoff)

		// Refresh token before reconnecting (may have expired)
		if err := p.refreshToken(); err != nil {
			slog.Warn("qqbot: token refresh failed during reconnect", "error", err, "attempt", attempt)
			backoff = min(backoff*2, maxReconnectBackoff)
			continue
		}

		token, _ := p.getAccessToken()

		gatewayURL, err := p.getGatewayURL(token)
		if err != nil {
			slog.Warn("qqbot: get gateway failed during reconnect", "error", err, "attempt", attempt)
			backoff = min(backoff*2, maxReconnectBackoff)
			continue
		}

		conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, nil)
		if err != nil {
			slog.Warn("qqbot: ws reconnect failed", "error", err, "attempt", attempt, "backoff", backoff)
			backoff = min(backoff*2, maxReconnectBackoff)
			continue
		}

		p.wsMu.Lock()
		p.wsConn = conn
		p.wsMu.Unlock()

		if err := p.waitForHello(conn); err != nil {
			slog.Warn("qqbot: hello failed during reconnect", "error", err)
			p.wsMu.Lock()
			p.wsConn = nil
			p.wsMu.Unlock()
			conn.Close()
			backoff = min(backoff*2, maxReconnectBackoff)
			continue
		}

		// closeAndNil closes the conn and clears p.wsConn so Stop()
		// and other code paths don't operate on a stale reference.
		closeAndNil := func() {
			p.wsMu.Lock()
			p.wsConn = nil
			p.wsMu.Unlock()
			conn.Close()
		}

		// Try Resume if we have a session_id, otherwise Identify
		if p.sessionID != "" {
			if err := p.sendResume(conn, token); err != nil {
				slog.Warn("qqbot: resume failed, falling back to identify", "error", err)
				p.sessionID = ""
				if err := p.sendIdentify(conn, token); err != nil {
					closeAndNil()
					backoff = min(backoff*2, maxReconnectBackoff)
					continue
				}
				if err := p.waitForReady(conn); err != nil {
					closeAndNil()
					backoff = min(backoff*2, maxReconnectBackoff)
					continue
				}
			}
		} else {
			if err := p.sendIdentify(conn, token); err != nil {
				closeAndNil()
				backoff = min(backoff*2, maxReconnectBackoff)
				continue
			}
			if err := p.waitForReady(conn); err != nil {
				closeAndNil()
				backoff = min(backoff*2, maxReconnectBackoff)
				continue
			}
		}

		slog.Info("qqbot: reconnected successfully")
		go p.heartbeatLoop(ctx)
		go p.readLoop(ctx)
		return
	}
	slog.Error("qqbot: failed to reconnect after max attempts", "attempts", maxReconnectAttempts)
}

func (p *Platform) sendResume(conn *websocket.Conn, token string) error {
	resume := map[string]any{
		"op": opResume,
		"d": map[string]any{
			"token":      fmt.Sprintf("QQBot %s", token),
			"session_id": p.sessionID,
			"seq":        p.lastSeq.Load(),
		},
	}
	p.wsMu.Lock()
	err := conn.WriteJSON(resume)
	p.wsMu.Unlock()
	return err
}

// ---------------------------------------------------------------------------
// Event Handling
// ---------------------------------------------------------------------------

func (p *Platform) handleDispatch(eventType string, data json.RawMessage) {
	switch eventType {
	case "GROUP_AT_MESSAGE_CREATE":
		p.handleGroupMessage(data)
	case "C2C_MESSAGE_CREATE":
		p.handleC2CMessage(data)
	case "RESUMED":
		slog.Info("qqbot: session resumed successfully")
	default:
		slog.Debug("qqbot: unhandled event", "type", eventType)
	}
}

func (p *Platform) handleGroupMessage(data json.RawMessage) {
	var d struct {
		ID          string       `json:"id"`
		GroupOpenID string       `json:"group_openid"`
		Content     string       `json:"content"`
		Timestamp   string       `json:"timestamp"`
		Attachments []attachment `json:"attachments"`
		Author      struct {
			MemberOpenID string `json:"member_openid"`
		} `json:"author"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		slog.Warn("qqbot: failed to parse group message", "error", err)
		return
	}

	if d.ID == "" || d.GroupOpenID == "" {
		return
	}

	// Check timestamp for old messages
	if ts, err := time.Parse(time.RFC3339, d.Timestamp); err == nil {
		if core.IsOldMessage(ts) {
			slog.Debug("qqbot: ignoring old group message", "id", d.ID)
			return
		}
	}

	if p.dedup.IsDuplicate(d.ID) {
		slog.Debug("qqbot: duplicate group message ignored", "id", d.ID)
		return
	}

	if !core.AllowList(p.allowFrom, d.Author.MemberOpenID) {
		return
	}

	// Strip leading @bot mention (the official API includes it as content prefix)
	content := stripAtMention(d.Content)

	// Download image attachments
	images := downloadAttachmentImages(d.Attachments)

	if content == "" && len(images) == 0 {
		return
	}

	var sessionKey string
	if p.shareSessionInChannel {
		sessionKey = fmt.Sprintf("qqbot:g:%s", d.GroupOpenID)
	} else {
		sessionKey = fmt.Sprintf("qqbot:%s:%s", d.GroupOpenID, d.Author.MemberOpenID)
	}

	rctx := &replyContext{
		messageType: "group",
		groupOpenID: d.GroupOpenID,
		userOpenID:  d.Author.MemberOpenID,
		eventMsgID:  d.ID,
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "qqbot",
		MessageID:  d.ID,
		UserID:     d.Author.MemberOpenID,
		UserName:   d.Author.MemberOpenID, // official API only provides openid, no nickname
		ChatName:   d.GroupOpenID,         // group openid as fallback (no group name API)
		Content:    content,
		Images:     images,
		ReplyCtx:   rctx,
	}

	slog.Debug("qqbot: group message received", "group", d.GroupOpenID, "user", d.Author.MemberOpenID, "len", len(content), "images", len(images))
	p.handler(p, msg)
}

func (p *Platform) handleC2CMessage(data json.RawMessage) {
	var d struct {
		ID          string       `json:"id"`
		Content     string       `json:"content"`
		Timestamp   string       `json:"timestamp"`
		Attachments []attachment `json:"attachments"`
		Author      struct {
			UserOpenID string `json:"user_openid"`
		} `json:"author"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		slog.Warn("qqbot: failed to parse c2c message", "error", err)
		return
	}

	if d.ID == "" {
		return
	}

	// Check timestamp for old messages
	if ts, err := time.Parse(time.RFC3339, d.Timestamp); err == nil {
		if core.IsOldMessage(ts) {
			slog.Debug("qqbot: ignoring old c2c message", "id", d.ID)
			return
		}
	}

	if p.dedup.IsDuplicate(d.ID) {
		slog.Debug("qqbot: duplicate c2c message ignored", "id", d.ID)
		return
	}

	if !core.AllowList(p.allowFrom, d.Author.UserOpenID) {
		return
	}

	content := strings.TrimSpace(d.Content)

	// Download image attachments
	images := downloadAttachmentImages(d.Attachments)

	if content == "" && len(images) == 0 {
		return
	}

	sessionKey := fmt.Sprintf("qqbot:%s", d.Author.UserOpenID)

	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  d.Author.UserOpenID,
		eventMsgID:  d.ID,
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "qqbot",
		MessageID:  d.ID,
		UserID:     d.Author.UserOpenID,
		UserName:   d.Author.UserOpenID,
		Content:    content,
		Images:     images,
		ReplyCtx:   rctx,
	}

	slog.Debug("qqbot: c2c message received", "user", d.Author.UserOpenID, "len", len(content), "images", len(images))
	p.handler(p, msg)
}

// ---------------------------------------------------------------------------
// Message Sending
// ---------------------------------------------------------------------------

func (p *Platform) sendMessage(rctx *replyContext, content string) error {
	var url string
	switch rctx.messageType {
	case "group":
		url = fmt.Sprintf("%s/v2/groups/%s/messages", p.apiBase(), rctx.groupOpenID)
	case "c2c":
		url = fmt.Sprintf("%s/v2/users/%s/messages", p.apiBase(), rctx.userOpenID)
	default:
		return fmt.Errorf("qqbot: unknown message type %q", rctx.messageType)
	}

	var body map[string]any
	if p.markdownSupport {
		// Markdown format (msg_type: 2)
		body = map[string]any{
			"markdown": map[string]any{
				"content": content,
			},
			"msg_type": 2,
		}
		slog.Debug("qqbot: sending markdown message", "content_len", len(content), "msg_type", 2)
	} else {
		// Plain text format (msg_type: 0)
		body = map[string]any{
			"content":  content,
			"msg_type": 0,
		}
		slog.Debug("qqbot: sending text message", "content_len", len(content), "msg_type", 0)
	}

	// Include msg_id for passive reply if available
	if rctx.eventMsgID != "" {
		body["msg_id"] = rctx.eventMsgID
		body["msg_seq"] = p.nextMsgSeq(rctx.eventMsgID)
	}

	return p.apiRequest("POST", url, body)
}

func (p *Platform) nextMsgSeq(eventMsgID string) int32 {
	p.msgSeqMu.Lock()
	defer p.msgSeqMu.Unlock()

	if p.msgSeqMap == nil {
		p.msgSeqMap = make(map[string]*msgSeqEntry)
	}

	// Evict expired entries
	now := time.Now()
	for k, v := range p.msgSeqMap {
		if now.Sub(v.createdAt) > msgSeqTTL {
			delete(p.msgSeqMap, k)
		}
	}

	entry, ok := p.msgSeqMap[eventMsgID]
	if !ok {
		entry = &msgSeqEntry{createdAt: now}
		p.msgSeqMap[eventMsgID] = entry
	}
	return entry.seq.Add(1)
}

// ---------------------------------------------------------------------------
// HTTP API Helper
// ---------------------------------------------------------------------------

func (p *Platform) apiBase() string {
	if p.sandbox {
		return apiBaseSandbox
	}
	return apiBaseProduction
}

func (p *Platform) apiRequest(method, url string, body any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qqbot: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("qqbot: get token: %w", err)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("qqbot: api request failed: %w", err)
	}
	defer resp.Body.Close()

	// Retry once on 401 (token may have expired)
	if resp.StatusCode == http.StatusUnauthorized {
		if err := p.refreshToken(); err != nil {
			return fmt.Errorf("qqbot: token refresh on 401: %w", err)
		}
		token, _ = p.getAccessToken()

		// Rebuild the request body reader
		if body != nil {
			data, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(data)
		}
		req2, err := http.NewRequest(method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("qqbot: build retry request: %w", err)
		}
		req2.Header.Set("Authorization", "QQBot "+token)
		req2.Header.Set("Content-Type", "application/json")

		resp2, err := core.HTTPClient.Do(req2)
		if err != nil {
			return fmt.Errorf("qqbot: api retry failed: %w", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode >= 300 {
			raw, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("qqbot: api %s %s returned %d (after retry): %s", method, url, resp2.StatusCode, raw)
		}
		return nil
	}

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qqbot: api %s %s returned %d: %s", method, url, resp.StatusCode, raw)
	}
	return nil
}

// attachment represents an image/file attachment in the QQ Bot API event payload.
type attachment struct {
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
}

// downloadAttachmentImages downloads all image attachments and returns ImageAttachments.
func downloadAttachmentImages(attachments []attachment) []core.ImageAttachment {
	var images []core.ImageAttachment
	for _, att := range attachments {
		if !strings.HasPrefix(att.ContentType, "image/") {
			continue
		}
		url := att.URL
		if url == "" {
			continue
		}
		// The official API may omit the https:// prefix
		if !strings.HasPrefix(url, "http") {
			url = "https://" + url
		}
		resp, err := core.HTTPClient.Get(url)
		if err != nil {
			slog.Warn("qqbot: download image failed", "url", url, "error", err)
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			slog.Warn("qqbot: read image body failed", "error", err)
			continue
		}
		mime := att.ContentType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		images = append(images, core.ImageAttachment{
			MimeType: mime,
			Data:     data,
			FileName: att.Filename,
		})
	}
	return images
}

// stripAtMention removes the leading @bot mention from group message content.
// The official QQ Bot API prefixes GROUP_AT_MESSAGE_CREATE content with an
// @mention tag like "<@!botid> " or sometimes just whitespace after the tag.
func stripAtMention(content string) string {
	content = strings.TrimSpace(content)
	// The official API formats the @mention as "<@!{bot_id}>"
	if strings.HasPrefix(content, "<@!") {
		if idx := strings.Index(content, ">"); idx >= 0 {
			content = strings.TrimSpace(content[idx+1:])
		}
	}
	return content
}
