package wecom

import (
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
	"net/http"
	"net/url"
	"sort"
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
}

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
		transport.Proxy = http.ProxyURL(u)
		transport.DisableKeepAlives = true // prevent CONNECT tunnel accumulation on proxy
		slog.Info("wecom: outbound API requests will use proxy (keep-alive disabled)", "proxy", proxyURL)
	}
	apiClient := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	enableMarkdown, _ := opts["enable_markdown"].(bool)

	return &Platform{
		corpID:         corpID,
		corpSecret:     corpSecret,
		agentID:        agentID,
		token:          callbackToken,
		aesKey:         aesKey,
		port:           port,
		callbackPath:   path,
		enableMarkdown: enableMarkdown,
		apiClient:      apiClient,
	}, nil
}

func (p *Platform) Name() string { return "wecom" }

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

// handleMessage processes incoming encrypted message POSTs.
func (p *Platform) handleMessage(w http.ResponseWriter, r *http.Request, msgSig, timestamp, nonce string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var encMsg xmlEncryptedMsg
	if err := xml.Unmarshal(body, &encMsg); err != nil {
		slog.Error("wecom: parse xml failed", "error", err)
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

	// Return 200 immediately (WeChat Work requires response within 5 seconds)
	w.WriteHeader(http.StatusOK)

	var msg xmlMessage
	if err := xml.Unmarshal([]byte(plainXML), &msg); err != nil {
		slog.Error("wecom: parse decrypted xml failed", "error", err)
		return
	}

	if p.dedup.isDuplicate(msg.MsgId) {
		slog.Debug("wecom: skipping duplicate message", "msg_id", msg.MsgId)
		return
	}

	sessionKey := fmt.Sprintf("wecom:%s", msg.FromUserName)
	rctx := replyContext{userID: msg.FromUserName}

	switch msg.MsgType {
	case "text":
		slog.Debug("wecom: message received", "user", msg.FromUserName, "text_len", len(msg.Content))
		go p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "wecom",
			UserID: msg.FromUserName, UserName: msg.FromUserName,
			Content: msg.Content, ReplyCtx: rctx,
		})

	case "image":
		slog.Debug("wecom: image received", "user", msg.FromUserName)
		imgData, err := p.downloadMedia(msg.MediaId)
		if err != nil {
			slog.Error("wecom: download image failed", "error", err)
			return
		}
		go p.handler(p, &core.Message{
			SessionKey: sessionKey, Platform: "wecom",
			UserID: msg.FromUserName, UserName: msg.FromUserName,
			Images:  []core.ImageAttachment{{MimeType: "image/jpeg", Data: imgData}},
			ReplyCtx: rctx,
		})

	default:
		slog.Debug("wecom: ignoring unsupported message type", "type", msg.MsgType)
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

func (p *Platform) sendMarkdown(accessToken, toUser, content string) error {
	payload := map[string]any{
		"touser":   toUser,
		"msgtype":  "markdown",
		"agentid":  p.agentID,
		"markdown": map[string]string{"content": content},
	}

	body, _ := json.Marshal(payload)
	apiURL := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + accessToken

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
	apiURL := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + accessToken

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

	apiURL := fmt.Sprintf(
		"https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		p.corpID, p.corpSecret,
	)

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
func (p *Platform) downloadMedia(mediaID string) ([]byte, error) {
	accessToken, err := p.getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}
	u := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=%s&media_id=%s", accessToken, mediaID)
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
