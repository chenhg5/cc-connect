package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("wecom", New)
}

// Incoming XML envelope from WeChat Work callback.
type xmlEncryptedMsg struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	AgentID    string   `xml:"AgentID"`
	Encrypt    string   `xml:"Encrypt"`
}

// Decrypted message body.
type xmlMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	PicUrl       string   `xml:"PicUrl"`
	MediaId      string   `xml:"MediaId"`
	FileName     string   `xml:"FileName"` // inbound file messages (MsgType=file)
	Format       string   `xml:"Format"`   // voice format: amr, speex, etc.
	MsgId        int64    `xml:"MsgId"`
	AgentID      int64    `xml:"AgentID"`
}

type replyContext struct {
	userID string
}

type tokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

type Platform struct {
	corpID         string
	corpSecret     string
	agentID        string
	apiBaseURL     string
	allowFrom      string
	token          string // callback verification token
	aesKey         []byte // decoded EncodingAESKey (32 bytes)
	port           string
	callbackPath   string
	enableMarkdown bool
	server         *http.Server
	handler        core.MessageHandler
	apiClient      *http.Client // HTTP client for outbound API calls (may use proxy)
	tokenCache     tokenCache
	dedup          msgDedup
	userNameCache  sync.Map // userID -> display name
}

const defaultAPIBaseURL = "https://qyapi.weixin.qq.com"

// msgDedup tracks recently processed MsgIds to avoid WeChat Work retry duplicates.
type msgDedup struct {
	mu   sync.Mutex
	seen map[int64]time.Time
}

func (d *msgDedup) isDuplicate(msgID int64) bool {
	if msgID == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[int64]time.Time)
	}
	// Evict old entries (older than 60s)
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) > 60*time.Second {
			delete(d.seen, k)
		}
	}
	if _, exists := d.seen[msgID]; exists {
		return true
	}
	d.seen[msgID] = now
	return false
}

func New(opts map[string]any) (core.Platform, error) {
	mode, _ := opts["mode"].(string)
	if mode == "websocket" {
		return newWebSocket(opts)
	}

	corpID, _ := opts["corp_id"].(string)
	corpSecret, _ := opts["corp_secret"].(string)
	agentID, _ := opts["agent_id"].(string)
	callbackToken, _ := opts["callback_token"].(string)
	callbackAESKey, _ := opts["callback_aes_key"].(string)

	if corpID == "" || corpSecret == "" || agentID == "" {
		return nil, fmt.Errorf("wecom: corp_id, corp_secret, and agent_id are required")
	}
	if callbackToken == "" || callbackAESKey == "" {
		return nil, fmt.Errorf("wecom: callback_token and callback_aes_key are required")
	}

	aesKey, err := decodeAESKey(callbackAESKey)
	if err != nil {
		return nil, fmt.Errorf("wecom: invalid callback_aes_key: %w", err)
	}

	port, _ := opts["port"].(string)
	if port == "" {
		port = "8081"
	}
	path, _ := opts["callback_path"].(string)
	if path == "" {
		path = "/wecom/callback"
	}
	apiBaseURL, _ := opts["api_base_url"].(string)
	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	} else {
		parsed, err := url.Parse(apiBaseURL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, fmt.Errorf("wecom: invalid api_base_url %q: must be a valid http(s) URL", apiBaseURL)
		}
	}

	transport := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     10 * time.Second,
	}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("wecom: invalid proxy URL %q: %w", proxyURL, err)
		}
		proxyUser, _ := opts["proxy_username"].(string)
		proxyPass, _ := opts["proxy_password"].(string)
		if proxyUser != "" {
			u.User = url.UserPassword(proxyUser, proxyPass)
		}
		transport.Proxy = http.ProxyURL(u)
		transport.DisableKeepAlives = true // prevent CONNECT tunnel accumulation on proxy
		slog.Info("wecom: outbound API requests will use proxy (keep-alive disabled)", "proxy", u.Host, "auth", proxyUser != "")
	}
	apiClient := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	enableMarkdown, _ := opts["enable_markdown"].(bool)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("wecom", allowFrom)

	return &Platform{
		corpID:         corpID,
		corpSecret:     corpSecret,
		agentID:        agentID,
		apiBaseURL:     apiBaseURL,
		allowFrom:      allowFrom,
		token:          callbackToken,
		aesKey:         aesKey,
		port:           port,
		callbackPath:   path,
		enableMarkdown: enableMarkdown,
		apiClient:      apiClient,
	}, nil
}

func (p *Platform) Name() string { return "wecom" }

func (p *Platform) wecomAPIURL(path string, query url.Values) string {
	base := strings.TrimRight(strings.TrimSpace(p.apiBaseURL), "/")
	if base == "" {
		base = defaultAPIBaseURL
	}
	u := base + path
	if len(query) == 0 {
		return u
	}
	return u + "?" + query.Encode()
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	mux := http.NewServeMux()
	mux.HandleFunc(p.callbackPath, p.callbackHandler)

	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: mux,
	}

	go func() {
		slog.Info("wecom: webhook server listening", "port", p.port, "path", p.callbackPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("wecom: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) callbackHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	msgSignature := q.Get("msg_signature")
	timestamp := q.Get("timestamp")
	nonce := q.Get("nonce")

	if r.Method == http.MethodGet {
		p.handleVerify(w, msgSignature, timestamp, nonce, q.Get("echostr"))
		return
	}

	if r.Method == http.MethodPost {
		p.handleMessage(w, r, msgSignature, timestamp, nonce)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
}

// handleVerify handles the one-time URL verification from WeChat Work.
func (p *Platform) handleVerify(w http.ResponseWriter, msgSig, timestamp, nonce, echostr string) {
	if !p.verifySignature(msgSig, timestamp, nonce, echostr) {
		slog.Warn("wecom: verify signature failed")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	plain, err := p.decrypt(echostr)
	if err != nil {
		slog.Error("wecom: decrypt echostr failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Info("wecom: URL verification succeeded")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, plain)
}

// wecomLogXMLPreview returns a short prefix of XML for debug only (may contain user content).
func wecomLogXMLPreview(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// handleMessage processes incoming encrypted message POSTs.
func (p *Platform) handleMessage(w http.ResponseWriter, r *http.Request, msgSig, timestamp, nonce string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		slog.Warn("wecom: read callback body failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	slog.Info("wecom: callback POST received",
		"body_bytes", len(body),
		"content_length", r.ContentLength,
		"has_msg_signature", msgSig != "",
		"has_timestamp", timestamp != "",
		"has_nonce", nonce != "")
	if len(body) == 0 {
		slog.Warn("wecom: empty callback POST body")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var encMsg xmlEncryptedMsg
	if err := xml.Unmarshal(body, &encMsg); err != nil {
		slog.Error("wecom: parse outer xml failed", "error", err, "body_bytes", len(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !p.verifySignature(msgSig, timestamp, nonce, encMsg.Encrypt) {
		slog.Warn("wecom: message signature verification failed")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	plainXML, err := p.decrypt(encMsg.Encrypt)
	if err != nil {
		slog.Error("wecom: decrypt message failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Debug("wecom: decrypted xml preview", "preview", wecomLogXMLPreview(plainXML, 512))

	// Return 200 immediately (WeChat Work requires response within 5 seconds)
	w.WriteHeader(http.StatusOK)

	var msg xmlMessage
	if err := xml.Unmarshal([]byte(plainXML), &msg); err != nil {
		slog.Error("wecom: parse decrypted xml failed", "error", err,
			"plain_len", len(plainXML),
			"preview", wecomLogXMLPreview(plainXML, 256))
		return
	}

	slog.Info("wecom: inbound parsed",
		"msg_type", msg.MsgType,
		"msg_id", msg.MsgId,
		"from_user", msg.FromUserName,
		"create_time", msg.CreateTime,
		"has_media_id", msg.MediaId != "",
		"file_name", msg.FileName)

	if p.dedup.isDuplicate(msg.MsgId) {
		slog.Info("wecom: dropping duplicate message", "msg_id", msg.MsgId, "msg_type", msg.MsgType)
		return
	}

	if msg.CreateTime > 0 {
		if core.IsOldMessage(time.Unix(msg.CreateTime, 0)) {
			slog.Info("wecom: ignoring old message after restart", "create_time", msg.CreateTime, "msg_type", msg.MsgType)
			return
		}
	}

	if !core.AllowList(p.allowFrom, msg.FromUserName) {
		slog.Warn("wecom: message rejected by allow_from", "user", msg.FromUserName, "msg_type", msg.MsgType)
		return
	}

	sessionKey := fmt.Sprintf("wecom:%s", msg.FromUserName)
	rctx := replyContext{userID: msg.FromUserName}

	switch msg.MsgType {
	case "text":
		text := stripWeComAtMentions(msg.Content, p.agentID)
		slog.Debug("wecom: message received", "user", msg.FromUserName, "text_len", len(text))
		go p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "wecom",
			MessageID: strconv.FormatInt(msg.MsgId, 10),
			UserID:    msg.FromUserName, UserName: p.resolveUserName(msg.FromUserName),
			Content: text, ReplyCtx: rctx,
		})

	case "image":
		slog.Debug("wecom: image received", "user", msg.FromUserName)
		go func() {
			imgData, err := p.downloadMedia(msg.MediaId)
			if err != nil {
				slog.Error("wecom: download image failed", "error", err)
				return
			}
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "wecom",
				MessageID: strconv.FormatInt(msg.MsgId, 10),
				UserID:    msg.FromUserName, UserName: p.resolveUserName(msg.FromUserName),
				Images:   []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
				ReplyCtx: rctx,
			})
		}()

	case "voice":
		slog.Debug("wecom: voice received", "user", msg.FromUserName, "format", msg.Format)
		go func() {
			audioData, err := p.downloadMedia(msg.MediaId)
			if err != nil {
				slog.Error("wecom: download voice failed", "error", err)
				return
			}
			format := strings.ToLower(msg.Format)
			if format == "" {
				format = "amr"
			}
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "wecom",
				MessageID: strconv.FormatInt(msg.MsgId, 10),
				UserID:    msg.FromUserName, UserName: p.resolveUserName(msg.FromUserName),
				Audio:    &core.AudioAttachment{MimeType: "audio/" + format, Data: audioData, Format: format},
				ReplyCtx: rctx,
			})
		}()

	case "file":
		slog.Info("wecom: file message accepted", "user", msg.FromUserName, "file_name", msg.FileName, "media_id_len", len(msg.MediaId))
		if msg.MediaId == "" {
			slog.Warn("wecom: file message missing MediaId")
			return
		}
		go func() {
			fileData, err := p.downloadMedia(msg.MediaId)
			if err != nil {
				slog.Error("wecom: download file failed", "error", err)
				return
			}
			baseName := filepath.Base(strings.TrimSpace(msg.FileName))
			if baseName == "" || baseName == "." {
				baseName = "attachment"
			}
			mt := wecomInboundFileMime(baseName, fileData)
			p.handler(p, &core.Message{
				SessionKey: sessionKey, Platform: "wecom",
				MessageID: strconv.FormatInt(msg.MsgId, 10),
				UserID:    msg.FromUserName, UserName: p.resolveUserName(msg.FromUserName),
				Files: []core.FileAttachment{{
					MimeType: mt,
					Data:     fileData,
					FileName: baseName,
				}},
				ReplyCtx: rctx,
			})
		}()

	default:
		slog.Warn("wecom: unsupported inbound message type (no handler)",
			"msg_type", msg.MsgType,
			"msg_id", msg.MsgId,
			"from_user", msg.FromUserName)
	}
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("wecom: invalid reply context type %T", rctx)
	}
	if content == "" {
		return nil
	}

	accessToken, err := p.getAccessToken()
	if err != nil {
		slog.Error("wecom: get access_token failed", "error", err)
		return fmt.Errorf("wecom: get access_token: %w", err)
	}

	if !p.enableMarkdown {
		content = core.StripMarkdown(content)
	}

	chunks := splitByBytes(content, 2000)
	for i, chunk := range chunks {
		var sendErr error
		if p.enableMarkdown {
			sendErr = p.sendMarkdown(accessToken, rc.userID, chunk)
		} else {
			sendErr = p.sendText(accessToken, rc.userID, chunk)
		}
		if sendErr != nil {
			slog.Error("wecom: send failed", "user", rc.userID, "chunk", i, "error", sendErr)
			return sendErr
		}
	}
	slog.Debug("wecom: message sent", "user", rc.userID, "chunks", len(chunks), "total_len", len(content))
	return nil
}

// Send sends a new message (same as Reply for WeChat Work)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.Reply(ctx, rctx, content)
}

// SendImage uploads and sends an image to the user.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("wecom: SendImage: invalid reply context type %T", rctx)
	}

	accessToken, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("wecom: send image: %w", err)
	}

	mediaID, err := p.uploadImageMedia(accessToken, img)
	if err != nil {
		return fmt.Errorf("wecom: send image: %w", err)
	}

	payload := map[string]any{
		"touser":  rc.userID,
		"msgtype": "image",
		"agentid": p.agentID,
		"image":   map[string]string{"media_id": mediaID},
	}

	body, _ := json.Marshal(payload)
	apiURL := p.wecomAPIURL("/cgi-bin/message/send", url.Values{
		"access_token": []string{accessToken},
	})

	resp, err := p.apiClient.Post(apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("wecom: send image: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("wecom: decode send image response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom: send image failed: %d %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

// uploadImageMedia uploads an image to WeChat Work media API and returns the media_id.
func (p *Platform) uploadImageMedia(accessToken string, img core.ImageAttachment) (string, error) {
	name := img.FileName
	if name == "" {
		name = "image.png"
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("media", name)
	if err != nil {
		return "", fmt.Errorf("wecom: create form file: %w", err)
	}
	if _, err := part.Write(img.Data); err != nil {
		return "", fmt.Errorf("wecom: write image data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("wecom: close multipart writer: %w", err)
	}

	apiURL := p.wecomAPIURL("/cgi-bin/media/upload", url.Values{
		"access_token": []string{accessToken},
		"type":         []string{"image"},
	})
	resp, err := p.apiClient.Post(apiURL, writer.FormDataContentType(), body)
	if err != nil {
		return "", fmt.Errorf("wecom: upload image: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MediaID string `json:"media_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("wecom: decode upload response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("wecom: upload image failed: %d %s", result.ErrCode, result.ErrMsg)
	}
	if result.MediaID == "" {
		return "", fmt.Errorf("wecom: upload image: empty media_id")
	}
	return result.MediaID, nil
}

var _ core.ImageSender = (*Platform)(nil)

func (p *Platform) sendMarkdown(accessToken, toUser, content string) error {
	payload := map[string]any{
		"touser":   toUser,
		"msgtype":  "markdown",
		"agentid":  p.agentID,
		"markdown": map[string]string{"content": content},
	}

	body, _ := json.Marshal(payload)
	apiURL := p.wecomAPIURL("/cgi-bin/message/send", url.Values{
		"access_token": []string{accessToken},
	})

	resp, err := p.apiClient.Post(apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("wecom: send markdown: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("wecom: decode send response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom: send markdown failed: %d %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

func (p *Platform) sendText(accessToken, toUser, text string) error {
	payload := map[string]any{
		"touser":  toUser,
		"msgtype": "text",
		"agentid": p.agentID,
		"text":    map[string]string{"content": text},
		"safe":    0,
	}

	body, _ := json.Marshal(payload)
	apiURL := p.wecomAPIURL("/cgi-bin/message/send", url.Values{
		"access_token": []string{accessToken},
	})

	resp, err := p.apiClient.Post(apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("wecom: send message: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("wecom: decode send response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wecom: send failed: %d %s", result.ErrCode, result.ErrMsg)
	}
	return nil
}

func (p *Platform) getAccessToken() (string, error) {
	p.tokenCache.mu.Lock()
	defer p.tokenCache.mu.Unlock()

	if p.tokenCache.token != "" && time.Now().Before(p.tokenCache.expiresAt) {
		return p.tokenCache.token, nil
	}

	apiURL := p.wecomAPIURL("/cgi-bin/gettoken", url.Values{
		"corpid":     []string{p.corpID},
		"corpsecret": []string{p.corpSecret},
	})

	resp, err := p.apiClient.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("wecom: request access_token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("wecom: decode token response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("wecom: get token failed: %d %s", result.ErrCode, result.ErrMsg)
	}

	p.tokenCache.token = result.AccessToken
	p.tokenCache.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn-60) * time.Second)

	slog.Debug("wecom: access_token refreshed", "expires_in", result.ExpiresIn)
	return result.AccessToken, nil
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// wecom:{userID}
	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) < 2 || parts[0] != "wecom" {
		return nil, fmt.Errorf("wecom: invalid session key %q", sessionKey)
	}
	return replyContext{userID: parts[1]}, nil
}

func (p *Platform) Stop() error {
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}

// --- Crypto helpers ---

// verifySignature checks SHA1(sort(token, timestamp, nonce, encrypt)).
func (p *Platform) verifySignature(expected, timestamp, nonce, encrypt string) bool {
	parts := []string{p.token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	got := fmt.Sprintf("%x", h.Sum(nil))
	return got == expected
}

// decodeAESKey converts the 43-char Base64 EncodingAESKey to 32 bytes.
func decodeAESKey(encodingAESKey string) ([]byte, error) {
	if len(encodingAESKey) != 43 {
		return nil, fmt.Errorf("EncodingAESKey must be 43 characters, got %d", len(encodingAESKey))
	}
	return base64.StdEncoding.DecodeString(encodingAESKey + "=")
}

// decrypt decodes and decrypts a Base64-encoded AES-256-CBC ciphertext.
// Layout after decryption + PKCS#7 unpad:
//
//	[16 bytes random] [4 bytes msg_len (big-endian)] [msg_len bytes message] [corp_id]
func (p *Platform) decrypt(cipherBase64 string) (string, error) {
	cipherData, err := base64.StdEncoding.DecodeString(cipherBase64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(p.aesKey)
	if err != nil {
		return "", fmt.Errorf("aes new cipher: %w", err)
	}

	if len(cipherData) < aes.BlockSize || len(cipherData)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length %d", len(cipherData))
	}

	iv := p.aesKey[:16]
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherData))
	mode.CryptBlocks(plain, cipherData)

	plain = pkcs7Unpad(plain)

	if len(plain) < 20 {
		return "", fmt.Errorf("decrypted data too short")
	}

	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if 20+msgLen > len(plain) {
		return "", fmt.Errorf("invalid message length %d in decrypted data (total %d)", msgLen, len(plain))
	}

	msg := string(plain[20 : 20+msgLen])
	corpID := string(plain[20+msgLen:])

	if corpID != p.corpID {
		return "", fmt.Errorf("corp_id mismatch: expected %s, got %s", p.corpID, corpID)
	}

	return msg, nil
}

func pkcs7Unpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > 32 || pad > len(data) {
		return data
	}
	return data[:len(data)-pad]
}

// downloadMedia fetches a temporary media file from WeChat Work by media_id.
func (p *Platform) resolveUserName(userID string) string {
	if cached, ok := p.userNameCache.Load(userID); ok {
		return cached.(string)
	}
	accessToken, err := p.getAccessToken()
	if err != nil {
		slog.Debug("wecom: resolve user name: get token failed", "error", err)
		return userID
	}
	apiURL := p.wecomAPIURL("/cgi-bin/user/get", url.Values{
		"access_token": []string{accessToken},
		"userid":       []string{userID},
	})
	resp, err := p.apiClient.Get(apiURL)
	if err != nil {
		slog.Debug("wecom: resolve user name failed", "user", userID, "error", err)
		return userID
	}
	defer resp.Body.Close()
	var result struct {
		ErrCode int    `json:"errcode"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.ErrCode != 0 {
		slog.Debug("wecom: resolve user name: api error", "user", userID, "errcode", result.ErrCode)
		return userID
	}
	if result.Name != "" {
		p.userNameCache.Store(userID, result.Name)
		return result.Name
	}
	return userID
}

// wecomInboundFileMime infers MIME type from filename extension, then from content sniffing.
func wecomInboundFileMime(fileName string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			if mt != "application/octet-stream" {
				return mt
			}
		}
	}
	if len(data) > 0 {
		if mt := wecomInboundFileMagicMime(data); mt != "" {
			return mt
		}
		if sniff := http.DetectContentType(data); sniff != "" {
			return sniff
		}
	}
	return "application/octet-stream"
}

func wecomInboundFileMagicMime(data []byte) string {
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 6 {
		head := string(data[:6])
		if head == "GIF87a" || head == "GIF89a" {
			return "image/gif"
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 4 && string(data[:4]) == "%PDF" {
		return "application/pdf"
	}
	return ""
}

func (p *Platform) downloadMedia(mediaID string) ([]byte, error) {
	accessToken, err := p.getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}
	u := p.wecomAPIURL("/cgi-bin/media/get", url.Values{
		"access_token": []string{accessToken},
		"media_id":     []string{mediaID},
	})
	resp, err := p.apiClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// splitByBytes splits text by UTF-8 byte length (WeChat Work limit is 2048 bytes).
func splitByBytes(s string, maxBytes int) []string {
	if len(s) <= maxBytes {
		return []string{s}
	}
	var parts []string
	for len(s) > 0 {
		end := maxBytes
		if end > len(s) {
			end = len(s)
		}
		// Avoid splitting in the middle of a UTF-8 character
		for end > 0 && end < len(s) && s[end]>>6 == 0b10 {
			end--
		}
		if end == 0 {
			end = maxBytes
		}
		parts = append(parts, s[:end])
		s = s[end:]
	}
	return parts
}
