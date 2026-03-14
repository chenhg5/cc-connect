package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

type audioContent struct {
	DownloadCode string `json:"downloadCode"`
	Recognition  string `json:"recognition"`
}

type downloadResponse struct {
	DownloadUrl string `json:"downloadUrl"`
}

type Platform struct {
	clientID              string
	clientSecret          string
	robotCode             string
	allowFrom             string
	shareSessionInChannel bool
	streamClient          *dingtalkClient.StreamClient
	handler               core.MessageHandler
	dedup                 core.MessageDedup
	httpClient            *http.Client
	accessToken           string
	tokenExpiry           time.Time
}

func New(opts map[string]any) (core.Platform, error) {
	clientID, _ := opts["client_id"].(string)
	clientSecret, _ := opts["client_secret"].(string)
	robotCode, _ := opts["robot_code"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("dingtalk", allowFrom)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("dingtalk: client_id and client_secret are required")
	}
	if robotCode == "" {
		robotCode = clientID // fallback to client_id if robot_code not specified
	}
	return &Platform{
		clientID:              clientID,
		clientSecret:          clientSecret,
		robotCode:             robotCode,
		allowFrom:             allowFrom,
		shareSessionInChannel: shareSessionInChannel,
		httpClient:            &http.Client{Timeout: 30 * time.Second},
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
	slog.Debug("dingtalk: message received", "user", data.SenderNick, "msgtype", data.Msgtype)

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

	// Handle audio messages
	if data.Msgtype == "audio" {
		p.handleAudioMessage(data, sessionKey)
		return
	}

	// Handle text messages (default)
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

func (p *Platform) handleAudioMessage(data *chatbot.BotCallbackDataModel, sessionKey string) {
	slog.Debug("dingtalk: audio message received", "user", data.SenderNick)

	// Parse audio content from the raw content
	audioData, ok := data.Content.(map[string]interface{})
	if !ok {
		slog.Error("dingtalk: invalid audio content type", "type", fmt.Sprintf("%T", data.Content))
		return
	}

	downloadCode, _ := audioData["downloadCode"].(string)
	recognition, _ := audioData["recognition"].(string)

	if downloadCode == "" {
		slog.Error("dingtalk: audio message missing downloadCode")
		return
	}

	// Download audio file
	audioBytes, mimeType, err := p.downloadAudio(downloadCode)
	if err != nil {
		slog.Error("dingtalk: failed to download audio", "error", err)
		// Fallback to recognition text if available
		if recognition != "" {
			msg := &core.Message{
				SessionKey: sessionKey,
				Platform:   "dingtalk",
				UserID:     data.SenderStaffId,
				UserName:   data.SenderNick,
				Content:    recognition,
				MessageID:  data.MsgId,
				ReplyCtx:   replyContext{sessionWebhook: data.SessionWebhook},
				FromVoice:  true,
			}
			p.handler(p, msg)
		}
		return
	}

	slog.Info("dingtalk: audio downloaded successfully", "size", len(audioBytes), "mime", mimeType)

	// Create message with audio attachment
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "dingtalk",
		UserID:     data.SenderStaffId,
		UserName:   data.SenderNick,
		Content:    recognition, // Use recognition as text content
		MessageID:  data.MsgId,
		ReplyCtx:   replyContext{sessionWebhook: data.SessionWebhook},
		FromVoice:  true,
		Audio: &core.AudioAttachment{
			MimeType: mimeType,
			Data:     audioBytes,
			Format:   "amr", // DingTalk typically uses AMR format
		},
	}

	p.handler(p, msg)
}

func (p *Platform) downloadAudio(downloadCode string) ([]byte, string, error) {
	// Get download URL
	downloadURL, err := p.getDownloadURL(downloadCode)
	if err != nil {
		return nil, "", fmt.Errorf("get download URL: %w", err)
	}

	// Download audio file
	resp, err := p.httpClient.Get(downloadURL)
	if err != nil {
		return nil, "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	// Determine MIME type from Content-Type header
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/amr" // Default to AMR if not specified
	}

	return data, mimeType, nil
}

func (p *Platform) getDownloadURL(downloadCode string) (string, error) {
	token, err := p.getAccessToken()
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}

	reqBody := map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    p.robotCode,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://api.dingtalk.com/v1.0/robot/messageFiles/download",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api returned status %d", resp.StatusCode)
	}

	var result downloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.DownloadUrl == "" {
		return "", fmt.Errorf("empty downloadUrl in response")
	}

	return result.DownloadUrl, nil
}

func (p *Platform) getAccessToken() (string, error) {
	// Return cached token if still valid
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	// Request new access token
	url := fmt.Sprintf("https://api.dingtalk.com/v1.0/oauth2/accessToken?appKey=%s&appSecret=%s",
		p.clientID, p.clientSecret)

	resp, err := p.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api returned status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty accessToken in response")
	}

	// Cache token with 5 minutes buffer before expiry
	p.accessToken = tokenResp.AccessToken
	expiry := tokenResp.ExpireIn
	if expiry > 300 {
		expiry -= 300 // 5 minute buffer
	}
	p.tokenExpiry = time.Now().Add(time.Duration(expiry) * time.Second)

	slog.Debug("dingtalk: access token refreshed", "expires_at", p.tokenExpiry)
	return p.accessToken, nil
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
