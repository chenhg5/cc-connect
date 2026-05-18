package weibo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

const (
	defaultTokenEndpoint = "https://open-im.api.weibo.com/open/auth/ws_token"
	defaultWSEndpoint    = "ws://open-im.api.weibo.com/ws/stream"

	pingInterval    = 30 * time.Second
	pongTimeout     = 40 * time.Second
	reconnectDelay  = 3 * time.Second
	maxReconnect    = 10 * time.Second
	tokenRenewBuf   = 60 * time.Second
	maxTextPerChunk = 2000
	maxSeenMessages = 1000
)

func init() {
	core.RegisterPlatform("weibo", New)
}

type replyContext struct {
	fromUserID string
	sessionKey string
}

type Platform struct {
	name          string
	appID         string
	appSecret     string
	tokenEndpoint string
	wsEndpoint    string
	allowFrom     string

	handler core.MessageHandler

	ws     *websocket.Conn
	wsMu   sync.Mutex
	connMu sync.Mutex

	token       string
	tokenExpiry time.Time
	tokenMu     sync.Mutex
	uid         string

	ctx    context.Context
	cancel context.CancelFunc

	seen   map[string]struct{}
	seenMu sync.Mutex
}

func New(opts map[string]any) (core.Platform, error) {
	name, _ := opts["name"].(string)
	if name == "" {
		name = "weibo"
	}
	appID, _ := opts["app_id"].(string)
	appSecret, _ := opts["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("weibo: app_id and app_secret are required")
	}

	tokenEndpoint, _ := opts["token_endpoint"].(string)
	if tokenEndpoint == "" {
		tokenEndpoint = defaultTokenEndpoint
	}
	wsEndpoint, _ := opts["ws_endpoint"].(string)
	if wsEndpoint == "" {
		wsEndpoint = defaultWSEndpoint
	}

	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom(name, allowFrom)

	return &Platform{
		name:          name,
		appID:         appID,
		appSecret:     appSecret,
		tokenEndpoint: tokenEndpoint,
		wsEndpoint:    wsEndpoint,
		allowFrom:     allowFrom,
		seen:          make(map[string]struct{}),
	}, nil
}

func (p *Platform) Name() string { return p.name }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	p.ctx, p.cancel = context.WithCancel(context.Background())

	if _, err := p.refreshToken(); err != nil {
		return fmt.Errorf("weibo: initial token fetch: %w", err)
	}
	slog.Info(p.tag()+": authenticated", "uid", p.uid)

	go p.connectLoop()
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	return p.sendMessage(rctx, content)
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.sendMessage(rctx, content)
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	if p.ws != nil {
		p.ws.Close()
	}
	return nil
}

// --- Token management ---

type tokenResponse struct {
	Data struct {
		Token    string          `json:"token"`
		ExpireIn int64           `json:"expire_in"` // seconds
		UID      json.RawMessage `json:"uid"`
	} `json:"data"`
	Error     string `json:"error"`
	ErrorCode int    `json:"error_code"`
}

func (p *Platform) refreshToken() (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.token != "" && time.Now().Before(p.tokenExpiry.Add(-tokenRenewBuf)) {
		return p.token, nil
	}

	body := fmt.Sprintf(`{"app_id":"%s","app_secret":"%s"}`, p.appID, p.appSecret)
	req, err := http.NewRequest("POST", p.tokenEndpoint, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("weibo: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("weibo: token request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("weibo: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("weibo: token HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("weibo: parse token response: %w", err)
	}
	if tr.Data.Token == "" {
		return "", fmt.Errorf("weibo: empty token in response: %s", string(raw))
	}

	p.token = tr.Data.Token
	p.tokenExpiry = time.Now().Add(time.Duration(tr.Data.ExpireIn) * time.Second)
	if len(tr.Data.UID) > 0 {
		p.uid = strings.Trim(string(tr.Data.UID), `"`)
	}

	slog.Debug(p.tag()+": token refreshed", "expires_in", tr.Data.ExpireIn)
	return p.token, nil
}

func (p *Platform) getToken() (string, error) {
	p.tokenMu.Lock()
	tok := p.token
	exp := p.tokenExpiry
	p.tokenMu.Unlock()

	if tok != "" && time.Now().Before(exp.Add(-tokenRenewBuf)) {
		return tok, nil
	}
	return p.refreshToken()
}

func (p *Platform) invalidateToken() {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	p.token = ""
	p.tokenExpiry = time.Time{}
}

// --- WebSocket connection ---

func (p *Platform) connectLoop() {
	delay := reconnectDelay
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		if err := p.connect(); err != nil {
			slog.Error(p.tag()+": connect failed", "error", err)
		}

		select {
		case <-p.ctx.Done():
			return
		case <-time.After(delay):
		}

		delay = min(delay*2, maxReconnect)
	}
}

func (p *Platform) connect() error {
	p.connMu.Lock()
	defer p.connMu.Unlock()

	tok, err := p.getToken()
	if err != nil {
		return err
	}

	u, err := url.Parse(p.wsEndpoint)
	if err != nil {
		return fmt.Errorf("weibo: parse ws endpoint: %w", err)
	}
	q := u.Query()
	q.Set("app_id", p.appID)
	q.Set("token", tok)
	u.RawQuery = q.Encode()

	ws, _, err := websocket.DefaultDialer.DialContext(p.ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("weibo: ws dial: %w", err)
	}

	p.wsMu.Lock()
	if p.ws != nil {
		p.ws.Close()
	}
	p.ws = ws
	p.wsMu.Unlock()

	slog.Info(p.tag() + ": websocket connected")

	go p.pingLoop(ws)
	p.readLoop(ws)
	return nil
}

func (p *Platform) pingLoop(ws *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.wsMu.Lock()
			if p.ws != ws {
				p.wsMu.Unlock()
				return
			}
			err := ws.WriteJSON(map[string]string{"type": "ping"})
			p.wsMu.Unlock()
			if err != nil {
				slog.Debug(p.tag()+": ping write error", "error", err)
				return
			}
		}
	}
}

type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type messagePayload struct {
	MessageID  string              `json:"messageId"`
	FromUserID string              `json:"fromUserId"`
	Text       string              `json:"text"`
	Timestamp  int64               `json:"timestamp"`
	Input      []messageInputItem  `json:"input,omitempty"`
}

type messageInputItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	Source   *inputSource `json:"source,omitempty"`
	FileName string       `json:"filename,omitempty"`
}

type inputSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

var supportedImageMIME = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

const (
	maxInboundImageBytes = 10 * 1024 * 1024
	maxInboundFileBytes  = 5 * 1024 * 1024
)

func (p *Platform) readLoop(ws *websocket.Conn) {
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})
	ws.SetReadDeadline(time.Now().Add(pongTimeout))

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			if websocket.IsCloseError(err, 4002) {
				slog.Warn(p.tag() + ": invalid token, clearing cache")
				p.invalidateToken()
			}
			slog.Debug(p.tag()+": ws read error", "error", err)
			return
		}

		ws.SetReadDeadline(time.Now().Add(pongTimeout))

		var msg wsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			slog.Debug(p.tag()+": ws unmarshal error", "error", err)
			continue
		}

		switch msg.Type {
		case "pong":
			// heartbeat response, already handled via deadline reset
		case "message":
			if msg.Payload != nil {
				p.handleInbound(msg.Payload)
			}
		default:
			slog.Debug(p.tag()+": unhandled ws message type", "type", msg.Type)
		}
	}
}

func (p *Platform) handleInbound(raw json.RawMessage) {
	var payload messagePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Error(p.tag()+": parse inbound payload", "error", err)
		return
	}

	text, images, files := normalizeInboundInput(payload)
	hasText := strings.TrimSpace(text) != ""
	hasAttachments := len(images) > 0 || len(files) > 0

	if payload.FromUserID == "" || (!hasText && !hasAttachments) {
		return
	}

	msgID := payload.MessageID
	if msgID == "" {
		msgID = fmt.Sprintf("%x", sha256.Sum256(raw))[:16]
	}

	if p.isDuplicate(msgID) {
		return
	}

	userID := payload.FromUserID
	if !core.AllowList(p.allowFrom, userID) {
		slog.Debug(p.tag()+": message from unauthorized user", "user", userID)
		return
	}

	sessionKey := p.name + ":" + userID + ":" + userID
	rctx := replyContext{
		fromUserID: userID,
		sessionKey: sessionKey,
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   p.name,
		MessageID:  msgID,
		UserID:     userID,
		UserName:   userID,
		Content:    text,
		Images:     images,
		Files:      files,
		ReplyCtx:   rctx,
	}

	if hasAttachments {
		slog.Debug(p.tag()+": inbound with attachments",
			"user", userID, "images", len(images), "files", len(files))
	}

	p.handler(p, msg)
}

func normalizeInboundInput(payload messagePayload) (string, []core.ImageAttachment, []core.FileAttachment) {
	var textParts []string
	var images []core.ImageAttachment
	var files []core.FileAttachment

	for _, item := range payload.Input {
		if item.Type != "message" || item.Role != "user" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "input_text":
				if part.Text != "" {
					textParts = append(textParts, part.Text)
				}
			case "input_image":
				if part.Source == nil || part.Source.Data == "" {
					continue
				}
				if !supportedImageMIME[part.Source.MediaType] {
					slog.Warn("weibo: unsupported inbound image mime", "mime", part.Source.MediaType)
					continue
				}
				data, err := base64.StdEncoding.DecodeString(part.Source.Data)
				if err != nil {
					slog.Warn("weibo: decode inbound image base64", "error", err)
					continue
				}
				if len(data) == 0 || len(data) > maxInboundImageBytes {
					continue
				}
				images = append(images, core.ImageAttachment{
					MimeType: part.Source.MediaType,
					Data:     data,
					FileName: part.FileName,
				})
			case "input_file":
				if part.Source == nil || part.Source.Data == "" {
					continue
				}
				data, err := base64.StdEncoding.DecodeString(part.Source.Data)
				if err != nil {
					slog.Warn("weibo: decode inbound file base64", "error", err)
					continue
				}
				if len(data) == 0 || len(data) > maxInboundFileBytes {
					continue
				}
				files = append(files, core.FileAttachment{
					MimeType: part.Source.MediaType,
					Data:     data,
					FileName: part.FileName,
				})
			}
		}
	}

	text := payload.Text
	if len(textParts) > 0 {
		text = strings.Join(textParts, "\n")
	}

	return text, images, files
}

// --- Sending ---

type sendPayload struct {
	ToUserID  string             `json:"toUserId"`
	Text      string             `json:"text"`
	MessageID string             `json:"messageId"`
	ChunkID   int                `json:"chunkId"`
	Done      bool               `json:"done"`
	Input     []messageInputItem `json:"input,omitempty"`
}

func (p *Platform) sendMessage(rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("weibo: invalid reply context type: %T", rctx)
	}

	chunks := splitText(content, maxTextPerChunk)
	msgID := fmt.Sprintf("out-%s-%d", rc.fromUserID, time.Now().UnixMilli())

	for i, chunk := range chunks {
		env := map[string]any{
			"type": "send_message",
			"payload": sendPayload{
				ToUserID:  rc.fromUserID,
				Text:      chunk,
				MessageID: msgID,
				ChunkID:   i,
				Done:      i == len(chunks)-1,
			},
		}
		if err := p.writeWS(env); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) SendImage(_ context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("weibo: invalid reply context type: %T", rctx)
	}

	b64 := base64.StdEncoding.EncodeToString(img.Data)
	mime := img.MimeType
	if mime == "" {
		mime = "image/png"
	}
	fname := img.FileName
	if fname == "" {
		fname = "image"
	}

	msgID := fmt.Sprintf("img-%s-%d", rc.fromUserID, time.Now().UnixMilli())
	env := map[string]any{
		"type": "send_message",
		"payload": sendPayload{
			ToUserID:  rc.fromUserID,
			MessageID: msgID,
			Done:      true,
			Input: []messageInputItem{{
				Type: "message",
				Role: "assistant",
				Content: []contentPart{{
					Type:     "input_image",
					FileName: fname,
					Source:   &inputSource{Type: "base64", MediaType: mime, Data: b64},
				}},
			}},
		},
	}
	slog.Debug(p.tag()+": sending image", "to", rc.fromUserID, "name", fname, "size", len(img.Data))
	return p.writeWS(env)
}

func (p *Platform) SendFile(_ context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("weibo: invalid reply context type: %T", rctx)
	}

	b64 := base64.StdEncoding.EncodeToString(file.Data)
	mime := file.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	fname := file.FileName
	if fname == "" {
		fname = "attachment"
	}

	msgID := fmt.Sprintf("file-%s-%d", rc.fromUserID, time.Now().UnixMilli())
	env := map[string]any{
		"type": "send_message",
		"payload": sendPayload{
			ToUserID:  rc.fromUserID,
			MessageID: msgID,
			Done:      true,
			Input: []messageInputItem{{
				Type: "message",
				Role: "assistant",
				Content: []contentPart{{
					Type:     "input_file",
					FileName: fname,
					Source:   &inputSource{Type: "base64", MediaType: mime, Data: b64},
				}},
			}},
		},
	}
	slog.Debug(p.tag()+": sending file", "to", rc.fromUserID, "name", fname, "size", len(file.Data))
	return p.writeWS(env)
}

func (p *Platform) writeWS(data any) error {
	p.wsMu.Lock()
	ws := p.ws
	p.wsMu.Unlock()
	if ws == nil {
		return fmt.Errorf("weibo: not connected")
	}
	if err := ws.WriteJSON(data); err != nil {
		return fmt.Errorf("weibo: ws send: %w", err)
	}
	return nil
}

// --- Helpers ---

func (p *Platform) tag() string { return p.name }

func (p *Platform) isDuplicate(msgID string) bool {
	p.seenMu.Lock()
	defer p.seenMu.Unlock()
	if _, ok := p.seen[msgID]; ok {
		return true
	}
	if len(p.seen) >= maxSeenMessages {
		// prune half
		i := 0
		for k := range p.seen {
			if i >= maxSeenMessages/2 {
				break
			}
			delete(p.seen, k)
			i++
		}
	}
	p.seen[msgID] = struct{}{}
	return false
}

func splitText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(runes) > 0 {
		end := limit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}
