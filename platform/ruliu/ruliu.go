package ruliu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	defaultPort        = "8082"
	defaultCallbackURL = "/ruliu/callback"
	maxTextRunes       = 2000
)

func init() {
	core.RegisterPlatform("ruliu", New)
}

type replyContext struct {
	groupID int64
}

type Platform struct {
	webhook              string
	token                string
	aesKey               []byte
	allowFrom            string
	port                 string
	callbackPath         string
	skipVerify           bool
	shareSessionInGroup  bool
	server               *http.Server
	handler              core.MessageHandler
	dedup                core.MessageDedup
}

type callbackEnvelope struct {
	EventType string          `json:"eventtype"`
	AgentID   int64           `json:"agentid"`
	GroupID   int64           `json:"groupid"`
	CorpID    string          `json:"corpid"`
	Message   callbackMessage `json:"message"`
	Time      int64           `json:"time"`
}

type callbackMessage struct {
	Header callbackHeader     `json:"header"`
	Body   []callbackBodyItem `json:"body"`
}

type callbackHeader struct {
	FromUserID string        `json:"fromuserid"`
	ToID       int64         `json:"toid"`
	ToType     string        `json:"totype"`
	MsgType    string        `json:"msgtype"`
	ClientMsg  int64         `json:"clientmsgid"`
	MessageID  int64         `json:"messageid"`
	MsgSeqID   string        `json:"msgseqid"`
	At         callbackAt    `json:"at"`
	Extra      string        `json:"extra"`
	ServerTime int64         `json:"servertime"`
	ClientTime int64         `json:"clienttime"`
	UpdateTime int64         `json:"updatetime"`
}

type callbackAt struct {
	AtRobotIDs []int64  `json:"atrobotids"`
	AtUserIDs  []string `json:"atuserids"`
}

type callbackBodyItem struct {
	Type        string   `json:"type"`
	Content     string   `json:"content"`
	Label       string   `json:"label"`
	Name        string   `json:"name"`
	DownloadURL string   `json:"downloadurl"`
	UserID      string   `json:"userid"`
	AtUserIDs   []string `json:"atuserids"`
}

type sendRequest struct {
	Message sendMessage `json:"message"`
}

type sendMessage struct {
	Header sendHeader     `json:"header"`
	Body   []sendBodyItem `json:"body"`
}

type sendHeader struct {
	ToID []int64 `json:"toid,omitempty"`
}

type sendBodyItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type sendResponse struct {
	ErrCode   int    `json:"errcode"`
	ErrorCode int    `json:"errorcode"`
	ErrMsg    string `json:"errmsg"`
	Msg       string `json:"msg"`
}

func New(opts map[string]any) (core.Platform, error) {
	webhook, _ := opts["webhook"].(string)
	token, _ := opts["token"].(string)
	encodingAESKey, _ := opts["encoding_aes_key"].(string)
	if webhook == "" || token == "" || encodingAESKey == "" {
		return nil, fmt.Errorf("ruliu: webhook, token, and encoding_aes_key are required")
	}

	aesKey, err := decodeEncodingAESKey(encodingAESKey)
	if err != nil {
		return nil, fmt.Errorf("ruliu: invalid encoding_aes_key: %w", err)
	}

	port, _ := opts["port"].(string)
	if port == "" {
		port = defaultPort
	}
	callbackPath, _ := opts["callback_path"].(string)
	if callbackPath == "" {
		callbackPath = defaultCallbackURL
	}
	allowFrom, _ := opts["allow_from"].(string)
	skipVerify, _ := opts["skip_signature_check"].(bool)
	shareSessionInGroup, _ := opts["share_session_in_group"].(bool)
	core.CheckAllowFrom("ruliu", allowFrom)

	return &Platform{
		webhook:             webhook,
		token:               token,
		aesKey:              aesKey,
		allowFrom:           allowFrom,
		port:                port,
		callbackPath:        callbackPath,
		skipVerify:          skipVerify,
		shareSessionInGroup: shareSessionInGroup,
	}, nil
}

func (p *Platform) Name() string { return "ruliu" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	mux := http.NewServeMux()
	mux.HandleFunc(p.callbackPath, p.callbackHandler)

	p.server = &http.Server{
		Addr:    ":" + p.port,
		Handler: mux,
	}

	go func() {
		slog.Info("ruliu: webhook server listening", "port", p.port, "path", p.callbackPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("ruliu: server error", "error", err)
		}
	}()

	return nil
}

func (p *Platform) callbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("ruliu: read callback body failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	signature := callbackParam(r, body, "signature")
	timestamp := callbackParam(r, body, "timestamp")
	rn := callbackParam(r, body, "rn")
	echostr := callbackParam(r, body, "echostr")
	if !p.skipVerify && (signature == "" || timestamp == "" || rn == "") {
		slog.Warn("ruliu: missing signature params", "raw_query", r.URL.RawQuery, "content_type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !p.skipVerify && !p.verifySignature(signature, rn, timestamp) {
		slog.Warn("ruliu: signature verification failed",
			"signature", signature,
			"expected", p.signatureFor(rn, timestamp),
			"rn", rn,
			"timestamp", timestamp,
			"raw_query", r.URL.RawQuery,
			"content_type", r.Header.Get("Content-Type"),
		)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if p.skipVerify && signature != "" && timestamp != "" && rn != "" && !p.verifySignature(signature, rn, timestamp) {
		slog.Warn("ruliu: signature verification skipped",
			"signature", signature,
			"expected", p.signatureFor(rn, timestamp),
			"rn", rn,
			"timestamp", timestamp,
			"raw_query", r.URL.RawQuery,
			"content_type", r.Header.Get("Content-Type"),
		)
	}

	if echostr != "" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, echostr)
		return
	}

	raw := strings.TrimSpace(string(body))
	if raw == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	payload, err := p.decryptPayload(raw)
	if err != nil {
		slog.Error("ruliu: decrypt callback body failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var envelope callbackEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		slog.Error("ruliu: decode callback payload failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	if msgTime, ok := callbackTime(envelope.Time); ok && core.IsOldMessage(msgTime) {
		slog.Debug("ruliu: ignoring old callback", "time", envelope.Time)
		return
	}
	if envelope.EventType != "MESSAGE_RECEIVE" {
		slog.Debug("ruliu: ignoring unsupported event", "event_type", envelope.EventType)
		return
	}

	fromUserID := envelope.Message.Header.FromUserID
	if !core.AllowList(p.allowFrom, fromUserID) {
		slog.Debug("ruliu: message from unauthorized user", "user", fromUserID)
		return
	}

	messageID := callbackMessageID(envelope.Message.Header)
	if messageID != "" && p.dedup.IsDuplicate(messageID) {
		slog.Debug("ruliu: duplicate message ignored", "message_id", messageID)
		return
	}

	content := extractMessageText(envelope.Message.Body)
	if strings.TrimSpace(content) == "" {
		slog.Debug("ruliu: ignoring callback without text content", "message_id", messageID)
		return
	}

	groupID := callbackGroupID(envelope)
	if groupID == 0 {
		slog.Warn("ruliu: missing group id in callback", "message_id", messageID)
		return
	}

	var sessionKey string
	if p.shareSessionInGroup {
		sessionKey = fmt.Sprintf("ruliu:%d", groupID)
		content = fmt.Sprintf("[%s]: %s", fromUserID, content)
	} else {
		sessionKey = fmt.Sprintf("ruliu:%d:%s", groupID, fromUserID)
	}
	p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "ruliu",
		MessageID:  messageID,
		UserID:     fromUserID,
		UserName:   fromUserID,
		Content:    content,
		ReplyCtx:   replyContext{groupID: groupID},
	})
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("ruliu: invalid reply context type %T", rctx)
	}
	if content == "" {
		return nil
	}

	content = core.StripMarkdown(content)
	for _, chunk := range splitByRunes(content, maxTextRunes) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		if err := p.sendText(ctx, rc.groupID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.Reply(ctx, rctx, content)
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "ruliu" {
		return nil, fmt.Errorf("ruliu: invalid session key %q", sessionKey)
	}
	groupID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ruliu: invalid group id in session key %q: %w", sessionKey, err)
	}
	return replyContext{groupID: groupID}, nil
}

func (p *Platform) Stop() error {
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}

func (p *Platform) verifySignature(signature, rn, timestamp string) bool {
	return strings.EqualFold(signature, p.signatureFor(rn, timestamp))
}

func (p *Platform) signatureFor(rn, timestamp string) string {
	sum := md5.Sum([]byte(rn + timestamp + p.token))
	return hex.EncodeToString(sum[:])
}

func (p *Platform) decryptPayload(raw string) ([]byte, error) {
	ciphertext, err := decodeURLBase64(raw)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	plaintext, err := decryptECB(ciphertext, p.aesKey)
	if err != nil {
		return nil, err
	}
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func (p *Platform) sendText(ctx context.Context, groupID int64, text string) error {
	reqBody, err := json.Marshal(sendRequest{
		Message: sendMessage{
			Header: sendHeader{ToID: []int64{groupID}},
			Body: []sendBodyItem{{
				Type:    "TEXT",
				Content: text,
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("ruliu: marshal send request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.webhook, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("ruliu: build send request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ruliu: send message: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ruliu: read send response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ruliu: send returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result sendResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("ruliu: decode send response: %w", err)
	}
	code := result.ErrCode
	if code == 0 {
		code = result.ErrorCode
	}
	msg := result.ErrMsg
	if msg == "" {
		msg = result.Msg
	}
	if code != 0 {
		return fmt.Errorf("ruliu: send failed: %d %s", code, msg)
	}
	return nil
}

func callbackParam(r *http.Request, body []byte, key string) string {
	if value := rawKVValue(r.URL.RawQuery, key); value != "" {
		return value
	}
	if value := rawBodyValue(r.Header.Get("Content-Type"), body, key); value != "" {
		return value
	}
	return ""
}

func rawBodyValue(contentType string, body []byte, key string) string {
	if len(body) == 0 {
		return ""
	}

	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		return rawKVValue(string(body), key)
	}
	if strings.HasPrefix(contentType, "application/json") {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if value, ok := payload[key].(string); ok {
				return value
			}
		}
	}
	return ""
}

func rawKVValue(raw, key string) string {
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k != key {
			continue
		}
		decoded, err := url.QueryUnescape(v)
		if err != nil {
			return v
		}
		return decoded
	}
	return ""
}

func callbackGroupID(envelope callbackEnvelope) int64 {
	if envelope.GroupID > 0 {
		return envelope.GroupID
	}
	if envelope.Message.Header.ToID > 0 {
		return envelope.Message.Header.ToID
	}
	return 0
}

func callbackMessageID(header callbackHeader) string {
	switch {
	case header.MessageID > 0:
		return strconv.FormatInt(header.MessageID, 10)
	case header.ClientMsg > 0:
		return "client:" + strconv.FormatInt(header.ClientMsg, 10)
	case strings.TrimSpace(header.MsgSeqID) != "":
		return "seq:" + strings.TrimSpace(header.MsgSeqID)
	default:
		return ""
	}
}

func extractMessageText(items []callbackBodyItem) string {
	var parts []string
	for _, item := range items {
		switch item.Type {
		case "TEXT":
			if text := strings.TrimSpace(item.Content); text != "" {
				parts = append(parts, text)
			}
		case "LINK":
			if label := strings.TrimSpace(item.Label); label != "" {
				parts = append(parts, label)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func splitByRunes(s string, maxLen int) []string {
	if maxLen <= 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return []string{s}
	}

	parts := make([]string, 0, len(runes)/maxLen+1)
	for len(runes) > 0 {
		end := maxLen
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

func callbackTime(ts int64) (time.Time, bool) {
	if ts <= 0 {
		return time.Time{}, false
	}
	// 如流文档示例里 time 是时间戳，但未明确固定秒/毫秒；这里兼容两种格式。
	if ts < 1_000_000_000_000 {
		return time.Unix(ts, 0), true
	}
	return time.UnixMilli(ts), true
}

func decodeEncodingAESKey(s string) ([]byte, error) {
	if len(s) != 22 {
		return nil, fmt.Errorf("expected 22 chars, got %d", len(s))
	}
	key, err := base64.StdEncoding.DecodeString(s + "==")
	if err != nil {
		return nil, err
	}
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("decoded key length %d, want %d", len(key), aes.BlockSize)
	}
	return key, nil
}

func decodeURLBase64(s string) ([]byte, error) {
	replacer := strings.NewReplacer("-", "+", "_", "/")
	s = replacer.Replace(s)
	switch len(s) % 4 {
	case 0:
	case 2:
		s += "=="
	case 3:
		s += "="
	default:
		return nil, fmt.Errorf("invalid base64 length")
	}
	return base64.StdEncoding.DecodeString(s)
}

func decryptECB(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid ciphertext length %d", len(ciphertext))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	plaintext := make([]byte, len(ciphertext))
	for start := 0; start < len(ciphertext); start += aes.BlockSize {
		block.Decrypt(plaintext[start:start+aes.BlockSize], ciphertext[start:start+aes.BlockSize])
	}
	return plaintext, nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid plaintext length %d", len(data))
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid padding size %d", padding)
	}
	for i := len(data) - padding; i < len(data); i++ {
		if int(data[i]) != padding {
			return nil, fmt.Errorf("invalid padding bytes")
		}
	}
	return data[:len(data)-padding], nil
}
