package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VideoGenerator generates a video attachment from a text prompt.
type VideoGenerator interface {
	GenerateVideo(ctx context.Context, prompt string) (data []byte, format string, err error)
}

// MusicGenerator generates an audio/music attachment from a prompt and options.
type MusicGenerator interface {
	GenerateMusic(ctx context.Context, prompt string, opts MusicGenerationOpts) (data []byte, format string, err error)
}

// VideoGenerationCfg holds video generation configuration for the engine.
type VideoGenerationCfg struct {
	Enabled      bool
	Provider     string
	Generator    VideoGenerator
	MaxPromptLen int
}

// MusicGenerationCfg holds music generation configuration for the engine.
type MusicGenerationCfg struct {
	Enabled         bool
	Provider        string
	Generator       MusicGenerator
	MaxPromptLen    int
	LyricsOptimizer bool
	Instrumental    bool
}

// MusicGenerationOpts carries per-request music generation controls.
type MusicGenerationOpts struct {
	Lyrics          string
	LyricsOptimizer bool
	Instrumental    bool
}

// MiniMaxVideoGenerator implements MiniMax asynchronous video generation.
type MiniMaxVideoGenerator struct {
	APIKey         string
	BaseURL        string
	Model          string
	Duration       int
	Resolution     string
	PollInterval   time.Duration
	Timeout        time.Duration
	Client         *http.Client
	DownloadClient *http.Client
}

func NewMiniMaxVideoGenerator(apiKey, baseURL, model string, client *http.Client) *MiniMaxVideoGenerator {
	if baseURL == "" {
		baseURL = "https://api.minimaxi.com"
	}
	if model == "" {
		model = "MiniMax-Hailuo-2.3"
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &MiniMaxVideoGenerator{
		APIKey:         apiKey,
		BaseURL:        strings.TrimRight(baseURL, "/"),
		Model:          model,
		PollInterval:   10 * time.Second,
		Timeout:        10 * time.Minute,
		Client:         client,
		DownloadClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (m *MiniMaxVideoGenerator) GenerateVideo(ctx context.Context, prompt string) ([]byte, string, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, "", fmt.Errorf("video prompt is required")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	taskID, err := m.createVideoTask(ctx, prompt)
	if err != nil {
		return nil, "", err
	}
	fileID, err := m.pollVideoTask(ctx, taskID)
	if err != nil {
		return nil, "", err
	}
	downloadURL, err := m.retrieveFileURL(ctx, fileID)
	if err != nil {
		return nil, "", err
	}
	data, err := m.downloadFile(ctx, downloadURL)
	if err != nil {
		return nil, "", err
	}
	return data, "mp4", nil
}

func (m *MiniMaxVideoGenerator) createVideoTask(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"prompt": prompt,
		"model":  m.Model,
	}
	if m.Duration > 0 {
		body["duration"] = m.Duration
	}
	if strings.TrimSpace(m.Resolution) != "" {
		body["resolution"] = strings.TrimSpace(m.Resolution)
	}
	var resp struct {
		TaskID   string `json:"task_id"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := m.doJSON(ctx, http.MethodPost, "/v1/video_generation", nil, body, &resp); err != nil {
		return "", fmt.Errorf("minimax video: create task: %w", err)
	}
	if resp.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("minimax video API error %d: %s", resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}
	if strings.TrimSpace(resp.TaskID) == "" {
		return "", fmt.Errorf("minimax video: empty task_id")
	}
	return resp.TaskID, nil
}

func (m *MiniMaxVideoGenerator) pollVideoTask(ctx context.Context, taskID string) (string, error) {
	interval := m.PollInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("minimax video: polling task %s: %w", taskID, ctx.Err())
		case <-timer.C:
		}
		var resp struct {
			Status       string `json:"status"`
			FileID       string `json:"file_id"`
			ErrorMessage string `json:"error_message"`
			BaseResp     struct {
				StatusCode int    `json:"status_code"`
				StatusMsg  string `json:"status_msg"`
			} `json:"base_resp"`
		}
		q := url.Values{"task_id": []string{taskID}}
		if err := m.doJSON(ctx, http.MethodGet, "/v1/query/video_generation", q, nil, &resp); err != nil {
			return "", fmt.Errorf("minimax video: query task: %w", err)
		}
		if resp.BaseResp.StatusCode != 0 {
			return "", fmt.Errorf("minimax video query API error %d: %s", resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
		}
		status := strings.ToLower(strings.TrimSpace(resp.Status))
		switch status {
		case "success", "completed", "succeeded":
			if strings.TrimSpace(resp.FileID) == "" {
				return "", fmt.Errorf("minimax video: task succeeded without file_id")
			}
			return resp.FileID, nil
		case "fail", "failed", "error":
			if resp.ErrorMessage == "" {
				resp.ErrorMessage = resp.BaseResp.StatusMsg
			}
			return "", fmt.Errorf("minimax video task failed: %s", resp.ErrorMessage)
		}
		timer.Reset(interval)
	}
}

func (m *MiniMaxVideoGenerator) retrieveFileURL(ctx context.Context, fileID string) (string, error) {
	var resp struct {
		File struct {
			DownloadURL string `json:"download_url"`
		} `json:"file"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	q := url.Values{"file_id": []string{fileID}}
	if err := m.doJSON(ctx, http.MethodGet, "/v1/files/retrieve", q, nil, &resp); err != nil {
		return "", fmt.Errorf("minimax video: retrieve file: %w", err)
	}
	if resp.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("minimax video retrieve API error %d: %s", resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}
	if strings.TrimSpace(resp.File.DownloadURL) == "" {
		return "", fmt.Errorf("minimax video: empty download_url")
	}
	return resp.File.DownloadURL, nil
}

func (m *MiniMaxVideoGenerator) downloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	client := m.DownloadClient
	if client == nil {
		client = m.Client
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download API %d: %s", resp.StatusCode, trimForError(data))
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("download returned empty file")
	}
	return data, nil
}

func (m *MiniMaxVideoGenerator) doJSON(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	u := strings.TrimRight(m.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API %d: %s", resp.StatusCode, trimForError(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

// MiniMaxMusicGenerator implements MiniMax music generation.
type MiniMaxMusicGenerator struct {
	APIKey     string
	BaseURL    string
	Model      string
	SampleRate int
	Bitrate    int
	Format     string
	Client     *http.Client
}

func NewMiniMaxMusicGenerator(apiKey, baseURL, model string, client *http.Client) *MiniMaxMusicGenerator {
	if baseURL == "" {
		baseURL = "https://api.minimaxi.com"
	}
	if model == "" {
		model = "music-2.6"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &MiniMaxMusicGenerator{
		APIKey:     apiKey,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		SampleRate: 44100,
		Bitrate:    256000,
		Format:     "mp3",
		Client:     client,
	}
}

func (m *MiniMaxMusicGenerator) GenerateMusic(ctx context.Context, prompt string, opts MusicGenerationOpts) ([]byte, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("music prompt is required")
	}
	format := strings.TrimSpace(m.Format)
	if format == "" {
		format = "mp3"
	}
	sampleRate := m.SampleRate
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	bitrate := m.Bitrate
	if bitrate <= 0 {
		bitrate = 256000
	}
	body := map[string]any{
		"model":         m.Model,
		"prompt":        prompt,
		"output_format": "hex",
		"audio_setting": map[string]any{
			"sample_rate": sampleRate,
			"bitrate":     bitrate,
			"format":      format,
		},
	}
	if opts.Instrumental {
		body["is_instrumental"] = true
	} else {
		if strings.TrimSpace(opts.Lyrics) != "" {
			body["lyrics"] = opts.Lyrics
		}
		if opts.LyricsOptimizer || strings.TrimSpace(opts.Lyrics) == "" {
			body["lyrics_optimizer"] = true
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("minimax music: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/v1/music_generation", bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("minimax music: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("minimax music: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("minimax music: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("minimax music API %d: %s", resp.StatusCode, trimForError(data))
	}
	var result struct {
		Data struct {
			Audio    string `json:"audio"`
			AudioURL string `json:"audio_url"`
			Status   int    `json:"status"`
		} `json:"data"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("minimax music: parse response: %w", err)
	}
	if result.BaseResp.StatusCode != 0 {
		return nil, "", fmt.Errorf("minimax music API error %d: %s", result.BaseResp.StatusCode, result.BaseResp.StatusMsg)
	}
	if strings.TrimSpace(result.Data.Audio) == "" {
		return nil, "", fmt.Errorf("minimax music: empty audio data")
	}
	audio, err := hex.DecodeString(result.Data.Audio)
	if err != nil {
		return nil, "", fmt.Errorf("minimax music: decode audio hex: %w", err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("minimax music: decoded empty audio")
	}
	return audio, format, nil
}

func generatedMediaFileName(kind, format string) string {
	ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("generated-%s-%d.%s", kind, time.Now().UnixMilli(), ext)
}

func generatedMediaMime(kind, format string) string {
	ext := "." + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	switch kind {
	case "video":
		return "video/" + strings.TrimPrefix(ext, ".")
	case "music":
		if ext == ".mp3" {
			return "audio/mpeg"
		}
		return "audio/" + strings.TrimPrefix(ext, ".")
	default:
		return "application/octet-stream"
	}
}

func trimForError(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 2048 {
		s = s[:2048] + "..."
	}
	return s
}
