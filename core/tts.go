package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		"input": map[string]any{
			"text":          text,
			"voice":         voice,
			"language_type": opts.LanguageType,
		},
	}
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
