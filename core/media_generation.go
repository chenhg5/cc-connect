package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ImageGenerator generates an image attachment from a text prompt.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, prompt string) (data []byte, format string, err error)
}

// VideoGenerator generates a video attachment from a text prompt.
type VideoGenerator interface {
	GenerateVideo(ctx context.Context, prompt string) (data []byte, format string, err error)
}

// MusicGenerator generates an audio/music attachment from a prompt and options.
type MusicGenerator interface {
	GenerateMusic(ctx context.Context, prompt string, opts MusicGenerationOpts) (data []byte, format string, err error)
}

// ImageGenerationCfg holds image generation configuration for the engine.
type ImageGenerationCfg struct {
	Enabled      bool
	Provider     string
	Generator    ImageGenerator
	MaxPromptLen int
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

// CommandMediaGenerator runs a configured local command to produce media bytes.
// It is intentionally provider-neutral: the command can be OpenClaw, a vendor
// CLI, or a small wrapper around any async media API. Commands are executed
// directly without a shell, and may either write to {{output}} or emit bytes on
// stdout.
type CommandMediaGenerator struct {
	Kind    string
	Command string
	Args    []string
	WorkDir string
	Env     []string
	Format  string
	Timeout time.Duration
}

// MiniMaxImageGenerator implements MiniMax text-to-image generation.
type MiniMaxImageGenerator struct {
	APIKey          string
	BaseURL         string
	Model           string
	ResponseFormat  string
	AspectRatio     string
	Width           int
	Height          int
	PromptOptimizer bool
	Client          *http.Client
	DownloadClient  *http.Client
}

// OpenAIImageGenerator implements the OpenAI-compatible Images API.
type OpenAIImageGenerator struct {
	APIKey         string
	BaseURL        string
	Model          string
	Size           string
	Quality        string
	ResponseFormat string
	OutputFormat   string
	Background     string
	Style          string
	Client         *http.Client
	DownloadClient *http.Client
}

func NewCommandMediaGenerator(kind, command string, args []string) *CommandMediaGenerator {
	return &CommandMediaGenerator{
		Kind:    kind,
		Command: command,
		Args:    append([]string(nil), args...),
		Format:  defaultGeneratedMediaFormat(kind),
		Timeout: 10 * time.Minute,
	}
}

func (g *CommandMediaGenerator) GenerateImage(ctx context.Context, prompt string) ([]byte, string, error) {
	return g.generate(ctx, prompt, nil)
}

func (g *CommandMediaGenerator) GenerateVideo(ctx context.Context, prompt string) ([]byte, string, error) {
	return g.generate(ctx, prompt, nil)
}

func (g *CommandMediaGenerator) GenerateMusic(ctx context.Context, prompt string, opts MusicGenerationOpts) ([]byte, string, error) {
	values := map[string]string{
		"lyrics":       opts.Lyrics,
		"instrumental": fmt.Sprintf("%t", opts.Instrumental),
	}
	return g.generate(ctx, prompt, values)
}

func (g *CommandMediaGenerator) generate(ctx context.Context, prompt string, values map[string]string) ([]byte, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("%s prompt is required", g.kindForError())
	}
	command := strings.TrimSpace(g.Command)
	if command == "" {
		return nil, "", fmt.Errorf("%s command is required", g.kindForError())
	}
	format := normalizeMediaFormat(g.Format, g.Kind)
	tmpDir, err := os.MkdirTemp("", "cc-connect-media-*")
	if err != nil {
		return nil, "", fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	outputPath := filepath.Join(tmpDir, "output."+format)
	replacements := map[string]string{
		"prompt": prompt,
		"output": outputPath,
		"format": format,
	}
	for k, v := range values {
		replacements[k] = v
	}
	args, usesOutput := replaceCommandMediaArgs(g.Args, replacements)
	runCtx := ctx
	cancel := func() {}
	if g.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, g.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, args...)
	if strings.TrimSpace(g.WorkDir) != "" {
		cmd.Dir = g.WorkDir
	}
	if len(g.Env) > 0 {
		cmd.Env = append(os.Environ(), g.Env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return nil, "", fmt.Errorf("%s command timed out: %w", g.kindForError(), runCtx.Err())
		}
		return nil, "", fmt.Errorf("%s command failed: %w: %s", g.kindForError(), err, strings.TrimSpace(stderr.String()))
	}
	var data []byte
	if usesOutput {
		data, err = os.ReadFile(outputPath)
		if err != nil {
			return nil, "", fmt.Errorf("read command output file: %w", err)
		}
	} else {
		data = stdout.Bytes()
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("%s command returned empty media", g.kindForError())
	}
	return data, format, nil
}

func (g *CommandMediaGenerator) kindForError() string {
	if strings.TrimSpace(g.Kind) == "" {
		return "media"
	}
	return strings.TrimSpace(g.Kind)
}

func replaceCommandMediaArgs(args []string, values map[string]string) ([]string, bool) {
	out := make([]string, 0, len(args))
	usesOutput := false
	for _, arg := range args {
		replaced := arg
		for k, v := range values {
			token := "{{" + k + "}}"
			if strings.Contains(replaced, token) && k == "output" {
				usesOutput = true
			}
			replaced = strings.ReplaceAll(replaced, token, v)
		}
		out = append(out, replaced)
	}
	return out, usesOutput
}

func NewMiniMaxImageGenerator(apiKey, baseURL, model string, client *http.Client) *MiniMaxImageGenerator {
	if baseURL == "" {
		baseURL = "https://api.minimaxi.com"
	}
	if model == "" {
		model = "image-01"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &MiniMaxImageGenerator{
		APIKey:         apiKey,
		BaseURL:        strings.TrimRight(baseURL, "/"),
		Model:          model,
		ResponseFormat: "base64",
		Client:         client,
		DownloadClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

func NewOpenAIImageGenerator(apiKey, baseURL, model string, client *http.Client) *OpenAIImageGenerator {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-image-1"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &OpenAIImageGenerator{
		APIKey:         apiKey,
		BaseURL:        strings.TrimRight(baseURL, "/"),
		Model:          model,
		Client:         client,
		DownloadClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (o *OpenAIImageGenerator) GenerateImage(ctx context.Context, prompt string) ([]byte, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("image prompt is required")
	}
	body := map[string]any{
		"model":  o.Model,
		"prompt": prompt,
		"n":      1,
	}
	if strings.TrimSpace(o.Size) != "" {
		body["size"] = strings.TrimSpace(o.Size)
	}
	if strings.TrimSpace(o.Quality) != "" {
		body["quality"] = strings.TrimSpace(o.Quality)
	}
	if strings.TrimSpace(o.ResponseFormat) != "" {
		body["response_format"] = strings.TrimSpace(o.ResponseFormat)
	}
	if strings.TrimSpace(o.OutputFormat) != "" {
		body["output_format"] = strings.TrimSpace(o.OutputFormat)
	}
	if strings.TrimSpace(o.Background) != "" {
		body["background"] = strings.TrimSpace(o.Background)
	}
	if strings.TrimSpace(o.Style) != "" {
		body["style"] = strings.TrimSpace(o.Style)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("openai image: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/images/generations", bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("openai image: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("openai image: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("openai image: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("openai image API %d: %s", resp.StatusCode, trimForError(data))
	}
	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("openai image: parse response: %w", err)
	}
	if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
		return nil, "", fmt.Errorf("openai image API error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 {
		return nil, "", fmt.Errorf("openai image: empty image data")
	}
	outputFormat := normalizeImageFormat(o.OutputFormat)
	if strings.TrimSpace(result.Data[0].B64JSON) != "" {
		img, format, err := decodeImageBase64(result.Data[0].B64JSON)
		if err != nil {
			return nil, "", fmt.Errorf("openai image: decode image base64: %w", err)
		}
		if outputFormat != "" {
			format = outputFormat
		}
		return img, format, nil
	}
	if strings.TrimSpace(result.Data[0].URL) != "" {
		client := o.DownloadClient
		if client == nil {
			client = o.Client
		}
		img, format, err := downloadGeneratedImage(ctx, client, result.Data[0].URL)
		if err != nil {
			return nil, "", fmt.Errorf("openai image: download image: %w", err)
		}
		return img, format, nil
	}
	return nil, "", fmt.Errorf("openai image: empty image data")
}

func (m *MiniMaxImageGenerator) GenerateImage(ctx context.Context, prompt string) ([]byte, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("image prompt is required")
	}
	responseFormat := strings.TrimSpace(m.ResponseFormat)
	if responseFormat == "" {
		responseFormat = "base64"
	}
	body := map[string]any{
		"model":           m.Model,
		"prompt":          prompt,
		"response_format": responseFormat,
		"n":               1,
	}
	if strings.TrimSpace(m.AspectRatio) != "" {
		body["aspect_ratio"] = strings.TrimSpace(m.AspectRatio)
	} else if m.Width > 0 && m.Height > 0 {
		body["width"] = m.Width
		body["height"] = m.Height
	}
	if m.PromptOptimizer {
		body["prompt_optimizer"] = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("minimax image: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/v1/image_generation", bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("minimax image: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("minimax image: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("minimax image: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("minimax image API %d: %s", resp.StatusCode, trimForError(data))
	}
	var result struct {
		Data struct {
			ImageURLs   []string `json:"image_urls"`
			Images      []string `json:"images"`
			Image       string   `json:"image"`
			ImageBase64 string   `json:"image_base64"`
			URL         string   `json:"url"`
		} `json:"data"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("minimax image: parse response: %w", err)
	}
	if result.BaseResp.StatusCode != 0 {
		return nil, "", fmt.Errorf("minimax image API error %d: %s", result.BaseResp.StatusCode, result.BaseResp.StatusMsg)
	}
	if len(result.Data.Images) > 0 && strings.TrimSpace(result.Data.Images[0]) != "" {
		img, format, err := decodeImageBase64(result.Data.Images[0])
		if err != nil {
			return nil, "", fmt.Errorf("minimax image: decode image base64: %w", err)
		}
		return img, format, nil
	}
	for _, raw := range []string{result.Data.Image, result.Data.ImageBase64} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		img, format, err := decodeImageBase64(raw)
		if err != nil {
			return nil, "", fmt.Errorf("minimax image: decode image base64: %w", err)
		}
		return img, format, nil
	}
	if len(result.Data.ImageURLs) > 0 && strings.TrimSpace(result.Data.ImageURLs[0]) != "" {
		img, format, err := m.downloadImage(ctx, result.Data.ImageURLs[0])
		if err != nil {
			return nil, "", fmt.Errorf("minimax image: download image: %w", err)
		}
		return img, format, nil
	}
	if strings.TrimSpace(result.Data.URL) != "" {
		img, format, err := m.downloadImage(ctx, result.Data.URL)
		if err != nil {
			return nil, "", fmt.Errorf("minimax image: download image: %w", err)
		}
		return img, format, nil
	}
	return nil, "", fmt.Errorf("minimax image: empty image data")
}

func (m *MiniMaxImageGenerator) downloadImage(ctx context.Context, downloadURL string) ([]byte, string, error) {
	client := m.DownloadClient
	if client == nil {
		client = m.Client
	}
	return downloadGeneratedImage(ctx, client, downloadURL)
}

func downloadGeneratedImage(ctx context.Context, client *http.Client, downloadURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read download: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download API %d: %s", resp.StatusCode, trimForError(data))
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("download returned empty image")
	}
	format := imageFormatFromMIME(resp.Header.Get("Content-Type"))
	if format == "" {
		if u, err := url.Parse(downloadURL); err == nil {
			format = strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
		}
	}
	if format == "" {
		format = "png"
	}
	return data, format, nil
}

func decodeImageBase64(raw string) ([]byte, string, error) {
	format := "png"
	value := strings.TrimSpace(raw)
	if i := strings.Index(value, ","); strings.HasPrefix(value, "data:") && i >= 0 {
		format = imageFormatFromMIME(value[len("data:"):i])
		value = value[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("decoded empty image")
	}
	if format == "" {
		format = "png"
	}
	return data, format, nil
}

func imageFormatFromMIME(mimeType string) string {
	mt := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func normalizeImageFormat(format string) string {
	f := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	switch f {
	case "jpeg":
		return "jpg"
	case "jpg", "png", "webp", "gif":
		return f
	default:
		return ""
	}
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

func defaultGeneratedMediaFormat(kind string) string {
	switch kind {
	case "image":
		return "png"
	case "video":
		return "mp4"
	case "music":
		return "mp3"
	default:
		return "bin"
	}
}

func normalizeMediaFormat(format, kind string) string {
	f := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	if f == "" || !isSafeMediaFormat(f) {
		return defaultGeneratedMediaFormat(kind)
	}
	return f
}

func isSafeMediaFormat(format string) bool {
	for _, r := range format {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func generatedMediaMime(kind, format string) string {
	ext := "." + strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	switch kind {
	case "image":
		if ext == ".jpg" || ext == ".jpeg" {
			return "image/jpeg"
		}
		return "image/" + strings.TrimPrefix(ext, ".")
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
