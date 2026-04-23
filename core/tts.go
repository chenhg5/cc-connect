package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TextToSpeech synthesizes text into audio bytes.
type TextToSpeech interface {
	Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) (audio []byte, format string, err error)
}

// TTSSynthesisOpts carries optional synthesis parameters.
type TTSSynthesisOpts struct {
	Voice        string  // voice name, e.g. "Cherry", "Alloy"; empty = provider default
	LanguageType string  // e.g. "Chinese", "English"; empty = auto-detect
	Speed        float64 // speaking speed multiplier (0.5–2.0); 0 = default
}

// TTSCfg holds TTS configuration for the engine (mirrors SpeechCfg).
type TTSCfg struct {
	Enabled    bool
	Provider   string
	Voice      string // default voice used when TTSSynthesisOpts.Voice is empty
	TTS        TextToSpeech
	MaxTextLen int // max rune count before skipping TTS; 0 = no limit

	mu      sync.RWMutex
	ttsMode string // "voice_only" (default) | "always"
}

// GetTTSMode returns the current TTS mode safely.
func (c *TTSCfg) GetTTSMode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ttsMode == "" {
		return "voice_only"
	}
	return c.ttsMode
}

// SetTTSMode updates the TTS mode safely.
func (c *TTSCfg) SetTTSMode(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttsMode = mode
}

// AudioSender is implemented by platforms that support sending voice/audio messages.
type AudioSender interface {
	SendAudio(ctx context.Context, replyCtx any, audio []byte, format string) error
}

// ──────────────────────────────────────────────────────────────
// QwenTTS — Alibaba DashScope TTS implementation
// ──────────────────────────────────────────────────────────────

// QwenTTS implements TextToSpeech using Alibaba DashScope multimodal generation API.
type QwenTTS struct {
	APIKey  string
	BaseURL string
	Model   string
	Client  *http.Client
}

// NewQwenTTS creates a new QwenTTS instance.
func NewQwenTTS(apiKey, baseURL, model string, client *http.Client) *QwenTTS {
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"
	}
	if model == "" {
		model = "qwen3-tts-flash"
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &QwenTTS{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
		Client:  client,
	}
}

// Synthesize sends text to Qwen TTS API and returns WAV audio bytes.
func (q *QwenTTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = "Cherry"
	}
	reqBody := map[string]any{
		"model": q.Model,
	}
	input := map[string]any{
		"text":  text,
		"voice": voice,
	}
	if opts.LanguageType != "" {
		input["language_type"] = opts.LanguageType
	}
	reqBody["input"] = input
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.BaseURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+q.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("qwen tts API %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Output  struct {
			Audio struct {
				URL string `json:"url"`
			} `json:"audio"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("qwen tts: parse response: %w", err)
	}
	if result.Code != "" {
		return nil, "", fmt.Errorf("qwen tts API error %s: %s", result.Code, result.Message)
	}
	if result.Output.Audio.URL == "" {
		return nil, "", fmt.Errorf("qwen tts: empty audio URL in response")
	}

	// Download WAV from temporary URL
	audioReq, err := http.NewRequestWithContext(ctx, http.MethodGet, result.Output.Audio.URL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: create download request: %w", err)
	}
	audioResp, err := q.Client.Do(audioReq)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: download audio: %w", err)
	}
	defer audioResp.Body.Close()

	wavData, err := io.ReadAll(audioResp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("qwen tts: read audio: %w", err)
	}
	return wavData, "wav", nil
}

// ──────────────────────────────────────────────────────────────
// OpenAITTS — OpenAI-compatible TTS implementation (P1)
// ──────────────────────────────────────────────────────────────

// OpenAITTS implements TextToSpeech using the OpenAI /v1/audio/speech API.
type OpenAITTS struct {
	APIKey  string
	BaseURL string
	Model   string
	Client  *http.Client
}

// NewOpenAITTS creates a new OpenAITTS instance.
func NewOpenAITTS(apiKey, baseURL, model string, client *http.Client) *OpenAITTS {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "tts-1"
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &OpenAITTS{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
		Client:  client,
	}
}

// Synthesize sends text to OpenAI TTS API and returns MP3 audio bytes.
func (o *OpenAITTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = "alloy"
	}
	reqBody := map[string]any{
		"model": o.Model,
		"input": text,
		"voice": voice,
	}
	if opts.Speed > 0 {
		reqBody["speed"] = opts.Speed
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("openai tts: marshal request: %w", err)
	}

	url := strings.TrimRight(o.BaseURL, "/") + "/audio/speech"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, "", fmt.Errorf("openai tts: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("openai tts: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("openai tts API %d: %s", resp.StatusCode, body)
	}

	mp3Data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("openai tts: read audio: %w", err)
	}
	return mp3Data, "mp3", nil
}

// ──────────────────────────────────────────────────────────────
// MiniMaxTTS — MiniMax T2A v2 TTS implementation
// ──────────────────────────────────────────────────────────────

// MiniMaxTTS implements TextToSpeech using the MiniMax T2A v2 API.
type MiniMaxTTS struct {
	APIKey  string
	BaseURL string
	Model   string
	Client  *http.Client
}

// NewMiniMaxTTS creates a new MiniMaxTTS instance.
func NewMiniMaxTTS(apiKey, baseURL, model string, client *http.Client) *MiniMaxTTS {
	if baseURL == "" {
		baseURL = "https://api.minimax.io"
	}
	if model == "" {
		model = "speech-2.8-hd"
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &MiniMaxTTS{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
		Client:  client,
	}
}

// Synthesize sends text to MiniMax T2A v2 API and returns MP3 audio bytes.
func (m *MiniMaxTTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = "English_Graceful_Lady"
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = 1.0
	}

	reqBody := map[string]any{
		"model":        m.Model,
		"text":         text,
		"stream":       true,
		"voice_setting": map[string]any{
			"voice_id": voice,
			"speed":    speed,
		},
		"audio_setting": map[string]any{
			"format":      "mp3",
			"sample_rate": 32000,
		},
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("minimax tts: marshal request: %w", err)
	}

	url := strings.TrimRight(m.BaseURL, "/") + "/v1/t2a_v2"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, "", fmt.Errorf("minimax tts: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("minimax tts: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("minimax tts API %d: %s", resp.StatusCode, body)
	}

	// Parse SSE stream: each line is "data: {...}" with hex-encoded audio chunks.
	var audioBuf bytes.Buffer
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var chunk struct {
			Data struct {
				Audio  string `json:"audio"`
				Status int    `json:"status"`
			} `json:"data"`
			BaseResp struct {
				StatusCode int    `json:"status_code"`
				StatusMsg  string `json:"status_msg"`
			} `json:"base_resp"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.BaseResp.StatusCode != 0 {
			return nil, "", fmt.Errorf("minimax tts API error %d: %s", chunk.BaseResp.StatusCode, chunk.BaseResp.StatusMsg)
		}
		if chunk.Data.Audio != "" {
			audioBytes, err := hex.DecodeString(chunk.Data.Audio)
			if err != nil {
				return nil, "", fmt.Errorf("minimax tts: decode audio hex: %w", err)
			}
			audioBuf.Write(audioBytes)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("minimax tts: read SSE stream: %w", err)
	}
	if audioBuf.Len() == 0 {
		return nil, "", fmt.Errorf("minimax tts: no audio data received")
	}
	return audioBuf.Bytes(), "mp3", nil
}

// ──────────────────────────────────────────────────────────────
// EspeakTTS — Local eSpeak text-to-speech implementation
// ──────────────────────────────────────────────────────────────

// EspeakTTS implements TextToSpeech using the local espeak command.
type EspeakTTS struct {
	Path  string // path to espeak executable (empty = "espeak")
	Voice string // default voice (e.g. "zh", "en", "zh+f3")
}

// NewEspeakTTS creates a new EspeakTTS instance.
func NewEspeakTTS(path, voice string) *EspeakTTS {
	if path == "" {
		path = "espeak"
	}
	if voice == "" {
		voice = "zh" // default to Chinese
	}
	return &EspeakTTS{
		Path:  path,
		Voice: voice,
	}
}

// Synthesize uses espeak to convert text to WAV audio bytes.
func (e *EspeakTTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = e.Voice
	}

	// Build espeak command
	args := []string{
		"-v", voice,
		"-w", "/dev/stdout", // write WAV to stdout (Unix-only; not supported on Windows)
	}

	// Add speed option if specified
	if opts.Speed > 0 {
		// espeak speed is in words per minute, default 160
		// Convert speed multiplier (0.5-2.0) to wpm
		wpm := int(160 * opts.Speed)
		args = append(args, "-s", fmt.Sprintf("%d", wpm))
	}

	// Add text as argument
	args = append(args, text)

	// Execute espeak command
	// Use Output() instead of CombinedOutput() to avoid mixing stderr warnings with audio data
	cmd := exec.Command(e.Path, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, "", fmt.Errorf("espeak: voice=%s text=%q: %w", voice, text, err)
	}

	return output, "wav", nil
}

// ──────────────────────────────────────────────────────────────
// PicoTTS — Google Pico TTS (better quality than espeak, offline)
// ──────────────────────────────────────────────────────────────

// PicoTTS implements TextToSpeech using pico2wave (Google Pico TTS).
type PicoTTS struct {
	Path  string // path to pico2wave executable (empty = "pico2wave")
	Voice string // default voice language (e.g. "zh-CN", "en-US")
}

// NewPicoTTS creates a new PicoTTS instance.
func NewPicoTTS(path, voice string) *PicoTTS {
	if path == "" {
		path = "pico2wave"
	}
	if voice == "" {
		voice = "zh-CN" // default to Chinese
	}
	return &PicoTTS{
		Path:  path,
		Voice: voice,
	}
}

// Synthesize uses pico2wave to convert text to WAV audio bytes.
// pico2wave produces much better quality than espeak.
func (p *PicoTTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = p.Voice
	}

	// Create secure temp file for pico2wave output
	tmpFile, err := os.CreateTemp("", "pico_tts_*.wav")
	if err != nil {
		return nil, "", fmt.Errorf("pico2wave: create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Build pico2wave command
	// --lang: language code (zh-CN for Chinese, en-US for English)
	// --wave: output WAV file path
	args := []string{
		"--lang=" + voice,
		"--wave=" + tmpPath,
		text,
	}

	// Execute pico2wave command
	cmd := exec.Command(p.Path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("pico2wave: voice=%s text=%q: %w, output: %s", voice, text, err, string(output))
	}

	// Read the generated WAV file
	audioData, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, "", fmt.Errorf("pico2wave: read output file: %w", err)
	}

	if len(audioData) == 0 {
		return nil, "", fmt.Errorf("pico2wave: produced empty audio file")
	}

	return audioData, "wav", nil
}

// ──────────────────────────────────────────────────────────────
// EdgeTTS — Microsoft Edge TTS (free, high quality, requires network)
// ──────────────────────────────────────────────────────────────

// EdgeTTS implements TextToSpeech using Microsoft Edge's free TTS API.
// This uses the edge-tts CLI command under the hood.
type EdgeTTS struct {
	Voice string // default voice (e.g. "zh-CN-XiaoxiaoNeural")
}

// NewEdgeTTS creates a new EdgeTTS instance.
func NewEdgeTTS(voice string) *EdgeTTS {
	if voice == "" {
		voice = "zh-CN-XiaoxiaoNeural" // default Chinese voice
	}
	return &EdgeTTS{
		Voice: voice,
	}
}

// Synthesize uses edge-tts CLI to convert text to MP3 audio bytes.
// EdgeTTS provides high-quality neural voices but requires network connection.
func (e *EdgeTTS) Synthesize(ctx context.Context, text string, opts TTSSynthesisOpts) ([]byte, string, error) {
	voice := opts.Voice
	if voice == "" {
		voice = e.Voice
	}

	// Create secure temp file for edge-tts output
	tmpFile, err := os.CreateTemp("", "edge_tts_*.mp3")
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts: create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Use edge-tts CLI directly to avoid code injection risks
	// Pass text via --text argument, not via embedded code
	args := []string{
		"--voice", voice,
		"--text", text,
		"--write-media", tmpPath,
	}

	cmd := exec.CommandContext(ctx, "edge-tts", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts: voice=%s text=%q: %w, output: %s", voice, text, err, string(output))
	}

	// Read the generated MP3 file
	audioData, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts: read output file: %w", err)
	}

	if len(audioData) == 0 {
		return nil, "", fmt.Errorf("edge-tts: produced empty audio file")
	}

	return audioData, "mp3", nil
}
