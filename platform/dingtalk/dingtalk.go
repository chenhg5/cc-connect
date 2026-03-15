package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingtalkClient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

func init() {
	core.RegisterPlatform("dingtalk", New)
}

type replyContext struct {
	sessionWebhook string
}

type Platform struct {
	clientID              string
	clientSecret          string
	allowFrom             string
	shareSessionInChannel bool
	streamClient          *dingtalkClient.StreamClient
	handler               core.MessageHandler
	dedup                 core.MessageDedup

	// access token cache
	tokenMu      sync.Mutex
	accessToken  string
	tokenExpires time.Time
}

func New(opts map[string]any) (core.Platform, error) {
	clientID, _ := opts["client_id"].(string)
	clientSecret, _ := opts["client_secret"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("dingtalk", allowFrom)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("dingtalk: client_id and client_secret are required")
	}
	return &Platform{
		clientID:              clientID,
		clientSecret:          clientSecret,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
	}, nil
}

func (p *Platform) Name() string { return "dingtalk" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	p.streamClient = dingtalkClient.NewStreamClient(
		dingtalkClient.WithAppCredential(dingtalkClient.NewAppCredentialConfig(p.clientID, p.clientSecret)),
	)

	p.streamClient.RegisterChatBotCallbackRouter(func(ctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		p.onMessage(data)
		return []byte(""), nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.streamClient.Start(context.Background())
	}()

	// Give the stream a short window to fail fast on auth errors.
	// If Start() returns nil quickly, it means it connected successfully (non-blocking SDK).
	// If it doesn't return within 3s, it's a blocking call that's running fine.
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("dingtalk: start stream: %w", err)
		}
	case <-time.After(3 * time.Second):
	}

	slog.Info("dingtalk: stream connected", "client_id", p.clientID)
	return nil
}

func (p *Platform) onMessage(data *chatbot.BotCallbackDataModel) {
	slog.Debug("dingtalk: message received", "user", data.SenderNick, "content_len", len(data.Text.Content))

	if p.dedup.IsDuplicate(data.MsgId) {
		slog.Debug("dingtalk: duplicate message ignored", "msg_id", data.MsgId)
		return
	}

	if data.CreateAt > 0 {
		msgTime := time.Unix(data.CreateAt/1000, (data.CreateAt%1000)*int64(time.Millisecond))
		if core.IsOldMessage(msgTime) {
			slog.Debug("dingtalk: ignoring old message after restart", "create_at", data.CreateAt)
			return
		}
	}

	if !core.AllowList(p.allowFrom, data.SenderStaffId) {
		slog.Debug("dingtalk: message from unauthorized user", "user", data.SenderStaffId)
		return
	}

	var sessionKey string
	if p.shareSessionInChannel {
		sessionKey = fmt.Sprintf("dingtalk:%s", data.ConversationId)
	} else {
		sessionKey = fmt.Sprintf("dingtalk:%s:%s", data.ConversationId, data.SenderStaffId)
	}

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "dingtalk",
		UserID:     data.SenderStaffId,
		UserName:   data.SenderNick,
		Content:    data.Text.Content,
		MessageID:  data.MsgId,
		ReplyCtx:   replyContext{sessionWebhook: data.SessionWebhook},
	}

	p.handler(p, msg)
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: invalid reply context type %T", rctx)
	}

	content = preprocessDingTalkMarkdown(content)

	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": "reply", "text": content},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal reply: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.sessionWebhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dingtalk: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: send reply: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dingtalk: reply returned status %d", resp.StatusCode)
	}
	return nil
}

// Send sends a new message (same as Reply for DingTalk)
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return p.Reply(ctx, rctx, content)
}

func (p *Platform) Stop() error {
	if p.streamClient != nil {
		p.streamClient.Close()
	}
	return nil
}

// preprocessDingTalkMarkdown adapts content for DingTalk's markdown renderer:
//   - Leading spaces → non-breaking spaces (prevents markdown from stripping indentation)
//   - Single \n between non-empty lines → trailing two-space forced line break
//   - Code blocks are left untouched
func preprocessDingTalkMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	inCodeBlock := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
		}
		if inCodeBlock {
			continue
		}
		spaceCount := len(line) - len(strings.TrimLeft(line, " "))
		if spaceCount > 0 {
			lines[i] = strings.Repeat("\u00A0", spaceCount) + line[spaceCount:]
		}
	}

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(line)
		if i < len(lines)-1 {
			if line != "" && lines[i+1] != "" {
				sb.WriteString("  \n")
			} else {
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

// ── Image sending (implements core.ImageSender) ────────────────

// SendImage uploads a local image file to DingTalk and sends it via sessionWebhook.
func (p *Platform) SendImage(ctx context.Context, rctx any, imagePath string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: invalid reply context type %T", rctx)
	}

	mediaID, err := p.uploadMedia(ctx, imagePath, "image")
	if err != nil {
		return fmt.Errorf("dingtalk: upload image: %w", err)
	}

	// DingTalk sessionWebhook does not support mediaId-based image messages.
	// Use markdown with the DingTalk media URL instead.
	downloadURL := fmt.Sprintf("https://oapi.dingtalk.com/media/download?access_token=%s&media_id=%s", p.cachedToken(), mediaID)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": "image", "text": fmt.Sprintf("![](%s)", downloadURL)},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal image message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.sessionWebhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dingtalk: create image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: send image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dingtalk: send image returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// getAccessToken returns a valid access token, fetching a new one if expired.
func (p *Platform) getAccessToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.accessToken != "" && time.Now().Before(p.tokenExpires) {
		return p.accessToken, nil
	}

	payload, _ := json.Marshal(map[string]string{
		"appKey":    p.clientID,
		"appSecret": p.clientSecret,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/oauth2/accessToken",
		bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch access token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode access token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access token returned")
	}

	p.accessToken = result.AccessToken
	// Refresh 5 minutes before actual expiry.
	p.tokenExpires = time.Now().Add(time.Duration(result.ExpireIn)*time.Second - 5*time.Minute)
	slog.Debug("dingtalk: access token refreshed", "expires_in", result.ExpireIn)
	return p.accessToken, nil
}

// cachedToken returns the cached token (best-effort, no refresh).
func (p *Platform) cachedToken() string {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	return p.accessToken
}

// uploadMedia uploads a local file to DingTalk and returns the media_id.
func (p *Platform) uploadMedia(ctx context.Context, filePath, fileType string) (string, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return "", err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("type", fileType)
	part, err := w.CreateFormFile("media", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", fmt.Errorf("copy file data: %w", err)
	}
	w.Close()

	uploadURL := fmt.Sprintf("https://oapi.dingtalk.com/media/upload?access_token=%s", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
		MediaID  string `json:"media_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("upload failed: %s (code %d)", result.ErrMsg, result.ErrCode)
	}
	slog.Debug("dingtalk: media uploaded", "media_id", result.MediaID, "file", filePath)
	return result.MediaID, nil
}
