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
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
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

const (
	defaultMaxAttachmentBytes = 50 * 1024 * 1024
	resourceDownloadTimeout   = 2 * time.Minute
	maxResourceAPIResponse    = 64 * 1024
	maxCloudDocumentBytes     = 4 * 1024 * 1024
	maxCloudDocumentResponse  = 8 * 1024 * 1024
)

// Platform implements core.Platform for WPS Xiezuo (WPS 协作).
type Platform struct {
	appID              string
	appSecret          string
	baseURL            string
	cleanReply         bool
	allowFrom          string
	handler            core.MessageHandler
	cancel             context.CancelFunc
	conn               *websocket.Conn
	mu                 sync.Mutex // protects conn access
	writeCh            chan any   // serializes all WebSocket writes (ACK, reactions, etc.)
	dedup              core.MessageDedup
	token              string
	tokenExpire        time.Time
	tokenMu            sync.Mutex
	httpClient         *http.Client
	maxAttachmentBytes int64
	stopOnce           sync.Once
	stopped            bool
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

type wpsMessageContent struct {
	Image    *wpsImageContent    `json:"image"`
	File     *wpsFileContent     `json:"file"`
	RichText *wpsRichTextContent `json:"rich_text"`
}

type wpsImageContent struct {
	Height     int    `json:"height"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	StorageKey string `json:"storage_key"`
	Type       string `json:"type"`
	Width      int    `json:"width"`
}

type wpsFileContent struct {
	Type  string               `json:"type"`
	Local *wpsLocalFileContent `json:"local"`
	Cloud *wpsCloudFileContent `json:"cloud"`
}

type wpsLocalFileContent struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	StorageKey string `json:"storage_key"`
}

type wpsCloudFileContent struct {
	ID      string `json:"id"`
	LinkID  string `json:"link_id"`
	LinkURL string `json:"link_url"`
}

type wpsRichTextContent struct {
	Elements []wpsRichTextElement `json:"elements"`
}

type wpsRichTextElement struct {
	Type             string                  `json:"type"`
	AltText          string                  `json:"alt_text"`
	Elements         []wpsRichTextElement    `json:"elements"`
	TextContent      *wpsRichTextTextContent `json:"text_content"`
	StyleTextContent *wpsStyleTextContent    `json:"style_text_content"`
	MentionContent   *wpsMentionContent      `json:"mention_content"`
	ImageContent     *wpsImageContent        `json:"image_content"`
	LinkContent      *wpsRichTextLinkContent `json:"link_content"`
	DocContent       *wpsRichTextDocContent  `json:"doc_content"`
}

type wpsRichTextTextContent struct {
	Content string `json:"content"`
}

type wpsStyleTextContent struct {
	Text string `json:"text"`
}

type wpsMentionContent struct {
	Text string `json:"text"`
}

type wpsRichTextLinkContent struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type wpsRichTextDocContent struct {
	Text string              `json:"text"`
	File wpsCloudFileContent `json:"file"`
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

type resourceDownloadResponse struct {
	Data struct {
		URL string `json:"url"`
	} `json:"data"`
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type cloudLinkMetaResponse struct {
	Data struct {
		FileID  string `json:"file_id"`
		DriveID string `json:"drive_id"`
		URL     string `json:"url"`
	} `json:"data"`
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type cloudDocumentContentResponse struct {
	Data struct {
		Markdown string `json:"markdown"`
		Plain    string `json:"plain"`
		HTML     string `json:"html"`
	} `json:"data"`
	Code int    `json:"code"`
	Msg  string `json:"msg"`
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
		appID:              appID,
		appSecret:          appSecret,
		baseURL:            baseURL,
		cleanReply:         cleanReply,
		allowFrom:          allowFrom,
		httpClient:         &http.Client{Timeout: resourceDownloadTimeout},
		maxAttachmentBytes: defaultMaxAttachmentBytes,
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

	header := p.signKSO1Header(http.MethodGet, u.RequestURI(), "", nil)
	header.Set("X-Ack-Mode", "required")
	return header, nil
}

func (p *Platform) signKSO1Header(method, requestURI, contentType string, body []byte) http.Header {
	dateStr := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	sha256Hex := ""
	if len(body) > 0 {
		hash := sha256.Sum256(body)
		sha256Hex = hex.EncodeToString(hash[:])
	}

	stringToSign := "KSO-1" + method + requestURI + contentType + dateStr + sha256Hex

	mac := hmac.New(sha256.New, []byte(p.appSecret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	return http.Header{
		"X-Kso-Date":          {dateStr},
		"X-Kso-Authorization": {fmt.Sprintf("KSO-1 %s:%s", p.appID, signature)},
	}
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

	if strings.EqualFold(msgData.Message.Type, "image") || strings.EqualFold(msgData.Message.Type, "file") {
		go p.handleMediaMessage(msgData)
		return
	}
	if strings.EqualFold(msgData.Message.Type, "rich_text") {
		go p.handleRichTextMessage(msgData)
		return
	}

	text := extractText(msgData.Message.Content)
	if text == "" {
		slog.Debug("wps-xiezuo: no supported content in message", "msg_id", msgData.Message.ID, "type", msgData.Message.Type)
		return
	}
	go p.dispatchChatMessage(msgData, text, nil, nil)
}

func (p *Platform) handleMediaMessage(msgData wpsMessageData) {
	var content wpsMessageContent
	if err := json.Unmarshal(msgData.Message.Content, &content); err != nil {
		slog.Warn("wps-xiezuo: parse media content failed", "error", err, "msg_id", msgData.Message.ID, "type", msgData.Message.Type)
		return
	}

	var (
		text   string
		images []core.ImageAttachment
		files  []core.FileAttachment
	)

	switch {
	case strings.EqualFold(msgData.Message.Type, "image"):
		image := content.Image
		if image == nil || strings.TrimSpace(image.StorageKey) == "" {
			slog.Warn("wps-xiezuo: image message missing storage_key", "msg_id", msgData.Message.ID)
			return
		}
		if err := p.validateAttachmentSize(image.Size); err != nil {
			slog.Warn("wps-xiezuo: image rejected", "error", err, "msg_id", msgData.Message.ID, "file_name", image.Name)
			return
		}

		data, responseType, err := p.downloadMessageResource(msgData.Chat.ID, msgData.Message.ID, image.StorageKey, image.Name)
		if err != nil {
			slog.Warn("wps-xiezuo: image download failed", "error", err, "msg_id", msgData.Message.ID, "file_name", image.Name)
			return
		}
		images = []core.ImageAttachment{{
			MimeType: chooseAttachmentMIME(image.Type, responseType, image.Name, data),
			Data:     data,
			FileName: sanitizeAttachmentName(image.Name, "image"),
		}}

	case strings.EqualFold(msgData.Message.Type, "file"):
		if content.File == nil {
			slog.Warn("wps-xiezuo: file message missing file content", "msg_id", msgData.Message.ID)
			return
		}

		switch {
		case strings.EqualFold(content.File.Type, "local") && content.File.Local != nil:
			local := content.File.Local
			if strings.TrimSpace(local.StorageKey) == "" {
				slog.Warn("wps-xiezuo: local file missing storage_key", "msg_id", msgData.Message.ID, "file_name", local.Name)
				return
			}
			if err := p.validateAttachmentSize(local.Size); err != nil {
				slog.Warn("wps-xiezuo: file rejected", "error", err, "msg_id", msgData.Message.ID, "file_name", local.Name)
				return
			}

			data, responseType, err := p.downloadMessageResource(msgData.Chat.ID, msgData.Message.ID, local.StorageKey, local.Name)
			if err != nil {
				slog.Warn("wps-xiezuo: file download failed", "error", err, "msg_id", msgData.Message.ID, "file_name", local.Name)
				return
			}
			name := sanitizeAttachmentName(local.Name, "attachment")
			files = []core.FileAttachment{{
				MimeType: chooseAttachmentMIME("", responseType, name, data),
				Data:     data,
				FileName: name,
			}}

		case strings.EqualFold(content.File.Type, "cloud") && content.File.Cloud != nil:
			text = p.enrichCloudDocument(context.Background(), "", *content.File.Cloud)
			if text == "" {
				slog.Warn("wps-xiezuo: cloud document missing link_url", "msg_id", msgData.Message.ID)
				return
			}

		default:
			slog.Debug("wps-xiezuo: unsupported file content", "msg_id", msgData.Message.ID, "file_type", content.File.Type)
			return
		}
	}

	p.dispatchChatMessage(msgData, text, images, files)
}

func (p *Platform) handleRichTextMessage(msgData wpsMessageData) {
	text, richImages, richDocuments, err := parseWPSRichText(msgData.Message.Content)
	if err != nil {
		slog.Warn("wps-xiezuo: parse rich text failed", "error", err, "msg_id", msgData.Message.ID)
		return
	}

	images := make([]core.ImageAttachment, 0, len(richImages))
	for _, image := range richImages {
		if strings.TrimSpace(image.StorageKey) == "" {
			continue
		}
		if err := p.validateAttachmentSize(image.Size); err != nil {
			slog.Warn("wps-xiezuo: rich text image rejected", "error", err, "msg_id", msgData.Message.ID, "file_name", image.Name)
			continue
		}

		data, responseType, err := p.downloadMessageResource(msgData.Chat.ID, msgData.Message.ID, image.StorageKey, image.Name)
		if err != nil {
			slog.Warn("wps-xiezuo: rich text image download failed", "error", err, "msg_id", msgData.Message.ID, "file_name", image.Name)
			continue
		}
		images = append(images, core.ImageAttachment{
			MimeType: chooseAttachmentMIME(image.Type, responseType, image.Name, data),
			Data:     data,
			FileName: sanitizeAttachmentName(image.Name, "image"),
		})
	}
	for _, document := range richDocuments {
		rendered := p.enrichCloudDocument(context.Background(), document.Title, document.File)
		base := formatWPSRichLink(document.Title, document.File.LinkURL)
		if rendered != base {
			text = appendCloudDocumentContent(text, strings.TrimSpace(strings.TrimPrefix(rendered, base)))
		}
	}

	if strings.TrimSpace(text) == "" && len(images) == 0 {
		slog.Debug("wps-xiezuo: rich text contains no supported content", "msg_id", msgData.Message.ID)
		return
	}
	p.dispatchChatMessage(msgData, text, images, nil)
}

func (p *Platform) dispatchChatMessage(msgData wpsMessageData, text string, images []core.ImageAttachment, files []core.FileAttachment) {
	if p.handler == nil {
		slog.Warn("wps-xiezuo: message handler is not configured", "msg_id", msgData.Message.ID)
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

	p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "wps-xiezuo",
		MessageID:  msgData.Message.ID,
		UserID:     msgData.Sender.ID,
		UserName:   msgData.Sender.ID, // WPS doesn't include name in event data
		Content:    text,
		Images:     images,
		Files:      files,
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
		RichText *wpsRichTextContent `json:"rich_text"`
	}
	if err := json.Unmarshal(raw, &wpsContent); err == nil && wpsContent.Text.Content != "" {
		return strings.TrimSpace(wpsContent.Text.Content)
	}
	if wpsContent.RichText != nil {
		text, _ := flattenWPSRichText(wpsContent.RichText.Elements)
		return text
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

func parseWPSRichText(raw json.RawMessage) (string, []wpsImageContent, []wpsRichDocument, error) {
	var content wpsMessageContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return "", nil, nil, fmt.Errorf("decode rich text content: %w", err)
	}
	if content.RichText == nil {
		return "", nil, nil, fmt.Errorf("missing rich_text content")
	}
	text, images := flattenWPSRichText(content.RichText.Elements)
	return text, images, collectWPSRichDocuments(content.RichText.Elements), nil
}

type wpsRichDocument struct {
	Title string
	File  wpsCloudFileContent
}

func collectWPSRichDocuments(elements []wpsRichTextElement) []wpsRichDocument {
	documents := make([]wpsRichDocument, 0)
	for _, element := range elements {
		if strings.EqualFold(element.Type, "doc") && element.DocContent != nil {
			documents = append(documents, wpsRichDocument{
				Title: strings.TrimSpace(element.DocContent.Text),
				File:  element.DocContent.File,
			})
		}
		documents = append(documents, collectWPSRichDocuments(element.Elements)...)
	}
	return documents
}

func flattenWPSRichText(elements []wpsRichTextElement) (string, []wpsImageContent) {
	lines := make([]string, 0, len(elements))
	images := make([]wpsImageContent, 0)
	for _, element := range elements {
		text, nestedImages := flattenWPSRichTextElement(element)
		if strings.TrimSpace(text) != "" {
			lines = append(lines, strings.TrimSpace(text))
		}
		images = append(images, nestedImages...)
	}
	return strings.Join(lines, "\n"), images
}

func flattenWPSRichTextElement(element wpsRichTextElement) (string, []wpsImageContent) {
	parts := make([]string, 0, 2)
	images := make([]wpsImageContent, 0)

	switch strings.ToLower(element.Type) {
	case "text", "emoji":
		if element.TextContent != nil {
			parts = appendNonEmpty(parts, element.TextContent.Content)
		} else if element.StyleTextContent != nil {
			parts = appendNonEmpty(parts, element.StyleTextContent.Text)
		}
	case "mention":
		if element.MentionContent != nil {
			parts = appendNonEmpty(parts, element.MentionContent.Text)
		}
	case "link":
		if element.LinkContent != nil {
			parts = appendNonEmpty(parts, formatWPSRichLink(element.LinkContent.Text, element.LinkContent.URL))
		}
	case "doc":
		if element.DocContent != nil {
			parts = appendNonEmpty(parts, formatWPSRichLink(element.DocContent.Text, element.DocContent.File.LinkURL))
		}
	case "image", "custom_emoji":
		if element.ImageContent != nil && strings.TrimSpace(element.ImageContent.StorageKey) != "" {
			images = append(images, *element.ImageContent)
		}
	default:
		if element.TextContent != nil {
			parts = appendNonEmpty(parts, element.TextContent.Content)
		} else if element.StyleTextContent != nil {
			parts = appendNonEmpty(parts, element.StyleTextContent.Text)
		}
	}

	for _, child := range element.Elements {
		text, nestedImages := flattenWPSRichTextElement(child)
		parts = appendNonEmpty(parts, text)
		images = append(images, nestedImages...)
	}
	if len(parts) == 0 && len(images) == 0 {
		parts = appendNonEmpty(parts, element.AltText)
	}
	return strings.Join(parts, ""), images
}

func appendNonEmpty(parts []string, value string) []string {
	if strings.TrimSpace(value) != "" {
		return append(parts, value)
	}
	return parts
}

func formatWPSRichLink(text, rawURL string) string {
	text = strings.TrimSpace(text)
	rawURL = strings.TrimSpace(rawURL)
	switch {
	case rawURL == "":
		return text
	case text == "" || text == rawURL:
		return rawURL
	default:
		return fmt.Sprintf("%s (%s)", text, rawURL)
	}
}

func (p *Platform) enrichCloudDocument(ctx context.Context, title string, cloud wpsCloudFileContent) string {
	linkURL := strings.TrimSpace(cloud.LinkURL)
	base := formatWPSRichLink(title, linkURL)
	if strings.TrimSpace(cloud.LinkID) == "" {
		return base
	}

	content, err := p.readCloudDocument(ctx, cloud.LinkID)
	if err != nil {
		slog.Warn("wps-xiezuo: cloud document content unavailable", "error", err, "link_id", cloud.LinkID)
		return base
	}
	if strings.TrimSpace(content) == "" {
		return base
	}
	slog.Info("wps-xiezuo: cloud document content loaded",
		"link_id", cloud.LinkID,
		"content_len", len(content),
	)
	if base == "" {
		base = linkURL
	}
	return base + "\n\n[WPS云文档正文（已由应用授权读取，请优先基于以下正文回答，不要通过网页链接再次访问）]\n" + content
}

func appendCloudDocumentContent(existing, document string) string {
	if strings.TrimSpace(document) == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return document
	}
	if strings.Contains(existing, "[WPS云文档正文（已由应用授权读取") || strings.Contains(existing, "[云文档正文]") {
		return existing + "\n\n" + document
	}
	return existing + "\n\n" + document
}

func (p *Platform) readCloudDocument(ctx context.Context, linkID string) (string, error) {
	if strings.TrimSpace(linkID) == "" {
		return "", fmt.Errorf("cloud document link_id is empty")
	}
	requestCtx, cancel := context.WithTimeout(ctx, resourceDownloadTimeout)
	defer cancel()

	token, err := p.getToken(requestCtx)
	if err != nil {
		return "", fmt.Errorf("get cloud document token: %w", err)
	}

	metaURI := fmt.Sprintf("/v7/links/%s/meta", url.PathEscape(linkID))
	meta, err := p.doSignedJSON(requestCtx, http.MethodGet, metaURI, token, maxResourceAPIResponse)
	if err != nil {
		return "", fmt.Errorf("resolve cloud document link: %w", err)
	}
	var metaResponse cloudLinkMetaResponse
	if err := json.Unmarshal(meta, &metaResponse); err != nil {
		return "", fmt.Errorf("parse cloud document link: %w", err)
	}
	if metaResponse.Code != 0 {
		return "", fmt.Errorf("cloud document link API: code=%d msg=%s", metaResponse.Code, metaResponse.Msg)
	}
	if strings.TrimSpace(metaResponse.Data.FileID) == "" || strings.TrimSpace(metaResponse.Data.DriveID) == "" {
		return "", fmt.Errorf("cloud document link API returned incomplete file metadata")
	}

	query := url.Values{"format": {"markdown"}}
	contentURI := fmt.Sprintf("/v7/drives/%s/files/%s/content?%s",
		url.PathEscape(metaResponse.Data.DriveID), url.PathEscape(metaResponse.Data.FileID), query.Encode())
	contentBody, err := p.doSignedJSON(requestCtx, http.MethodGet, contentURI, token, maxCloudDocumentResponse)
	if err != nil {
		return "", fmt.Errorf("read cloud document content: %w", err)
	}
	var contentResponse cloudDocumentContentResponse
	if err := json.Unmarshal(contentBody, &contentResponse); err != nil {
		return "", fmt.Errorf("parse cloud document content: %w", err)
	}
	if contentResponse.Code != 0 {
		return "", fmt.Errorf("cloud document content API: code=%d msg=%s", contentResponse.Code, contentResponse.Msg)
	}
	content := strings.TrimSpace(contentResponse.Data.Markdown)
	if content == "" {
		content = strings.TrimSpace(contentResponse.Data.Plain)
	}
	if content == "" {
		content = strings.TrimSpace(contentResponse.Data.HTML)
	}
	if len([]byte(content)) > maxCloudDocumentBytes {
		return "", fmt.Errorf("cloud document content exceeds %d bytes", maxCloudDocumentBytes)
	}
	return content, nil
}

func (p *Platform) doSignedJSON(ctx context.Context, method, requestURI, token string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+requestURI, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for key, values := range p.signKSO1Header(method, requestURI, "", nil) {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status=%d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	return body, nil
}

func (p *Platform) downloadMessageResource(chatID, messageID, storageKey, fileName string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), resourceDownloadTimeout)
	defer cancel()

	downloadURL, err := p.getMessageResourceDownloadURL(ctx, chatID, messageID, storageKey, fileName)
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build resource download request: %w", err)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download resource: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download resource: status=%d", resp.StatusCode)
	}
	limit := p.attachmentSizeLimit()
	if resp.ContentLength > limit {
		return nil, "", fmt.Errorf("download resource: content length %d exceeds limit %d", resp.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("read resource: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("download resource: body exceeds limit %d", limit)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("download resource: empty body")
	}

	return data, resp.Header.Get("Content-Type"), nil
}

func (p *Platform) getMessageResourceDownloadURL(ctx context.Context, chatID, messageID, storageKey, fileName string) (string, error) {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(messageID) == "" || strings.TrimSpace(storageKey) == "" {
		return "", fmt.Errorf("message resource requires chat_id, message_id, and storage_key")
	}

	token, err := p.getToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get resource token: %w", err)
	}

	requestURI := fmt.Sprintf("/v7/chats/%s/messages/%s/resources/%s/download",
		url.PathEscape(chatID), url.PathEscape(messageID), url.PathEscape(storageKey))
	if strings.TrimSpace(fileName) != "" {
		query := url.Values{"file_name": {fileName}}
		requestURI += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+requestURI, nil)
	if err != nil {
		return "", fmt.Errorf("build resource URL request: %w", err)
	}
	for key, values := range p.signKSO1Header(http.MethodGet, requestURI, "", nil) {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("request resource URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request resource URL: status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResourceAPIResponse+1))
	if err != nil {
		return "", fmt.Errorf("read resource URL response: %w", err)
	}
	if len(body) > maxResourceAPIResponse {
		return "", fmt.Errorf("resource URL response exceeds %d bytes", maxResourceAPIResponse)
	}

	var result resourceDownloadResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse resource URL response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("resource URL API: code=%d msg=%s", result.Code, result.Msg)
	}
	downloadURL := strings.TrimSpace(result.Data.URL)
	if err := validateResourceDownloadURL(downloadURL); err != nil {
		return "", err
	}
	return downloadURL, nil
}

func (p *Platform) client() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return http.DefaultClient
}

func (p *Platform) attachmentSizeLimit() int64 {
	if p.maxAttachmentBytes > 0 {
		return p.maxAttachmentBytes
	}
	return defaultMaxAttachmentBytes
}

func (p *Platform) validateAttachmentSize(size int64) error {
	if size < 0 {
		return fmt.Errorf("invalid declared size %d", size)
	}
	if size > p.attachmentSizeLimit() {
		return fmt.Errorf("declared size %d exceeds limit %d", size, p.attachmentSizeLimit())
	}
	return nil
}

func validateResourceDownloadURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid resource download URL: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid resource download URL")
	}
	return nil
}

func sanitizeAttachmentName(name, fallback string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fallback
	}
	return name
}

func chooseAttachmentMIME(declared, responseType, fileName string, data []byte) string {
	generic := ""
	for _, candidate := range []string{declared, responseType} {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(candidate))
		if err != nil || mediaType == "" {
			continue
		}
		if mediaType != "application/octet-stream" {
			return mediaType
		}
		generic = mediaType
	}
	if byExtension := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName))); byExtension != "" {
		if mediaType, _, err := mime.ParseMediaType(byExtension); err == nil {
			return mediaType
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	if generic != "" {
		return generic
	}
	return "application/octet-stream"
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

	// WPS Open Platform v7 messages API: outer message `type` MUST be one of
	// the API-defined message-type enums (text / rich_text / image / file /
	// audio / video / card) — passing "markdown" makes the API reject the
	// request with `400000002 invalid open_v7_message_type: "markdown"`.
	//
	// Markdown rendering is opted into via the INNER `Content.Text.Type =
	// "markdown"` field (the only other inner enum is "plain"). When the
	// inner field is "markdown" WPS renders the content with a CommonMark
	// subset, and CommonMark collapses a single "\n" between non-empty
	// lines into a space. To force a real line break we must emit either
	// "  \n" (two trailing spaces) or "\n\n" — see the official docs at
	// https://open.wps.cn/documents/app-integration-dev/guide/robot/webhook
	//
	// We use the trailing-spaces form so we preserve paragraph structure
	// (no spurious blank lines) and stay safe inside fenced code blocks
	// (the two extra trailing whitespace characters inside ``` blocks are
	// preserved verbatim but visually invisible).
	content = applyWPSLineBreaks(content)

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

	resp, err := p.client().Do(req)
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

		resp, err := p.client().Do(req)
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

	resp, err := p.client().Do(req)
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

	resp, err := p.client().Do(req)
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

// applyWPSLineBreaks converts engine-emitted "\n" into the form that WPS
// Open Platform markdown actually renders as a line break.
//
// Per WPS docs (https://open.wps.cn/documents/app-integration-dev/guide/robot/webhook
// markdown section), a single "\n" between two non-empty lines is collapsed
// into a space (standard CommonMark behaviour); to force a hard line break
// you must use either "two trailing spaces + \n" or a blank line ("\n\n").
//
// We pick the trailing-spaces form because:
//   - It preserves the original paragraph structure (no spurious blank lines).
//   - It is idempotent if the content already uses "\n\n" — replacing the
//     bare "\n" inside an empty line with "  \n" still renders correctly.
//   - It is safe inside fenced code blocks (the two trailing spaces are
//     whitespace inside a block where the markdown renderer preserves
//     content verbatim, so visually nothing changes).
//
// We deliberately do not normalize "\r\n" → "\n" first; cc-connect engine
// emits Unix newlines, and forcing the transform on already-converted
// "  \n" would over-indent (which is also visually benign but pointless).
func applyWPSLineBreaks(content string) string {
	if content == "" || !strings.Contains(content, "\n") {
		return content
	}
	// Replace bare "\n" not already preceded by "  " (two spaces).
	// Cheap two-pass implementation: temporarily mark already-broken
	// newlines, then convert the rest, then restore the markers.
	const marker = "\x00WPS_HARD_BREAK\x00"
	content = strings.ReplaceAll(content, "  \n", marker)
	content = strings.ReplaceAll(content, "\n", "  \n")
	content = strings.ReplaceAll(content, marker, "  \n")
	return content
}

// --- Compile-time interface assertions ---

var (
	_ core.Platform                  = (*Platform)(nil)
	_ core.ReplyContextReconstructor = (*Platform)(nil)
	_ core.TypingIndicator           = (*Platform)(nil)
	_ core.TypingIndicatorDone       = (*Platform)(nil)
)
