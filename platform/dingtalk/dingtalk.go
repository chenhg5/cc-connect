package dingtalk

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/braheezy/shine-mp3/pkg/mp3"

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
	agentID               int64    // Agent ID for work notifications API (numeric)
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bodyBytes))
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

// SendAudio uploads audio bytes to DingTalk and sends a voice message.
// Implements core.AudioSender interface.
// Uses DingTalk oToMessages API with msgKey: "sampleAudio" (voice messages).
// DingTalk voice messages only support ogg/amr formats (not mp3).
func (p *Platform) SendAudio(ctx context.Context, rctx any, audio []byte, format string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("dingtalk: SendAudio: invalid reply context type %T", rctx)
	}

	slog.Info("dingtalk: SendAudio called", "format", format, "size", len(audio), "conversation_id", rc.conversationId)

	// Convert MP3 to OGG if needed (DingTalk voice messages only support ogg/amr)
	if strings.ToLower(format) == "mp3" {
		slog.Info("dingtalk: converting MP3 to OGG format (DingTalk requirement)")
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
		slog.Info("dingtalk: audio converted", "new_format", format, "new_size", len(audio))
	}

	// Compress audio if too large (DingTalk limit is 2MB)
	const maxAudioSize = 2 * 1024 * 1024
	if len(audio) > maxAudioSize {
		slog.Info("dingtalk: audio too large, compressing", "size", len(audio), "max", maxAudioSize)
		compressed, compressedFormat, err := p.compressAudio(ctx, audio, format)
		if err != nil {
			slog.Warn("dingtalk: compression failed, using original", "error", err)
		} else {
			audio = compressed
			format = compressedFormat
			slog.Info("dingtalk: audio compressed", "new_size", len(audio), "new_format", format)
		}
	}

	// Upload audio to DingTalk media API
	mediaID, err := p.uploadMedia(ctx, audio, format)
	if err != nil {
		return fmt.Errorf("dingtalk: upload audio: %w", err)
	}

	slog.Info("dingtalk: audio uploaded", "media_id", mediaID, "format", format, "size", len(audio))

	// Calculate duration from audio size (rough estimate)
	// OGG: ~8KB/sec at 64kbps, AMR: ~4KB/sec at 12.2kbps
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

	slog.Info("dingtalk: sending voice via oToMessages API", "media_id", mediaID, "duration", durationMs, "user_id", rc.senderStaffId)

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
	slog.Info("dingtalk: oToMessages API response", "status", resp.StatusCode, "body", string(respBody))

	if resp.StatusCode != 200 {
		return fmt.Errorf("dingtalk: send audio failed: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	slog.Info("dingtalk: voice message sent successfully", "media_id", mediaID, "conversation_id", rc.conversationId)
	return nil
}

// compressAudio compresses audio if it exceeds size limits.
// Tries pure Go MP3 encoding first, falls back to ffmpeg if unavailable.
// Converts WAV to MP3 format (DingTalk supported, ~10:1 compression ratio).
func (p *Platform) compressAudio(ctx context.Context, audio []byte, format string) ([]byte, string, error) {
	// Only WAV format can be compressed to MP3
	if strings.ToLower(format) != "wav" {
		return nil, "", fmt.Errorf("only WAV format can be compressed, got: %s", format)
	}

	// Try pure Go MP3 encoding first (no external dependencies)
	mp3Data, err := p.encodeWAVToMP3(audio)
	if err != nil {
		slog.Warn("dingtalk: pure Go MP3 encoding failed, trying ffmpeg", "error", err)
		return p.compressAudioWithFFmpeg(ctx, audio, format)
	}

	slog.Info("dingtalk: audio compressed successfully", "original_size", len(audio), "compressed_size", len(mp3Data), "ratio", float64(len(mp3Data))/float64(len(audio)))
	return mp3Data, "mp3", nil
}

// encodeWAVToMP3 uses pure Go shine-mp3 encoder to compress WAV to MP3.
// No external dependencies required.
func (p *Platform) encodeWAVToMP3(wavData []byte) ([]byte, error) {
	slog.Info("dingtalk: starting MP3 encoding", "wav_size", len(wavData))

	// Parse WAV header to extract PCM data as int16 samples
	pcmInt16, sampleRate, numChannels, bitsPerSample, err := parseWAVToInt16(wavData)
	if err != nil {
		slog.Error("dingtalk: WAV parsing failed", "error", err)
		return nil, fmt.Errorf("parse WAV: %w", err)
	}

	slog.Info("dingtalk: WAV parsed successfully", "sample_rate", sampleRate, "channels", numChannels, "bits", bitsPerSample, "samples", len(pcmInt16))

	// Validate format (shine-mp3 expects 16-bit signed PCM)
	if bitsPerSample != 16 {
		slog.Error("dingtalk: unsupported bits per sample", "bits", bitsPerSample)
		return nil, fmt.Errorf("unsupported bits per sample: %d (only 16-bit supported)", bitsPerSample)
	}

	// Create MP3 encoder (128 kbps bitrate fixed by shine)
	encoder := mp3.NewEncoder(sampleRate, numChannels)

	// Encode PCM to MP3 using bytes.Buffer as writer
	var mp3Buffer bytes.Buffer
	if err := encoder.Write(&mp3Buffer, pcmInt16); err != nil {
		slog.Error("dingtalk: MP3 encoding failed", "error", err)
		return nil, fmt.Errorf("encode MP3: %w", err)
	}

	mp3Data := mp3Buffer.Bytes()
	slog.Info("dingtalk: MP3 encoding successful", "mp3_size", len(mp3Data), "compression_ratio", float64(len(wavData))/float64(len(mp3Data)))
	return mp3Data, nil
}

// parseWAVToInt16 parses WAV file header and returns PCM data as int16 samples,
// along with sample rate, channels, and bits per sample.
func parseWAVToInt16(data []byte) (pcm []int16, sampleRate, numChannels, bitsPerSample int, err error) {
	pcmBytes, sampleRate, numChannels, bitsPerSample, err := parseWAV(data)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	// Convert bytes to int16 samples (little-endian)
	pcm = make([]int16, len(pcmBytes)/2)
	for i := 0; i < len(pcm); i++ {
		pcm[i] = int16(binary.LittleEndian.Uint16(pcmBytes[i*2 : i*2+2]))
	}

	return pcm, sampleRate, numChannels, bitsPerSample, nil
}

// parseWAV parses WAV file header and returns PCM data, sample rate, channels, and bits per sample.
func parseWAV(data []byte) (pcm []byte, sampleRate, numChannels, bitsPerSample int, err error) {
	if len(data) < 44 {
		return nil, 0, 0, 0, fmt.Errorf("invalid WAV: too short (%d bytes)", len(data))
	}

	// Check RIFF header
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, 0, fmt.Errorf("invalid WAV: missing RIFF/WAVE headers")
	}

	// Read format chunk (starts at byte 12)
	fmtChunk := data[12:16]
	if string(fmtChunk) != "fmt " {
		return nil, 0, 0, 0, fmt.Errorf("invalid WAV: missing fmt chunk")
	}

	// Parse audio format (should be 1 for PCM)
	audioFormat := binary.LittleEndian.Uint16(data[20:22])
	if audioFormat != 1 {
		return nil, 0, 0, 0, fmt.Errorf("invalid WAV: audio format %d (only PCM=1 supported)", audioFormat)
	}

	// Parse channels
	numChannels = int(binary.LittleEndian.Uint16(data[22:24]))
	if numChannels < 1 || numChannels > 2 {
		return nil, 0, 0, 0, fmt.Errorf("invalid WAV: channels %d (only 1-2 supported)", numChannels)
	}

	// Parse sample rate
	sampleRate = int(binary.LittleEndian.Uint32(data[24:28]))

	// Parse bits per sample
	bitsPerSample = int(binary.LittleEndian.Uint16(data[34:36]))

	// Find data chunk
	dataChunkPos := 12
	for dataChunkPos < len(data)-8 {
		chunkID := string(data[dataChunkPos:dataChunkPos+4])
		chunkSize := binary.LittleEndian.Uint32(data[dataChunkPos+4:dataChunkPos+8])

		if chunkID == "data" {
			// Found data chunk
			dataStart := dataChunkPos + 8
			dataEnd := dataStart + int(chunkSize)
			if dataEnd > len(data) {
				dataEnd = len(data)
			}
			pcm = data[dataStart:dataEnd]
			return pcm, sampleRate, numChannels, bitsPerSample, nil
		}

		// Skip to next chunk (aligned to 2 bytes)
		dataChunkPos += 8 + int(chunkSize)
		if dataChunkPos%2 != 0 {
			dataChunkPos++
		}
	}

	return nil, 0, 0, 0, fmt.Errorf("invalid WAV: no data chunk found")
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

// uploadMedia uploads audio file to DingTalk and returns the media ID.
func (p *Platform) uploadMedia(ctx context.Context, audio []byte, format string) (string, error) {
	// Check audio size limit (DingTalk limit is typically 2MB for voice messages)
	const maxAudioSize = 2 * 1024 * 1024 // 2MB
	if len(audio) > maxAudioSize {
		return "", fmt.Errorf("audio file too large: %d bytes (max %d bytes)", len(audio), maxAudioSize)
	}

	token, err := p.getAccessToken()
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}

	// Use legacy API for media upload: oapi.dingtalk.com/media/upload
	uploadURL := fmt.Sprintf("https://oapi.dingtalk.com/media/upload?access_token=%s&type=voice", token)

	// Create multipart form data with field name "media"
	body := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(body)

	// Create form file with field name "media" (required by DingTalk)
	part, err := writer.CreateFormFile("media", fmt.Sprintf("audio.%s", format))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}

	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
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

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload returned status %d: %s", resp.StatusCode, respBody)
	}

	// Log response for debugging
	respBody, _ := io.ReadAll(resp.Body)
	slog.Info("dingtalk: media upload response", "status", resp.StatusCode, "body", string(respBody))

	var uploadResp struct {
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
		MediaID  string `json:"media_id"`
		Type     string `json:"type"`
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

	slog.Info("dingtalk: media uploaded successfully", "media_id", uploadResp.MediaID, "size", len(audio))
	return uploadResp.MediaID, nil
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
