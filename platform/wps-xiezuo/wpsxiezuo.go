package wpsxiezuo

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

var (
	wsEndpoint    = "wss://openapi.wps.cn/v7/event/ws"
	tokenEndpoint = "https://openapi.wps.cn"
	maxBackoff    = 60 * time.Second
)

// Platform implements core.Platform for WPS Xiezuo (WPS 协作).
type Platform struct {
	appID       string
	appSecret   string
	baseURL     string
	cleanReply  bool
	allowFrom   string
	handler     core.MessageHandler
	cancel      context.CancelFunc
	conn        *websocket.Conn
	mu          sync.Mutex // protects conn access
	writeCh     chan any   // serializes all WebSocket writes (ACK, reactions, etc.)
	dedup       core.MessageDedup
	token       string
	tokenExpire time.Time
	tokenMu     sync.Mutex
	stopOnce    sync.Once
	stopped     bool
}

// replyContext holds the context needed to reply to a specific message.
type replyContext struct {
	ChatID    string `json:"chat_id"`
	ChatType  string `json:"chat_type"`
	CompanyID string `json:"company_id"`
	MessageID string `json:"message_id"`
	SenderID  string `json:"sender_id"`
}

// --- WPS event frame types ---

// wpsEventFrame represents an event frame from the WPS WebSocket.
type wpsEventFrame struct {
	Topic         string `json:"topic"`
	Operation     string `json:"operation"`
	Time          int64  `json:"time"`
	Nonce         string `json:"nonce"`
	Signature     string `json:"signature"`
	EncryptedData string `json:"encrypted_data"`
	AccessKey     string `json:"access_key"`
}

// wpsGoAwayFrame represents a goaway control frame.
type wpsGoAwayFrame struct {
	Type        string `json:"type"`
	Reason      string `json:"reason"`
	Message     string `json:"message"`
	ReconnectMs int64  `json:"reconnect_ms,omitempty"`
}

// pongFrame is sent through writeCh to reply to a server ping.
type pongFrame struct {
	Type string `json:"-"` // not JSON-encoded, used for type switch
	Data string
}

// pingControl is sent through writeCh to send a client ping.
type pingControl struct{}

// wpsMessageData represents the decrypted message event data.
type wpsMessageData struct {
	Chat      wpsChatInfo    `json:"chat"`
	CompanyID string         `json:"company_id"`
	Message   wpsMessageInfo `json:"message"`
	SendTime  int64          `json:"send_time"`
	Sender    wpsSenderInfo  `json:"sender"`
}

type wpsChatInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type wpsMessageInfo struct {
	Content json.RawMessage `json:"content"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
}

type wpsSenderInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// --- Message create API ---

type sendMessageRequest struct {
	Type     string         `json:"type"`
	Receiver receiverInfo   `json:"receiver"`
	Content  messageContent `json:"content"`
}

type receiverInfo struct {
	Type       string `json:"type"`
	ReceiverID string `json:"receiver_id"`
}

type messageContent struct {
	Text textContent `json:"text"`
}

type textContent struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

// --- Token API ---

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// --- Reaction API ---

type reactionRequest struct {
	ReactionType string `json:"reaction_type"`
}

// --- Factory ---

func init() {
	core.RegisterPlatform("wps-xiezuo", New)
}

// New creates a new WPS Xiezuo platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("wps-xiezuo: app_id and app_secret are required")
	}

	baseURL := tokenEndpoint
	if v, ok := opts["base_url"].(string); ok && v != "" {
		baseURL = strings.TrimRight(v, "/")
	}

	cleanReply, _ := opts["clean_reply"].(bool)
	allowFrom, _ := opts["allow_from"].(string)

	core.CheckAllowFrom("wps-xiezuo", allowFrom)

	return &Platform{
		appID:      appID,
		appSecret:  appSecret,
		baseURL:    baseURL,
		cleanReply: cleanReply,
		allowFrom:  allowFrom,
	}, nil
}

func (p *Platform) Name() string { return "wps-xiezuo" }

// Start begins the WebSocket connection loop.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.connectLoop(ctx)
	return nil
}

// Stop cancels the context and closes the WebSocket connection.
func (p *Platform) Stop() error {
	p.stopOnce.Do(func() {
		p.stopped = true
		if p.cancel != nil {
			p.cancel()
		}
		p.mu.Lock()
		if p.conn != nil {
			p.conn.Close()
			p.conn = nil
		}
		p.mu.Unlock()
	})
	return nil
}

// --- WebSocket connection loop with exponential backoff ---

func (p *Platform) connectLoop(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := p.runConnection(ctx)
		if p.stopped || ctx.Err() != nil {
			return
		}

		// Reset backoff if connection was alive long enough
		if time.Since(start) > 2*time.Minute {
			backoff = time.Second
		}

		slog.Warn("wps-xiezuo: connection lost, reconnecting", "error", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (p *Platform) runConnection(ctx context.Context) error {
	slog.Info("wps-xiezuo: connecting", "endpoint", wsEndpoint)

	header, err := p.signWSHeader()
	if err != nil {
		return fmt.Errorf("sign header: %w", err)
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, wsEndpoint, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	p.mu.Lock()
	p.conn = conn
	writeCh := make(chan any, 64)
	p.writeCh = writeCh
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.conn = nil
		p.writeCh = nil
		p.mu.Unlock()
		close(writeCh)
		conn.Close()
	}()

	slog.Info("wps-xiezuo: connected")

	// Set up control frame handlers.
	// CRITICAL: Both handlers must reset the read deadline, otherwise
	// the connection times out even though heartbeats are flowing.
	const pingTimeout = 90 * time.Second
	conn.SetPingHandler(func(appData string) error {
		slog.Debug("wps-xiezuo: server ping received")
		_ = conn.SetReadDeadline(time.Now().Add(pingTimeout))
		p.mu.Lock()
		ch := p.writeCh
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- pongFrame{Type: "pong", Data: appData}:
			default:
			}
		}
		return nil
	})
	conn.SetPongHandler(func(appData string) error {
		slog.Debug("wps-xiezuo: server pong received")
		_ = conn.SetReadDeadline(time.Now().Add(pingTimeout))
		return nil
	})

	// Start writer goroutine to serialize all WebSocket writes
	writeCtx, writeCancel := context.WithCancel(ctx)
	defer writeCancel()
	go p.writeLoop(writeCtx, conn, writeCh)

	// Send client pings every 25s to keep the connection alive
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-writeCtx.Done():
				return
			case <-ticker.C:
				p.mu.Lock()
				ch := p.writeCh
				p.mu.Unlock()
				if ch != nil {
					select {
					case ch <- pingControl{}:
					default:
					}
				}
			}
		}
	}()

	// Read deadline: 90s for PING timeout (matching Node.js SDK)
	_ = conn.SetReadDeadline(time.Now().Add(pingTimeout))

	// Read loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		slog.Info("wps-xiezuo: message received", "type", msgType, "len", len(raw), "data", string(raw))

		// Reset deadline on successful read
		_ = conn.SetReadDeadline(time.Now().Add(pingTimeout))

		p.handleRawMessage(ctx, raw)
	}
}

// writeLoop serializes all WebSocket writes (ACK frames, pongs, pings, etc.) on a single goroutine.
// gorilla/websocket requires all writes to be serialized.
func (p *Platform) writeLoop(ctx context.Context, conn *websocket.Conn, writeCh chan any) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-writeCh:
			if !ok {
				return
			}
			switch v := msg.(type) {
			case pongFrame:
				if err := conn.WriteControl(websocket.PongMessage, []byte(v.Data), time.Now().Add(5*time.Second)); err != nil {
					slog.Debug("wps-xiezuo: pong write error", "error", err)
					return
				}
				slog.Debug("wps-xiezuo: pong sent")
			case pingControl:
				if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
					slog.Debug("wps-xiezuo: ping write error", "error", err)
					return
				}
				slog.Debug("wps-xiezuo: client ping sent")
			default:
				if err := conn.WriteJSON(msg); err != nil {
					slog.Debug("wps-xiezuo: write error", "error", err)
					return
				}
			}
		}
	}
}

// --- KSO-1 HMAC-SHA256 signing ---

func (p *Platform) signWSHeader() (http.Header, error) {
	u, err := url.Parse(wsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}

	uri := u.RequestURI()
	dateStr := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")

	// stringToSign = "KSO-1" + method + uri + contentType + date + sha256Hex
	// For WebSocket: method=GET, contentType="", sha256Hex=""
	stringToSign := "KSO-1" + "GET" + uri + "" + dateStr + ""

	mac := hmac.New(sha256.New, []byte(p.appSecret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	header := http.Header{
		"X-Kso-Date":          {dateStr},
		"X-Kso-Authorization": {fmt.Sprintf("KSO-1 %s:%s", p.appID, signature)},
		"X-Ack-Mode":          {"required"},
	}

	return header, nil
}

// --- Raw message dispatch ---

func (p *Platform) handleRawMessage(ctx context.Context, raw []byte) {
	// Try to detect frame type
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		slog.Warn("wps-xiezuo: invalid json", "error", err)
		return
	}

	// GoAway frame: has "type": "goaway"
	if t, ok := probe["type"]; ok {
		var typeStr string
		if err := json.Unmarshal(t, &typeStr); err == nil {
			if typeStr == "goaway" {
				var goAway wpsGoAwayFrame
				if err := json.Unmarshal(raw, &goAway); err == nil {
					p.handleGoAway(goAway)
				}
				return
			}
			// Ignore other control frames (ack, etc.)
			slog.Debug("wps-xiezuo: control frame", "type", typeStr)
			return
		}
	}

	// Event frame: has "topic" and "operation"
	if _, hasTopic := probe["topic"]; hasTopic {
		var event wpsEventFrame
		if err := json.Unmarshal(raw, &event); err != nil {
			slog.Warn("wps-xiezuo: parse event frame failed", "error", err)
			return
		}
		p.handleEvent(event)
		return
	}

	slog.Debug("wps-xiezuo: unknown frame", "data", string(raw))
}

// --- GoAway handling ---

func (p *Platform) handleGoAway(goAway wpsGoAwayFrame) {
	slog.Warn("wps-xiezuo: goaway received", "reason", goAway.Reason, "message", goAway.Message)

	if goAway.Reason == "connection_replaced" {
		slog.Warn("wps-xiezuo: connection replaced, stopping reconnect")
		p.stopped = true
		_ = p.Stop()
		return
	}

	// For other reasons (server_shutdown etc.), normal reconnect will happen
	if goAway.ReconnectMs > 0 {
		time.Sleep(time.Duration(goAway.ReconnectMs) * time.Millisecond)
	}
}

// --- Event handling ---

func (p *Platform) handleEvent(event wpsEventFrame) {
	// Verify signature
	if !p.verifyEventSignature(event) {
		slog.Warn("wps-xiezuo: signature verification failed", "topic", event.Topic, "nonce", event.Nonce)
		return
	}

	// Decrypt data
	plain, err := p.decryptEventData(event.Nonce, event.EncryptedData)
	if err != nil {
		slog.Warn("wps-xiezuo: decrypt failed", "error", err, "topic", event.Topic)
		return
	}

	// Dispatch by topic+operation. Avoid logging decrypted user content.
	slog.Info("wps-xiezuo: decrypted event", "topic", event.Topic, "operation", event.Operation, "payload_bytes", len(plain))
	switch {
	case event.Topic == "kso.app_chat.message" && event.Operation == "create":
		p.sendAck(event.Nonce, nil)
		p.handleChatMessage(plain)
	case event.Topic == "kso.app_chat.message.recall":
		p.sendAck(event.Nonce, nil)
		p.handleChatMessageRecall(plain)
	default:
		p.sendAck(event.Nonce, nil)
		slog.Debug("wps-xiezuo: unhandled event", "topic", event.Topic, "operation", event.Operation)
	}
}

// --- Signature verification ---

func (p *Platform) verifyEventSignature(event wpsEventFrame) bool {
	content := fmt.Sprintf("%s:%s:%s:%d:%s", p.appID, event.Topic, event.Nonce, event.Time, event.EncryptedData)
	mac := hmac.New(sha256.New, []byte(p.appSecret))
	mac.Write([]byte(content))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	expectedSig = strings.TrimRight(expectedSig, "=")

	return hmac.Equal([]byte(event.Signature), []byte(expectedSig))
}

// --- AES-256-CBC decryption ---

func (p *Platform) decryptEventData(nonce, encryptedData string) ([]byte, error) {
	// key = MD5(appSecret).hexdigest() → 32 bytes
	hash := md5.Sum([]byte(p.appSecret))
	key := []byte(hex.EncodeToString(hash[:])) // 32 bytes

	// iv = nonce[:16]
	iv := []byte(nonce)
	if len(iv) > 16 {
		iv = iv[:16]
	}
	if len(iv) < 16 {
		// Pad with zeros if nonce is shorter than 16 bytes
		iv = append(iv, make([]byte, 16-len(iv))...)
	}

	// Base64 decode ciphertext
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	// AES-CBC decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not multiple of block size")
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpadding
	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		return nil, fmt.Errorf("pkcs7 unpad: %w", err)
	}

	return plaintext, nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen > len(data) || padLen > aes.BlockSize {
		return nil, fmt.Errorf("invalid padding length %d", padLen)
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid padding byte at %d", i)
		}
	}
	return data[:len(data)-padLen], nil
}

// --- ACK ---

func (p *Platform) sendAck(nonce string, err error) {
	if nonce == "" {
		return
	}
	ack := map[string]any{
		"type":  "ack",
		"nonce": nonce,
		"code":  200,
	}
	if err != nil {
		ack["code"] = 500
		ack["msg"] = err.Error()
		if len(err.Error()) > 256 {
			ack["msg"] = err.Error()[:256]
		}
	}
	p.mu.Lock()
	ch := p.writeCh
	p.mu.Unlock()
	if ch != nil {
		select {
		case ch <- ack:
			slog.Info("wps-xiezuo: ack queued", "nonce", nonce)
		default:
			slog.Warn("wps-xiezuo: write channel full, dropping ack", "nonce", nonce)
		}
	}
}

// --- Chat message handling ---

func (p *Platform) handleChatMessage(plain []byte) {
	var msgData wpsMessageData
	if err := json.Unmarshal(plain, &msgData); err != nil {
		slog.Warn("wps-xiezuo: parse message data failed", "error", err)
		return
	}

	if p.dedup.IsDuplicate(msgData.Message.ID) {
		slog.Debug("wps-xiezuo: skipping duplicate message", "msg_id", msgData.Message.ID)
		return
	}

	if !core.AllowList(p.allowFrom, msgData.Sender.ID) {
		slog.Debug("wps-xiezuo: message from unauthorized user", "user", msgData.Sender.ID)
		return
	}

	// Extract text content
	text := extractText(msgData.Message.Content)
	if text == "" {
		slog.Debug("wps-xiezuo: no text content in message", "msg_id", msgData.Message.ID)
		return
	}

	// Build session key. P2P sessions include both actual chat ID and sender ID:
	// chat ID is needed for proactive sends, sender ID keeps the session user-scoped.
	sessionKey := fmt.Sprintf("wps-xiezuo:%s:%s", msgData.CompanyID, msgData.Chat.ID)
	if isP2P(msgData.Chat.Type) {
		sessionKey = fmt.Sprintf("wps-xiezuo:%s:%s:%s", msgData.CompanyID, msgData.Chat.ID, msgData.Sender.ID)
	}

	rctx := replyContext{
		ChatID:    msgData.Chat.ID, // Always use actual chat ID for WPS API
		ChatType:  msgData.Chat.Type,
		CompanyID: msgData.CompanyID,
		MessageID: msgData.Message.ID,
		SenderID:  msgData.Sender.ID,
	}

	go p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "wps-xiezuo",
		MessageID:  msgData.Message.ID,
		UserID:     msgData.Sender.ID,
		UserName:   msgData.Sender.ID, // WPS doesn't include name in event data
		Content:    text,
		ChannelKey: msgData.Chat.ID, // needed for per-group session isolation (#1217)
		ReplyCtx:   rctx,
	})
}

func (p *Platform) handleChatMessageRecall(plain []byte) {
	// Recall event has a flat structure: {"chat_id":"...","id":"...","operator":{...}}
	var recallData struct {
		ChatID    string `json:"chat_id"`
		ID        string `json:"id"`
		CompanyID string `json:"company_id"`
		Operator  struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"operator"`
	}
	if err := json.Unmarshal(plain, &recallData); err != nil {
		slog.Warn("wps-xiezuo: parse recall data failed", "error", err)
		return
	}

	sessionKey := fmt.Sprintf("wps-xiezuo:%s:%s", recallData.CompanyID, recallData.ChatID)

	rctx := replyContext{
		ChatID:    recallData.ChatID,
		ChatType:  "p2p",
		CompanyID: recallData.CompanyID,
		MessageID: recallData.ID,
		SenderID:  recallData.Operator.ID,
	}

	go p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "wps-xiezuo",
		MessageID:  recallData.ID,
		Recalled:   true,
		UserID:     recallData.Operator.ID,
		ReplyCtx:   rctx,
	})
}

// --- Text extraction ---

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// WPS v7 format: {"text":{"content":"xxx"}}
	var wpsContent struct {
		Text struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(raw, &wpsContent); err == nil && wpsContent.Text.Content != "" {
		return strings.TrimSpace(wpsContent.Text.Content)
	}

	// Try {"type":"text","content":"xxx"}
	var simple struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &simple); err == nil {
		if simple.Type == "text" || simple.Type == "" {
			return extractStringContent(simple.Content)
		}
		if simple.Type == "rich_text" {
			return extractRichText(simple.Content)
		}
	}

	// Fallback: try as plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}

	return ""
}

func extractStringContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	// Try as {"content":"xxx"}
	var obj struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Content)
	}
	return strings.TrimSpace(string(raw))
}

func extractRichText(raw json.RawMessage) string {
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

func isP2P(chatType string) bool {
	return chatType == "p2p" || chatType == "single" || chatType == "direct"
}

// --- Reply/Send ---

// Reply sends a message back to the WPS chat via REST API.
func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	return p.sendWPSMessage(ctx, rctx, content)
}

// Send sends a proactive message to the WPS chat via REST API.
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.sendWPSMessage(ctx, rctx, content)
}

func (p *Platform) sendWPSMessage(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("wps-xiezuo: invalid reply context type %T", rctx)
	}
	if content == "" {
		return nil
	}

	if p.cleanReply {
		content = cleanReplyContent(content)
	}

	token, err := p.getToken(ctx)
	if err != nil {
		return fmt.Errorf("wps-xiezuo: get token: %w", err)
	}

	reqBody := sendMessageRequest{
		Type: "text",
		Receiver: receiverInfo{
			Type:       "chat",
			ReceiverID: rc.ChatID,
		},
		Content: messageContent{
			Text: textContent{
				Content: content,
				Type:    "markdown",
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("wps-xiezuo: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v7/messages/create", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wps-xiezuo: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("wps-xiezuo: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wps-xiezuo: send failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	slog.Debug("wps-xiezuo: message sent", "chat_id", rc.ChatID, "len", len(content))
	return nil
}

// --- Token management ---

func (p *Platform) getToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.token != "" && time.Now().Before(p.tokenExpire.Add(-60*time.Second)) {
		return p.token, nil
	}

	formData := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {p.appID},
		"client_secret": {p.appSecret},
	}

	// Try primary endpoint first, then fallback
	endpoints := []string{p.baseURL + "/oauth2/token", p.baseURL + "/openapi/oauth2/token"}
	var lastErr error

	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
			continue
		}

		var tokenResp tokenResponse
		if err := json.Unmarshal(respBody, &tokenResp); err != nil {
			lastErr = err
			continue
		}

		if tokenResp.AccessToken == "" {
			lastErr = fmt.Errorf("empty access_token")
			continue
		}

		p.token = tokenResp.AccessToken
		if tokenResp.ExpiresIn > 0 {
			p.tokenExpire = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		} else {
			p.tokenExpire = time.Now().Add(7200 * time.Second)
		}

		slog.Info("wps-xiezuo: token obtained", "expires_in", tokenResp.ExpiresIn)
		return p.token, nil
	}

	return "", fmt.Errorf("wps-xiezuo: all token endpoints failed: %w", lastErr)
}

// --- Reaction API (typing indicator) ---

func (p *Platform) addReaction(ctx context.Context, rctx replyContext, reactionType string) error {
	token, err := p.getToken(ctx)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(reactionRequest{ReactionType: reactionType})
	url := fmt.Sprintf("%s/v7/chats/%s/messages/%s/reactions/create", p.baseURL, rctx.ChatID, rctx.MessageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add reaction failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (p *Platform) deleteReaction(ctx context.Context, rctx replyContext, reactionType string) error {
	token, err := p.getToken(ctx)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(reactionRequest{ReactionType: reactionType})
	url := fmt.Sprintf("%s/v7/chats/%s/messages/%s/reactions/delete", p.baseURL, rctx.ChatID, rctx.MessageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete reaction failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return nil
}

// --- Optional interface: ReplyContextReconstructor ---

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// Formats:
	//   wps-xiezuo:{company_id}:{chat_id}             - group or legacy P2P
	//   wps-xiezuo:{company_id}:{chat_id}:{sender_id} - P2P, user-scoped
	parts := strings.SplitN(sessionKey, ":", 4)
	if len(parts) < 3 || parts[0] != "wps-xiezuo" {
		return nil, fmt.Errorf("wps-xiezuo: invalid session key %q", sessionKey)
	}
	rc := replyContext{
		ChatID:    parts[2],
		CompanyID: parts[1],
	}
	if len(parts) == 4 {
		rc.ChatType = "p2p"
		rc.SenderID = parts[3]
	}
	return rc, nil
}

// --- Optional interface: TypingIndicator ---

func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}
	if rc.ChatID == "" || rc.MessageID == "" {
		return func() {}
	}
	if err := p.addReaction(ctx, rc, "emoji_busy"); err != nil {
		slog.Debug("wps-xiezuo: add typing reaction failed", "error", err)
	}
	return func() {}
}

// --- Optional interface: TypingIndicatorDone ---

func (p *Platform) AddDoneReaction(rctx any) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return
	}
	if rc.ChatID == "" || rc.MessageID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.deleteReaction(ctx, rc, "emoji_busy"); err != nil {
		slog.Debug("wps-xiezuo: delete typing reaction failed", "error", err)
	}
}

// --- Clean reply content ---

func cleanReplyContent(content string) string {
	lines := strings.Split(content, "\n")
	var filtered []string
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "💭") || strings.HasPrefix(trimmed, "🔧") || strings.HasPrefix(trimmed, "🧾") {
			continue
		}
		filtered = append(filtered, line)
	}
	result := strings.Join(filtered, "\n")
	result = strings.TrimSpace(result)
	if result == "" {
		return content // Return original if everything was filtered
	}
	return result
}

// --- Compile-time interface assertions ---

var (
	_ core.Platform                  = (*Platform)(nil)
	_ core.ReplyContextReconstructor = (*Platform)(nil)
	_ core.TypingIndicator           = (*Platform)(nil)
	_ core.TypingIndicatorDone       = (*Platform)(nil)
)
