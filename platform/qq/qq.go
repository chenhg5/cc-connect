package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func init() {
	core.RegisterPlatform("qq", New)
}

// Platform connects to a OneBot v11 implementation (NapCat, LLOneBot, etc.)
// via forward WebSocket. It receives message events and sends messages back
// through the same WS connection.
type Platform struct {
	wsURL     string // e.g. "ws://127.0.0.1:3001"
	token     string // optional access_token
	allowFrom string // comma-separated user IDs or "*"
	handler   core.MessageHandler
	conn      *websocket.Conn
	mu        sync.Mutex
	echoSeq   atomic.Int64
	echoCh    sync.Map // echo -> chan json.RawMessage
	cancel    context.CancelFunc
	selfID    int64
}

func New(opts map[string]any) (core.Platform, error) {
	wsURL, _ := opts["ws_url"].(string)
	if wsURL == "" {
		wsURL = "ws://127.0.0.1:3001"
	}
	token, _ := opts["token"].(string)
	allowFrom, _ := opts["allow_from"].(string)

	return &Platform{
		wsURL:     wsURL,
		token:     token,
		allowFrom: allowFrom,
	}, nil
}

func (p *Platform) Name() string { return "qq" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	header := http.Header{}
	if p.token != "" {
		header.Set("Authorization", "Bearer "+p.token)
	}

	conn, _, err := websocket.DefaultDialer.Dial(p.wsURL, header)
	if err != nil {
		return fmt.Errorf("qq: ws connect failed (%s): %w", p.wsURL, err)
	}
	p.conn = conn

	slog.Info("qq: connected to OneBot", "url", p.wsURL)

	// Get bot self info
	if info, err := p.callAPI("get_login_info", nil); err == nil {
		if uid, ok := info["user_id"].(float64); ok {
			p.selfID = int64(uid)
		}
		nick, _ := info["nickname"].(string)
		slog.Info("qq: logged in", "qq", p.selfID, "nickname", nick)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go p.readLoop(ctx)

	return nil
}

func (p *Platform) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, raw, err := p.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("qq: ws read error, reconnecting...", "error", err)
			p.reconnect()
			continue
		}

		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil {
			continue
		}

		// If this is an API response (has "echo" field), route to caller
		if echo, ok := payload["echo"].(string); ok {
			if ch, loaded := p.echoCh.LoadAndDelete(echo); loaded {
				if dataCh, ok := ch.(chan json.RawMessage); ok {
					dataCh <- raw
				}
			}
			continue
		}

		// Otherwise it's an event
		postType, _ := payload["post_type"].(string)
		if postType == "message" {
			p.handleMessage(payload)
		}
	}
}

func (p *Platform) reconnect() {
	for i := 1; i <= 30; i++ {
		time.Sleep(time.Duration(i) * 2 * time.Second)
		header := http.Header{}
		if p.token != "" {
			header.Set("Authorization", "Bearer "+p.token)
		}
		conn, _, err := websocket.DefaultDialer.Dial(p.wsURL, header)
		if err != nil {
			slog.Warn("qq: reconnect attempt failed", "attempt", i, "error", err)
			continue
		}
		p.mu.Lock()
		p.conn = conn
		p.mu.Unlock()
		slog.Info("qq: reconnected")
		return
	}
	slog.Error("qq: failed to reconnect after 30 attempts")
}

func (p *Platform) handleMessage(payload map[string]any) {
	msgType, _ := payload["message_type"].(string)
	userID := jsonInt64(payload, "user_id")
	groupID := jsonInt64(payload, "group_id")
	messageID := jsonInt64(payload, "message_id")

	if userID == p.selfID {
		return
	}

	if !p.isAllowed(userID) {
		return
	}

	// Extract sender info
	var userName string
	if sender, ok := payload["sender"].(map[string]any); ok {
		card, _ := sender["card"].(string)
		nick, _ := sender["nickname"].(string)
		if card != "" {
			userName = card
		} else {
			userName = nick
		}
	}

	// Parse message content from CQ message array or raw_message
	text, images, audio := p.parseMessage(payload)
	if text == "" && len(images) == 0 && audio == nil {
		return
	}

	var sessionKey string
	if msgType == "group" {
		sessionKey = fmt.Sprintf("qq:%d:%d", groupID, userID)
	} else {
		sessionKey = fmt.Sprintf("qq:%d", userID)
	}

	rctx := &replyContext{
		messageType: msgType,
		userID:      userID,
		groupID:     groupID,
		messageID:   int32(messageID),
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "qq",
		UserID:     strconv.FormatInt(userID, 10),
		UserName:   userName,
		Content:    text,
		Images:     images,
		Audio:      audio,
		ReplyCtx:   rctx,
	}

	slog.Debug("qq: message received", "type", msgType, "user", userID, "text_len", len(text))
	p.handler(p, msg)
}

func (p *Platform) parseMessage(payload map[string]any) (string, []core.ImageAttachment, *core.AudioAttachment) {
	var textParts []string
	var images []core.ImageAttachment
	var audio *core.AudioAttachment

	// OneBot message can be array of segments or a string
	switch msg := payload["message"].(type) {
	case []any:
		for _, seg := range msg {
			s, ok := seg.(map[string]any)
			if !ok {
				continue
			}
			segType, _ := s["type"].(string)
			data, _ := s["data"].(map[string]any)
			if data == nil {
				continue
			}

			switch segType {
			case "text":
				if text, ok := data["text"].(string); ok {
					textParts = append(textParts, text)
				}
			case "image":
				if url, ok := data["url"].(string); ok && url != "" {
					imgData, mime, err := downloadFile(url)
					if err != nil {
						slog.Warn("qq: download image failed", "error", err)
						continue
					}
					images = append(images, core.ImageAttachment{
						MimeType: mime,
						Data:     imgData,
					})
				}
			case "record":
				if url, ok := data["url"].(string); ok && url != "" {
					audioData, _, err := downloadFile(url)
					if err != nil {
						slog.Warn("qq: download audio failed", "error", err)
						continue
					}
					format := "silk"
					if f, ok := data["file"].(string); ok {
						if strings.HasSuffix(f, ".amr") {
							format = "amr"
						} else if strings.HasSuffix(f, ".mp3") {
							format = "mp3"
						}
					}
					audio = &core.AudioAttachment{
						Data:   audioData,
						Format: format,
					}
				}
			case "at":
				// Ignore @mentions in parsed text
			}
		}
	default:
		// raw_message fallback (string with CQ codes)
		if raw, ok := payload["raw_message"].(string); ok {
			textParts = append(textParts, stripCQCodes(raw))
		}
	}

	return strings.TrimSpace(strings.Join(textParts, "")), images, audio
}

// Reply sends a message as a reply to an incoming message.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.Send(ctx, replyCtx, content)
}

// Send sends a message to the conversation identified by replyCtx.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rctx, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("qq: invalid reply context")
	}

	params := map[string]any{
		"message": content,
	}

	if rctx.messageType == "group" {
		params["group_id"] = rctx.groupID
		_, err := p.callAPI("send_group_msg", params)
		return err
	}

	params["user_id"] = rctx.userID
	_, err := p.callAPI("send_private_msg", params)
	return err
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// ── OneBot API call via WebSocket ───────────────────────────────

func (p *Platform) callAPI(action string, params map[string]any) (map[string]any, error) {
	seq := p.echoSeq.Add(1)
	echo := strconv.FormatInt(seq, 10)

	req := map[string]any{
		"action": action,
		"echo":   echo,
	}
	if params != nil {
		req["params"] = params
	}

	ch := make(chan json.RawMessage, 1)
	p.echoCh.Store(echo, ch)
	defer p.echoCh.Delete(echo)

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	err = p.conn.WriteMessage(websocket.TextMessage, data)
	p.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("qq: ws write: %w", err)
	}

	select {
	case raw := <-ch:
		var resp struct {
			Status  string          `json:"status"`
			RetCode int             `json:"retcode"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &resp) != nil {
			return nil, fmt.Errorf("qq: invalid API response")
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("qq: API %s failed (retcode=%d)", action, resp.RetCode)
		}
		var result map[string]any
		json.Unmarshal(resp.Data, &result)
		return result, nil

	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("qq: API %s timeout", action)
	}
}

// ── Helpers ─────────────────────────────────────────────────────

type replyContext struct {
	messageType string // "private" or "group"
	userID      int64
	groupID     int64
	messageID   int32
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// qq:{userID} or qq:{groupID}:{userID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "qq" {
		return nil, fmt.Errorf("qq: invalid session key %q", sessionKey)
	}
	if len(parts) == 3 {
		gid, _ := strconv.ParseInt(parts[1], 10, 64)
		uid, _ := strconv.ParseInt(parts[2], 10, 64)
		return &replyContext{messageType: "group", groupID: gid, userID: uid}, nil
	}
	uid, _ := strconv.ParseInt(parts[1], 10, 64)
	return &replyContext{messageType: "private", userID: uid}, nil
}

func (p *Platform) isAllowed(userID int64) bool {
	if p.allowFrom == "" || p.allowFrom == "*" {
		return true
	}
	uid := strconv.FormatInt(userID, 10)
	for _, allowed := range strings.Split(p.allowFrom, ",") {
		if strings.TrimSpace(allowed) == uid {
			return true
		}
	}
	return false
}

func jsonInt64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func stripCQCodes(s string) string {
	var result strings.Builder
	for len(s) > 0 {
		idx := strings.Index(s, "[CQ:")
		if idx < 0 {
			result.WriteString(s)
			break
		}
		result.WriteString(s[:idx])
		end := strings.Index(s[idx:], "]")
		if end < 0 {
			break
		}
		s = s[idx+end+1:]
	}
	return result.String()
}

func downloadFile(url string) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}
