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
	"os/exec"
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
	sessionWebhook  string
	conversationId  string
	senderStaffId   string
}

type downloadResponse struct {
	DownloadUrl string `json:"downloadUrl"`
}

type Platform struct {
	clientID              string
	clientSecret          string
	robotCode             string
	agentID               int64    // Agent ID for work notifications API (numeric)
	allowFrom             string
	shareSessionInChannel bool
	streamClient          *dingtalkClient.StreamClient
	streamCtxCancel       context.CancelFunc
	handler               core.MessageHandler
	dedup                 core.MessageDedup
	httpClient            *http.Client
	tokenMu               sync.Mutex
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
	// Validate robot_code format (should not be empty after fallback)
	if robotCode == "" {
		return nil, fmt.Errorf("dingtalk: robot_code is required (or client_id)")
	}

	// agent_id is required for work notifications API (numeric type)
	// Try to read as int64 first, then float64 (JSON numbers), fallback to 0
	var agentID int64
	if v, ok := opts["agent_id"].(int64); ok {
		agentID = v
	} else if v, ok := opts["agent_id"].(float64); ok {
		agentID = int64(v)
	} else if v, ok := opts["agent_id"].(int); ok {
		agentID = int64(v)
	}
	// agent_id can be 0 for testing, but will fail in production

	return &Platform{
		clientID:              clientID,
		clientSecret:          clientSecret,
		robotCode:             robotCode,
		agentID:               agentID,
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

	ctx, cancel := context.WithCancel(context.Background())
	p.streamCtxCancel = cancel

	// Run the stream in a restart loop. The SDK's processLoop() runs in a background
	// goroutine and handles keepalive pings internally. If the goroutine exits
	// (e.g. server closes idle connection), Start() returns and we attempt to reconnect.
	// This ensures the bot stays connected even after long periods of silence.
	go func() {
		defer slog.Info("dingtalk: stream runner exited")
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := p.streamClient.Start(ctx); err != nil {
				slog.Warn("dingtalk: stream disconnected, reconnecting", "error", err)
			}

			// Brief pause before reconnecting to avoid tight loop on persistent failures.
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()

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

	// Handle image messages
	if data.Msgtype == "image" {
		p.handleImageMessage(data, sessionKey)
		return
	}

	// Handle text messages (default)
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "dingtalk",
		UserID:     data.SenderStaffId,
		UserName:   data.SenderNick,
		ChatName:   data.ConversationTitle,
		Content:    data.Text.Content,
		MessageID:  data.MsgId,
		ReplyCtx: replyContext{
			sessionWebhook:  data.SessionWebhook,
			conversationId:  data.ConversationId,
			senderStaffId:   data.SenderStaffId,
		},
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
				ReplyCtx: replyContext{
					sessionWebhook:  data.SessionWebhook,
					conversationId:  data.ConversationId,
					senderStaffId:   data.SenderStaffId,
				},
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
		ReplyCtx: replyContext{
			sessionWebhook:  data.SessionWebhook,
			conversationId:  data.ConversationId,
			senderStaffId:   data.SenderStaffId,
		},
		FromVoice:  true,
		Audio: &core.AudioAttachment{
			MimeType: mimeType,
			Data:     audioBytes,
			Format:   "amr", // DingTalk typically uses AMR format
		},
	}

	p.handler(p, msg)
}

func (p *Platform) handleImageMessage(data *chatbot.BotCallbackDataModel, sessionKey string) {
	slog.Debug("dingtalk: image message received", "user", data.SenderNick)

	// Parse image content from the raw content
	imageData, ok := data.Content.(map[string]interface{})
	if !ok {
		slog.Error("dingtalk: invalid image content type", "type", fmt.Sprintf("%T", data.Content))
		return
	}

	downloadCode, _ := imageData["downloadCode"].(string)
	if downloadCode == "" {
		slog.Error("dingtalk: image message missing downloadCode")
		return
	}

	// Download image file using the same messageFiles/download API as audio
	downloadURL, err := p.getDownloadURL(downloadCode)
	if err != nil {
		slog.Error("dingtalk: failed to get image download URL", "error", err)
		return
	}

	resp, err := p.httpClient.Get(downloadURL)
	if err != nil {
		slog.Error("dingtalk: failed to download image", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("dingtalk: image download returned status", "status", resp.StatusCode)
		return
	}

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("dingtalk: failed to read image data", "error", err)
		return
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/png"
	}

	slog.Info("dingtalk: image downloaded successfully", "size", len(imgBytes), "mime", mimeType)

	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "dingtalk",
		UserID:     data.SenderStaffId,
		UserName:   data.SenderNick,
		MessageID:  data.MsgId,
		ReplyCtx: replyContext{
			sessionWebhook:  data.SessionWebhook,
			conversationId:  data.ConversationId,
			senderStaffId:   data.SenderStaffId,
		},
		Images: []core.ImageAttachment{{
			MimeType: mimeType,
			Data:     imgBytes,
		}},
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
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
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	// Return cached token if still valid
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	// Request new access token using DingTalk's new API (api.dingtalk.com/v1.0/oauth2/accessToken)
	// This requires POST request with JSON body
	url := "https://api.dingtalk.com/v1.0/oauth2/accessToken"

	reqBody := map[string]string{
		"appKey":    p.clientID,
		"appSecret": p.clientSecret,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("api returned status %d: %s", resp.StatusCode, body)
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

// SendImage uploads and sends an image via DingTalk oToMessages API.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: SendImage: invalid reply context type %T", rctx)
	}

	name := img.FileName
	if name == "" {
		name = "image.png"
	}

	mediaID, err := p.uploadMedia(ctx, img.Data, name, "image")
	if err != nil {
		return fmt.Errorf("dingtalk: upload image: %w", err)
	}

	slog.Debug("dingtalk: image uploaded", "media_id", mediaID, "size", len(img.Data))

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	msgParamBytes, _ := json.Marshal(map[string]string{"photoURL": mediaID})
	requestBody := map[string]any{
		"robotCode": p.robotCode,
		"userIds":   []string{rc.senderStaffId},
		"msgKey":    "sampleImageMsg",
		"msgParam":  string(msgParamBytes),
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal image message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dingtalk: create image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: send image request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	slog.Debug("dingtalk: oToMessages image response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dingtalk: send image failed: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	slog.Info("dingtalk: image message sent", "media_id", mediaID, "user", rc.senderStaffId)
	return nil
}

var _ core.ImageSender = (*Platform)(nil)

// SendFile uploads and sends a file via DingTalk oToMessages API.
// Implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: SendFile: invalid reply context type %T", rctx)
	}

	name := file.FileName
	if name == "" {
		name = "file"
	}

	mediaID, err := p.uploadMedia(ctx, file.Data, name, "file")
	if err != nil {
		return fmt.Errorf("dingtalk: upload file: %w", err)
	}

	slog.Debug("dingtalk: file uploaded", "media_id", mediaID, "name", name, "size", len(file.Data))

	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	ext := ""
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		ext = name[idx+1:]
	}

	msgParamBytes, _ := json.Marshal(map[string]string{
		"mediaId":  mediaID,
		"fileName": name,
		"fileType": ext,
	})
	requestBody := map[string]any{
		"robotCode": p.robotCode,
		"userIds":   []string{rc.senderStaffId},
		"msgKey":    "sampleFile",
		"msgParam":  string(msgParamBytes),
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal file message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dingtalk: create file request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: send file request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	slog.Debug("dingtalk: oToMessages file response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dingtalk: send file failed: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	slog.Info("dingtalk: file message sent", "media_id", mediaID, "name", name, "user", rc.senderStaffId)
	return nil
}

var _ core.FileSender = (*Platform)(nil)

// SendAudio uploads audio bytes to DingTalk and sends a voice message.
// Implements core.AudioSender interface.
// Uses DingTalk oToMessages API with msgKey: "sampleAudio" (voice messages).
// DingTalk voice messages only support ogg/amr formats (not mp3).
func (p *Platform) SendAudio(ctx context.Context, rctx any, audio []byte, format string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: SendAudio: invalid reply context type %T", rctx)
	}

	slog.Debug("dingtalk: SendAudio called", "format", format, "size", len(audio), "conversation_id", rc.conversationId)

	// Convert MP3 to OGG if needed (DingTalk voice messages only support ogg/amr)
	if strings.ToLower(format) == "mp3" {
		slog.Debug("dingtalk: converting MP3 to OGG format (DingTalk requirement)")
		oggAudio, err := core.ConvertMP3ToOGG(ctx, audio)
		if err != nil {
			slog.Warn("dingtalk: MP3 to OGG conversion failed", "error", err)
			// Fallback: try AMR format instead
			amrAudio, err := core.ConvertMP3ToAMR(ctx, audio)
			if err != nil {
				return fmt.Errorf("dingtalk: convert MP3 to AMR failed: %w", err)
			}
			audio = amrAudio
			format = "amr"
		} else {
			audio = oggAudio
			format = "ogg"
		}
		slog.Debug("dingtalk: audio converted", "new_format", format, "new_size", len(audio))
	}

	// Compress audio if too large (DingTalk limit is 2MB)
	const maxAudioSize = 2 * 1024 * 1024
	if len(audio) > maxAudioSize {
		slog.Debug("dingtalk: audio too large, compressing", "size", len(audio), "max", maxAudioSize)
		compressed, compressedFormat, err := p.compressAudio(ctx, audio, format)
		if err != nil {
			slog.Warn("dingtalk: compression failed, using original", "error", err)
		} else {
			audio = compressed
			format = compressedFormat
			slog.Debug("dingtalk: audio compressed", "new_size", len(audio), "new_format", format)
		}
	}

	// Upload audio to DingTalk media API
	mediaID, err := p.uploadMedia(ctx, audio, fmt.Sprintf("audio.%s", format), "voice")
	if err != nil {
		return fmt.Errorf("dingtalk: upload audio: %w", err)
	}

	slog.Debug("dingtalk: audio uploaded", "media_id", mediaID, "format", format, "size", len(audio))

	// Calculate duration from audio size (rough estimate based on bitrate)
	// NOTE: This is an approximation. For accurate duration, consider using ffprobe or go-audio library.
	// OGG (Opus 64kbps): ~8KB/sec, AMR-NB (12.2kbps): ~4KB/sec, MP3 (128kbps): ~16KB/sec
	var duration int
	if format == "ogg" {
		duration = len(audio) / 8000
	} else if format == "amr" {
		duration = len(audio) / 4000
	} else if format == "mp3" {
		duration = len(audio) / 16000
	} else {
		duration = len(audio) / 32000
	}
	if duration == 0 {
		duration = 1
	}

	durationMs := duration * 1000

	// Use oToMessages API with msgKey: "sampleAudio" for voice messages
	// This is the official API for sending voice messages in bot conversations
	token, err := p.getAccessToken()
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	// Build oToMessages API request with sampleAudio msgKey
	// msgParam must be a JSON string, not an object
	msgParamJSON := fmt.Sprintf(`{"mediaId":"%s","duration":"%d"}`, mediaID, durationMs)
	requestBody := map[string]interface{}{
		"robotCode": p.robotCode,
		"userIds":   []string{rc.senderStaffId},
		"msgKey":    "sampleAudio",
		"msgParam":  msgParamJSON,
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("dingtalk: marshal audio message: %w", err)
	}

	slog.Debug("dingtalk: sending voice via oToMessages API", "media_id", mediaID, "duration", durationMs, "user_id", rc.senderStaffId)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dingtalk: create audio request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk: send audio request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	slog.Debug("dingtalk: oToMessages API response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode != 200 {
		return fmt.Errorf("dingtalk: send audio failed: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	slog.Info("dingtalk: voice message sent successfully", "media_id", mediaID, "conversation_id", rc.conversationId)
	return nil
}

// compressAudio compresses audio if it exceeds size limits.
// Uses ffmpeg to convert WAV to MP3 format (DingTalk supported, ~10:1 compression ratio).
func (p *Platform) compressAudio(ctx context.Context, audio []byte, format string) ([]byte, string, error) {
	// Only WAV format can be compressed to MP3
	if strings.ToLower(format) != "wav" {
		return nil, "", fmt.Errorf("only WAV format can be compressed, got: %s", format)
	}

	return p.compressAudioWithFFmpeg(ctx, audio, format)
}

// compressAudioWithFFmpeg compresses audio using ffmpeg with stdin/stdout pipes.
// Converts WAV to MP3 format (64 kbps for voice).
func (p *Platform) compressAudioWithFFmpeg(ctx context.Context, audio []byte, format string) ([]byte, string, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, "", fmt.Errorf("ffmpeg not found: %w", err)
	}

	args := []string{
		"-i", "pipe:0",
		"-ar", "16000", // 16kHz sample rate for voice
		"-ac", "1",     // mono
		"-b:a", "64k",  // 64 kbps bitrate (voice quality)
		"-f", "mp3",
		"-y",
		"pipe:1",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stdin = bytes.NewReader(audio)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("ffmpeg compression failed: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.Bytes(), "mp3", nil
}

// uploadMedia uploads a file to DingTalk media API and returns the media ID.
// mediaType should be "voice" or "image".
func (p *Platform) uploadMedia(ctx context.Context, data []byte, fileName, mediaType string) (string, error) {
	token, err := p.getAccessToken()
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}

	uploadURL := fmt.Sprintf("https://oapi.dingtalk.com/media/upload?access_token=%s&type=%s", token, mediaType)

	body := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("media", fileName)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}

	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write media data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, body)
	if err != nil {
		return "", fmt.Errorf("create upload request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload returned status %d: %s", resp.StatusCode, respBody)
	}

	slog.Debug("dingtalk: media upload response", "status", resp.StatusCode, "body", string(respBody))

	var uploadResp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MediaID string `json:"media_id"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(respBody, &uploadResp); err != nil {
		return "", fmt.Errorf("decode upload response: %w, body: %s", err, respBody)
	}

	if uploadResp.ErrCode != 0 {
		return "", fmt.Errorf("upload API error %d: %s", uploadResp.ErrCode, uploadResp.ErrMsg)
	}

	if uploadResp.MediaID == "" {
		return "", fmt.Errorf("empty media_id in upload response: %s", respBody)
	}

	slog.Debug("dingtalk: media uploaded successfully", "media_id", uploadResp.MediaID, "type", mediaType, "size", len(data))
	return uploadResp.MediaID, nil
}

func (p *Platform) Stop() error {
	if p.streamCtxCancel != nil {
		p.streamCtxCancel()
	}
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
