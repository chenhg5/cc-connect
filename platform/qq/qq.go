package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
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
	httpURL               string   // OneBot HTTP API URL, e.g. "http://127.0.0.1:3000"
	recordDirs            []string // absolute directories local voice files may be read from
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

	recordDirs, err := parseRecordDirs(opts["record_dirs"])
	if err != nil {
		return nil, err
	}

	return &Platform{
		wsURL:                 wsURL,
		token:                 token,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
		httpURL:               httpURL,
		recordDirs:            recordDirs,
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

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	// Start readLoop BEFORE callAPI: callAPI's response is routed by readLoop,
	// so calling it first would always time out after 15s and leave selfID=0,
	// which disables the self-message filter in handleMessage and lets the bot
	// respond to its own messages.
	go p.readLoop(ctx)

	// Get bot self info
	if info, err := p.callAPI("get_login_info", nil); err == nil {
		if uid, ok := info["user_id"].(float64); ok {
			p.selfID = int64(uid)
		}
		nick, _ := info["nickname"].(string)
		slog.Info("qq: logged in", "qq", p.selfID, "nickname", nick)
	} else {
		slog.Warn("qq: get_login_info failed; self-message filter disabled until next reconnect", "error", err)
	}

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
					size, err := recordFileSize(data["file_size"])
					if err != nil {
						slog.Warn("qq: read audio failed", "error", err)
						continue
					}
					audioData, err := p.readRecord(url, size)
					if err != nil {
						slog.Warn("qq: read audio failed", "error", err)
						continue
					}
					audio = &core.AudioAttachment{
						Data:   audioData,
						Format: recordFormat(audioData),
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

const (
	// maxRecordBytes caps a voice file, whether read from disk or over HTTP.
	maxRecordBytes = 20 << 20

	// recordWaitTimeout bounds how long readRecord waits for QQ to finish
	// writing a local voice file.
	recordWaitTimeout  = 10 * time.Second
	recordPollInterval = 100 * time.Millisecond
)

// parseRecordDirs reads the record_dirs option: a directory or a list of
// directories that local voice files may be read from. A leading ~ is
// expanded to the home directory.
func parseRecordDirs(v any) ([]string, error) {
	var raw []string
	switch v := v.(type) {
	case nil:
	case string:
		if v != "" {
			raw = []string{v}
		}
	case []string:
		raw = v
	case []any:
		for _, d := range v {
			s, ok := d.(string)
			if !ok {
				return nil, fmt.Errorf("qq: record_dirs: entry %v is not a string", d)
			}
			raw = append(raw, s)
		}
	default:
		return nil, fmt.Errorf("qq: record_dirs must be a string or a list of strings")
	}

	dirs := make([]string, 0, len(raw))
	for _, d := range raw {
		if d == "~" || strings.HasPrefix(d, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("qq: record_dirs %q: %w", d, err)
			}
			d = filepath.Join(home, d[1:])
		}
		if !filepath.IsAbs(d) {
			return nil, fmt.Errorf("qq: record_dirs %q is not an absolute path", d)
		}
		dirs = append(dirs, filepath.Clean(d))
	}
	return dirs, nil
}

// recordFileSize parses a record segment's file_size. A missing value
// returns 0; a present value must be a positive integer.
func recordFileSize(v any) (int64, error) {
	var n int64
	switch v := v.(type) {
	case nil:
		return 0, nil
	case float64:
		if v != math.Trunc(v) || v > math.MaxInt64 {
			return 0, fmt.Errorf("invalid voice file_size %v", v)
		}
		n = int64(v)
	case string:
		var err error
		if n, err = strconv.ParseInt(v, 10, 64); err != nil {
			return 0, fmt.Errorf("invalid voice file_size %q", v)
		}
	default:
		return 0, fmt.Errorf("invalid voice file_size %v", v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid voice file_size %d", n)
	}
	return n, nil
}

// readRecord returns the bytes of a voice segment. size is the segment's
// file_size, 0 if missing.
//
// A NapCat instance on the same host puts a local file path in "url" instead
// of an HTTP URL. Such paths are only read inside record_dirs, and need a
// file_size because NapCat pushes the event before QQ has finished writing
// the file.
func (p *Platform) readRecord(src string, size int64) ([]byte, error) {
	if size > maxRecordBytes {
		return nil, fmt.Errorf("voice file is %d bytes, limit is %d", size, maxRecordBytes)
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return downloadRecord(src)
	}
	if size <= 0 {
		return nil, fmt.Errorf("local voice file %s has no file_size", src)
	}
	return readLocalRecord(src, size, p.recordDirs, recordWaitTimeout)
}

// readLocalRecord reads a voice file inside one of roots, waiting up to
// timeout for it to exist and reach size bytes.
func readLocalRecord(path string, size int64, roots []string, timeout time.Duration) ([]byte, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("local voice file %s: set record_dirs to allow reading local voice files", path)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("voice file path %q is not absolute", path)
	}
	path = filepath.Clean(path)
	// Reject paths outside record_dirs without waiting for them to appear.
	// readRecordFile checks again after resolving symlinks.
	if !lexicallyInside(path, roots) {
		return nil, fmt.Errorf("voice file %s is outside record_dirs", path)
	}

	deadline := time.Now().Add(timeout)
	for {
		data, err := readRecordFile(path, roots)
		if err == nil && int64(len(data)) >= size {
			return data, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("incomplete voice file %s: %d of %d bytes", path, len(data), size)
			}
			return nil, err
		}
		time.Sleep(recordPollInterval)
	}
}

// lexicallyInside reports whether path is inside one of roots, comparing
// against each root both as configured and with symlinks resolved.
func lexicallyInside(path string, roots []string) bool {
	for _, root := range roots {
		if _, ok := relInside(root, path); ok {
			return true
		}
		if real, err := filepath.EvalSymlinks(root); err == nil {
			if _, ok := relInside(real, path); ok {
				return true
			}
		}
	}
	return false
}

// relInside returns path relative to root if path is strictly inside root.
func relInside(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// readRecordFile resolves symlinks in path, checks that the target is a
// regular file inside one of roots and reads at most maxRecordBytes of it.
// The file is opened through os.Root, so a symlink swapped in after the check
// still cannot lead outside the root.
func readRecordFile(path string, roots []string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, ok := relInside(realRoot, real)
		if !ok {
			continue
		}
		r, err := os.OpenRoot(realRoot)
		if err != nil {
			return nil, err
		}
		defer func() { _ = r.Close() }()
		// Lstat before Open: opening a FIFO would block.
		info, err := r.Lstat(rel)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("voice file %s is not a regular file", path)
		}
		f, err := r.Open(rel)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		if info, err = f.Stat(); err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("voice file %s is not a regular file", path)
		}
		return readRecordLimited(f)
	}
	return nil, fmt.Errorf("voice file %s is outside record_dirs", path)
}

// readRecordLimited reads r and fails if it holds more than maxRecordBytes.
func readRecordLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordBytes {
		return nil, fmt.Errorf("voice file exceeds %d bytes", maxRecordBytes)
	}
	return data, nil
}

func downloadRecord(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download voice: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxRecordBytes {
		return nil, fmt.Errorf("voice file is %d bytes, limit is %d", resp.ContentLength, maxRecordBytes)
	}
	return readRecordLimited(resp.Body)
}

// recordFormat detects the voice codec from the file header. QQ NT saves SILK
// voice under a ".amr" file name, so the name cannot be trusted. QQ voice is
// SILK unless the header says AMR or MP3; anything else is passed on as SILK
// and rejected by the SILK decoder if it is not.
func recordFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("#!AMR")):
		return "amr"
	case bytes.HasPrefix(data, []byte("ID3")), len(data) > 1 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "mp3"
	default:
		return "silk"
	}
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
