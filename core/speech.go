package core

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
	"time"
)

// SpeechToText transcribes audio to text.
type SpeechToText interface {
	Transcribe(ctx context.Context, audio []byte, format string, lang string) (string, error)
}

// SpeechConfig holds STT configuration for the engine.
type SpeechCfg struct {
	Enabled  bool
	Provider string
	Language string
	STT      SpeechToText
}

// OpenAIWhisper implements SpeechToText using the OpenAI-compatible Whisper API.
// Works with OpenAI, Groq, and any endpoint that implements the same multipart API.
type OpenAIWhisper struct {
	APIKey  string
	BaseURL string
	Model   string
	Client  *http.Client
}

func NewOpenAIWhisper(apiKey, baseURL, model string) *OpenAIWhisper {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "whisper-1"
	}
	return &OpenAIWhisper{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		Client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (w *OpenAIWhisper) Transcribe(ctx context.Context, audio []byte, format string, lang string) (string, error) {
	ext := formatToExt(format)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", "audio."+ext)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("write audio: %w", err)
	}
	_ = writer.WriteField("model", w.Model)
	_ = writer.WriteField("response_format", "text")
	if lang != "" {
		_ = writer.WriteField("language", lang)
	}
	writer.Close()

	url := w.BaseURL + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := w.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper API %d: %s", resp.StatusCode, string(body))
	}

	// response_format=text returns plain text; try to handle JSON fallback
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "{") {
		var jr struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(body, &jr) == nil {
			text = jr.Text
		}
	}
	return text, nil
}

// ConvertAudioToMP3 uses ffmpeg to convert audio from unsupported formats to mp3.
// Returns the mp3 bytes. If ffmpeg is not installed, returns an error.
func ConvertAudioToMP3(audio []byte, srcFormat string) ([]byte, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH: install ffmpeg to enable voice message support")
	}

	cmd := exec.Command(ffmpegPath,
		"-i", "pipe:0",
		"-f", srcFormat,
		"-f", "mp3",
		"-ac", "1",
		"-ar", "16000",
		"-y",
		"pipe:1",
	)
	// For formats where ffmpeg can't auto-detect from pipe, specify input format
	if srcFormat == "amr" || srcFormat == "silk" {
		cmd = exec.Command(ffmpegPath,
			"-f", srcFormat,
			"-i", "pipe:0",
			"-f", "mp3",
			"-ac", "1",
			"-ar", "16000",
			"-y",
			"pipe:1",
		)
	} else {
		cmd = exec.Command(ffmpegPath,
			"-i", "pipe:0",
			"-f", "mp3",
			"-ac", "1",
			"-ar", "16000",
			"-y",
			"pipe:1",
		)
	}

	cmd.Stdin = bytes.NewReader(audio)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg conversion failed: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// NeedsConversion returns true if the audio format is not directly supported by Whisper API.
func NeedsConversion(format string) bool {
	switch strings.ToLower(format) {
	case "mp3", "mp4", "mpeg", "mpga", "m4a", "wav", "webm":
		return false
	default:
		return true
	}
}

// HasFFmpeg checks if ffmpeg is available.
func HasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func formatToExt(format string) string {
	switch strings.ToLower(format) {
	case "amr":
		return "amr"
	case "ogg", "oga", "opus":
		return "ogg"
	case "m4a", "mp4", "aac":
		return "m4a"
	case "mp3":
		return "mp3"
	case "wav":
		return "wav"
	case "webm":
		return "webm"
	case "silk":
		return "silk"
	default:
		return format
	}
}

// TranscribeAudio is a convenience function used by the Engine.
// It handles format conversion (if needed) and calls the STT provider.
func TranscribeAudio(ctx context.Context, stt SpeechToText, audio *AudioAttachment, lang string) (string, error) {
	data := audio.Data
	format := strings.ToLower(audio.Format)

	if NeedsConversion(format) {
		slog.Debug("speech: converting audio", "from", format, "to", "mp3")
		converted, err := ConvertAudioToMP3(data, format)
		if err != nil {
			return "", err
		}
		data = converted
		format = "mp3"
	}

	slog.Debug("speech: transcribing", "format", format, "size", len(data))
	return stt.Transcribe(ctx, data, format, lang)
}
