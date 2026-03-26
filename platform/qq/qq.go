package qq

import (
	"bytes"
	"context"
	"encoding/base64"
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
	wsURL                 string // e.g. "ws://127.0.0.1:3001"
	token                 string // optional access_token
	allowFrom             string // comma-separated user IDs or "*"
	shareSessionInChannel bool
	handler               core.MessageHandler
	conn                  *websocket.Conn
	mu                    sync.Mutex
	echoSeq               atomic.Int64
	echoCh                sync.Map // echo -> chan json.RawMessage
	cancel                context.CancelFunc
	selfID                int64
	dedup                 core.MessageDedup
	groupNameCache        sync.Map // groupID -> group name
	httpURL            string   // OneBot HTTP API URL, e.g. "http://127.0.0.1:3000"
}

func New(opts map[string]any) (core.Platform, error) {
	wsURL, _ := opts["ws_url"].(string)
	if wsURL == "" {
		wsURL = "ws://127.0.0.1:3001"
	}
	token, _ := opts["token"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)

	core.CheckAllowFrom("qq", allowFrom)

	httpURL, _ := opts["http_url"].(string)
	httpURL = strings.TrimRight(httpURL, "/")

	return &Platform{
		wsURL:                 wsURL,
		token:                 token,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
		httpURL:            httpURL,
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

	if ts, ok := payload["time"].(float64); ok && ts > 0 {
		if core.IsOldMessage(time.Unix(int64(ts), 0)) {
			slog.Debug("qq: ignoring old message after restart", "time", int64(ts))
			return
		}
	}

	msgIDStr := strconv.FormatInt(messageID, 10)
	if p.dedup.IsDuplicate(msgIDStr) {
		slog.Debug("qq: duplicate message ignored", "message_id", messageID)
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
	text, images, files, audio := p.parseMessage(payload, msgType, groupID)
	if text == "" && len(images) == 0 && len(files) == 0 && audio == nil {
		return
	}

	var sessionKey string
	if msgType == "group" {
		if p.shareSessionInChannel {
			sessionKey = fmt.Sprintf("qq:g:%d", groupID)
		} else {
			sessionKey = fmt.Sprintf("qq:%d:%d", groupID, userID)
		}
	} else {
		sessionKey = fmt.Sprintf("qq:%d", userID)
	}

	rctx := &replyContext{
		messageType: msgType,
		userID:      userID,
		groupID:     groupID,
		messageID:   int32(messageID),
	}

	var chatName string
	if msgType == "group" {
		chatName = p.resolveGroupName(groupID)
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "qq",
		MessageID:  strconv.FormatInt(messageID, 10),
		UserID:     strconv.FormatInt(userID, 10),
		UserName:   userName,
		ChatName:   chatName,
		Content:    text,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ReplyCtx:   rctx,
	}

	slog.Debug("qq: message received", "type", msgType, "user", userID, "text_len", len(text))
	p.handler(p, msg)
}

func (p *Platform) parseMessage(payload map[string]any, msgType string, groupID int64) (string, []core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment) {
	var textParts []string
	var images []core.ImageAttachment
	var files []core.FileAttachment
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
			case "file":
				name, _ := data["name"].(string)
				if name == "" {
					name, _ = data["file"].(string)
				}
				fileID, _ := data["file_id"].(string)
				if fileID == "" {
					fileID, _ = data["file"].(string)
				}

				slog.Info("qq: file segment received", "name", name, "file_id", fileID,
					"has_url", data["url"] != nil, "msg_type", msgType, "group_id", groupID)

				var downloaded bool

				// Step 1: Try direct URL from message segment (with longer timeout for large files)
				if url, ok := data["url"].(string); ok && url != "" {
					fileData, mime, err := downloadLargeFile(url)
					if err != nil {
						slog.Warn("qq: [step1] download file via segment URL failed", "error", err)
					} else {
						files = append(files, core.FileAttachment{
							MimeType: mime,
							Data:     fileData,
							FileName: name,
						})
						downloaded = true
						slog.Info("qq: [step1] file downloaded via segment URL", "name", name, "size", len(fileData))
					}
				}

				// Step 2: Get fresh direct link via NapCat API (CDN URLs expire / have download limits)
				// NapCat docs: params are STRINGS — group (not group_id), file_id
				if !downloaded && p.httpURL != "" && fileID != "" {
					var freshURL string
					if msgType == "group" && groupID != 0 {
						groupStr := strconv.FormatInt(groupID, 10)
						slog.Info("qq: [step2] trying get_group_file_url", "file_id", fileID, "group", groupStr)
						result, err := p.callHTTPAPI("get_group_file_url", map[string]any{
							"file_id": fileID,
							"group":   groupStr,
						})
						if err == nil {
							freshURL, _ = result["url"].(string)
							slog.Info("qq: [step2] get_group_file_url returned", "has_url", freshURL != "")
						} else {
							slog.Warn("qq: [step2] get_group_file_url failed", "file_id", fileID, "error", err)
						}
					} else {
						slog.Info("qq: [step2] trying get_private_file_url", "file_id", fileID)
						result, err := p.callHTTPAPI("get_private_file_url", map[string]any{
							"file_id": fileID,
						})
						if err == nil {
							freshURL, _ = result["url"].(string)
						} else {
							slog.Warn("qq: [step2] get_private_file_url failed", "file_id", fileID, "error", err)
						}
					}
					if freshURL != "" {
						fileData, mime, err := downloadLargeFile(freshURL)
						if err == nil {
							files = append(files, core.FileAttachment{
								MimeType: mime,
								Data:     fileData,
								FileName: name,
							})
							downloaded = true
							slog.Info("qq: [step2] file downloaded via fresh URL", "name", name, "size", len(fileData))
						} else {
							slog.Warn("qq: [step2] download file via fresh URL failed", "error", err)
						}
					}
				}

				// Step 3: Last resort — get_file (downloads to NapCat local or returns base64)
				if !downloaded && p.httpURL != "" && fileID != "" {
					slog.Info("qq: [step3] trying get_file", "file_id", fileID)
					result, err := p.callHTTPAPI("get_file", map[string]any{"file_id": fileID})
					if err == nil {
						if fileURL, ok := result["url"].(string); ok && fileURL != "" {
							fileData, mime, err := downloadLargeFile(fileURL)
							if err == nil {
								files = append(files, core.FileAttachment{
									MimeType: mime,
									Data:     fileData,
									FileName: name,
								})
								downloaded = true
							}
						}
						if !downloaded {
							if b64Str, ok := result["base64"].(string); ok && b64Str != "" {
								if decoded, err := base64.StdEncoding.DecodeString(b64Str); err == nil {
									files = append(files, core.FileAttachment{
										MimeType: http.DetectContentType(decoded),
										Data:     decoded,
										FileName: name,
									})
									downloaded = true
								}
							}
						}
					} else {
						slog.Warn("qq: get_file API failed", "file_id", fileID, "error", err)
					}
				}

				if !downloaded {
					slog.Warn("qq: file segment could not be downloaded", "name", name)
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

	return strings.TrimSpace(strings.Join(textParts, "")), images, files, audio
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

// SendImage sends an image to the conversation.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rctx, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("qq: SendImage: invalid reply context type %T", replyCtx)
	}

	b64 := base64.StdEncoding.EncodeToString(img.Data)
	segments := []map[string]any{
		{"type": "image", "data": map[string]any{"file": "base64://" + b64}},
	}

	params := map[string]any{
		"message": segments,
	}

	if rctx.messageType == "group" {
		params["group_id"] = rctx.groupID
		_, err := p.callAPI("send_group_msg", params)
		if err != nil {
			return fmt.Errorf("qq: send image: %w", err)
		}
		return nil
	}

	params["user_id"] = rctx.userID
	_, err := p.callAPI("send_private_msg", params)
	if err != nil {
		return fmt.Errorf("qq: send image: %w", err)
	}
	return nil
}

var _ core.ImageSender = (*Platform)(nil)

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

func (p *Platform) resolveGroupName(groupID int64) string {
	if groupID == 0 {
		return ""
	}
	fallback := strconv.FormatInt(groupID, 10)
	if cached, ok := p.groupNameCache.Load(fallback); ok {
		return cached.(string)
	}
	result, err := p.callAPI("get_group_info", map[string]any{"group_id": groupID})
	if err != nil {
		slog.Debug("qq: resolve group name failed", "group_id", groupID, "error", err)
		return fallback
	}
	name, _ := result["group_name"].(string)
	if name != "" {
		p.groupNameCache.Store(fallback, name)
		return name
	}
	return fallback
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
		_ = json.Unmarshal(resp.Data, &result)
		return result, nil

	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("qq: API %s timeout", action)
	}
}

// callHTTPAPI calls a OneBot v11 HTTP endpoint (e.g. /upload_group_file).
// Used for file operations — avoids WebSocket message size limits and
// file-path issues across Windows/WSL/Docker boundaries.
// Requires http_url to be configured.
func (p *Platform) callHTTPAPI(action string, params map[string]any) (map[string]any, error) {
	if p.httpURL == "" {
		return nil, fmt.Errorf("qq: http_url not configured")
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	url := p.httpURL + "/" + action
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qq: HTTP %s failed: %w", action, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qq: HTTP %s read body: %w", action, err)
	}

	var apiResp struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &apiResp) != nil {
		return nil, fmt.Errorf("qq: HTTP %s invalid response", action)
	}
	if apiResp.RetCode != 0 {
		return nil, fmt.Errorf("qq: HTTP %s failed (retcode=%d, msg=%s)", action, apiResp.RetCode, apiResp.Message)
	}
	var result map[string]any
	_ = json.Unmarshal(apiResp.Data, &result)
	return result, nil
}

// ── Helpers ─────────────────────────────────────────────────────

type replyContext struct {
	messageType string // "private" or "group"
	userID      int64
	groupID     int64
	messageID   int32
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// qq:{userID}, qq:{groupID}:{userID} or qq:g:{groupID}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "qq" {
		return nil, fmt.Errorf("qq: invalid session key %q", sessionKey)
	}
	if len(parts) == 3 {
		if parts[1] == "g" {
			gid, _ := strconv.ParseInt(parts[2], 10, 64)
			return &replyContext{messageType: "group", groupID: gid}, nil
		}
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

func downloadLargeFile(url string) ([]byte, string, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

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

// SendFile sends a file to the conversation.
// Implements core.FileSender.
//
// Uses base64-encoded file data to avoid file-path issues across
// Windows/WSL/Docker. Routes through NapCat HTTP API when configured
// (better for large files), falls back to WebSocket.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rctx, ok := replyCtx.(*replyContext)
	if !ok {
		return fmt.Errorf("qq: SendFile: invalid reply context type %T", replyCtx)
	}

	name := file.FileName
	if name == "" {
		name = "attachment"
	}

	b64data := "base64://" + base64.StdEncoding.EncodeToString(file.Data)

	// Pick API caller: prefer HTTP for large payloads, fall back to WebSocket.
	call := p.callAPI
	if p.httpURL != "" {
		call = p.callHTTPAPI
	}

	if rctx.messageType == "group" {
		_, err := call("upload_group_file", map[string]any{
			"group_id": rctx.groupID,
			"file":     b64data,
			"name":     name,
		})
		if err != nil {
			return fmt.Errorf("qq: SendFile group: %w", err)
		}
		return nil
	}

	// Private: use send_private_msg with file segment
	_, err := call("send_private_msg", map[string]any{
		"user_id": rctx.userID,
		"message": []map[string]any{
			{"type": "file", "data": map[string]any{"file": b64data, "name": name}},
		},
	})
	if err != nil {
		return fmt.Errorf("qq: SendFile private: %w", err)
	}
	return nil
}

var _ core.FileSender = (*Platform)(nil)
