package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMiniMaxImageGenerator_GenerateBase64Image(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	rawImage := []byte{0x89, 'P', 'N', 'G'}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"images": []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(rawImage)},
			},
			"base_resp": map[string]any{
				"status_code": 0,
				"status_msg":  "success",
			},
		})
	}))
	defer ts.Close()

	gen := NewMiniMaxImageGenerator("sk-test", ts.URL, "image-01", ts.Client())
	gen.AspectRatio = "16:9"
	image, format, err := gen.GenerateImage(context.Background(), "watercolor fox")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if string(image) != string(rawImage) || format != "png" {
		t.Fatalf("image/format = %q/%q, want png bytes/png", image, format)
	}
	if gotPath != "/v1/image_generation" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "image-01" || gotBody["prompt"] != "watercolor fox" {
		t.Fatalf("body = %#v", gotBody)
	}
	if gotBody["response_format"] != "base64" || gotBody["n"] != float64(1) || gotBody["aspect_ratio"] != "16:9" {
		t.Fatalf("image options not sent: %#v", gotBody)
	}
}

func TestMiniMaxImageGenerator_GenerateURLImage(t *testing.T) {
	var requested []string
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/image_generation":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["response_format"] != "url" || body["width"] != float64(1024) || body["height"] != float64(768) {
				t.Fatalf("body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"image_urls": []string{ts.URL + "/generated.webp"},
				},
				"base_resp": map[string]any{"status_code": 0},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/generated.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("webp-bytes"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	gen := NewMiniMaxImageGenerator("sk-test", ts.URL, "", ts.Client())
	gen.ResponseFormat = "url"
	gen.Width = 1024
	gen.Height = 768
	gen.DownloadClient = ts.Client()

	image, format, err := gen.GenerateImage(context.Background(), "poster")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if string(image) != "webp-bytes" || format != "webp" {
		t.Fatalf("image/format = %q/%q", image, format)
	}
	got := strings.Join(requested, "\n")
	for _, want := range []string{"POST /v1/image_generation", "GET /generated.webp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("requests =\n%s\nmissing %s", got, want)
		}
	}
}

func TestOpenAIImageGenerator_GenerateBase64Image(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	rawImage := []byte{0x89, 'P', 'N', 'G'}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"b64_json": base64.StdEncoding.EncodeToString(rawImage)},
			},
		})
	}))
	defer ts.Close()

	gen := NewOpenAIImageGenerator("sk-openai", ts.URL+"/v1", "gpt-image-1", ts.Client())
	gen.Size = "1024x1024"
	gen.Quality = "high"
	gen.OutputFormat = "webp"

	image, format, err := gen.GenerateImage(context.Background(), "watercolor fox")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if string(image) != string(rawImage) || format != "webp" {
		t.Fatalf("image/format = %q/%q, want png bytes/webp", image, format)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-openai" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "gpt-image-1" || gotBody["prompt"] != "watercolor fox" || gotBody["n"] != float64(1) {
		t.Fatalf("body = %#v", gotBody)
	}
	if gotBody["size"] != "1024x1024" || gotBody["quality"] != "high" || gotBody["output_format"] != "webp" {
		t.Fatalf("openai options not sent: %#v", gotBody)
	}
	if _, ok := gotBody["response_format"]; ok {
		t.Fatalf("response_format should be omitted by default for GPT image models: %#v", gotBody)
	}
}

func TestOpenAIImageGenerator_GenerateURLImage(t *testing.T) {
	var requested []string
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["response_format"] != "url" || body["style"] != "natural" {
				t.Fatalf("body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"url": ts.URL + "/openai-image.png"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/openai-image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-bytes"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	gen := NewOpenAIImageGenerator("sk-openai", ts.URL+"/v1", "dall-e-3", ts.Client())
	gen.ResponseFormat = "url"
	gen.Style = "natural"
	gen.DownloadClient = ts.Client()

	image, format, err := gen.GenerateImage(context.Background(), "poster")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if string(image) != "png-bytes" || format != "png" {
		t.Fatalf("image/format = %q/%q", image, format)
	}
	got := strings.Join(requested, "\n")
	for _, want := range []string{"POST /v1/images/generations", "GET /openai-image.png"} {
		if !strings.Contains(got, want) {
			t.Fatalf("requests =\n%s\nmissing %s", got, want)
		}
	}
}

func TestCommandMediaGeneratorHelper(t *testing.T) {
	if os.Getenv("CC_CONNECT_COMMAND_MEDIA_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		_, _ = os.Stderr.WriteString("missing helper args")
		os.Exit(2)
	}
	args = args[1:]
	switch args[0] {
	case "file":
		if len(args) < 3 {
			_, _ = os.Stderr.WriteString("missing file helper args")
			os.Exit(2)
		}
		if err := os.WriteFile(args[1], []byte(args[2]), 0o600); err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
	case "stdout":
		if len(args) < 2 {
			_, _ = os.Stderr.WriteString("missing stdout helper args")
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(strings.Join(args[1:], "|"))
	default:
		_, _ = os.Stderr.WriteString("unknown helper mode")
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCommandMediaGenerator_GenerateImageFromOutputFile(t *testing.T) {
	gen := NewCommandMediaGenerator("image", os.Args[0], []string{
		"-test.run=TestCommandMediaGeneratorHelper",
		"--",
		"file",
		"{{output}}",
		"{{prompt}}",
	})
	gen.Env = []string{"CC_CONNECT_COMMAND_MEDIA_HELPER=1"}
	gen.Format = "webp"

	image, format, err := gen.GenerateImage(context.Background(), "command image")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if string(image) != "command image" || format != "webp" {
		t.Fatalf("image/format = %q/%q", image, format)
	}
}

func TestCommandMediaGenerator_GenerateMusicFromStdout(t *testing.T) {
	gen := NewCommandMediaGenerator("music", os.Args[0], []string{
		"-test.run=TestCommandMediaGeneratorHelper",
		"--",
		"stdout",
		"{{prompt}}",
		"{{lyrics}}",
		"{{instrumental}}",
	})
	gen.Env = []string{"CC_CONNECT_COMMAND_MEDIA_HELPER=1"}
	gen.Format = "wav"

	audio, format, err := gen.GenerateMusic(context.Background(), "suno song", MusicGenerationOpts{
		Lyrics:       "[Verse] hello",
		Instrumental: true,
	})
	if err != nil {
		t.Fatalf("GenerateMusic() error = %v", err)
	}
	if string(audio) != "suno song|[Verse] hello|true" || format != "wav" {
		t.Fatalf("audio/format = %q/%q", audio, format)
	}
}

func TestNormalizeMediaFormatRejectsPathSeparators(t *testing.T) {
	if got := normalizeMediaFormat("../mp4", "video"); got != "mp4" {
		t.Fatalf("normalizeMediaFormat() = %q, want mp4", got)
	}
	if got := normalizeMediaFormat("webp", "image"); got != "webp" {
		t.Fatalf("normalizeMediaFormat() = %q, want webp", got)
	}
}

func TestMiniMaxMusicGenerator_GenerateHexAudio(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"audio":  "6d7033",
				"status": 2,
			},
			"base_resp": map[string]any{
				"status_code": 0,
				"status_msg":  "success",
			},
		})
	}))
	defer ts.Close()

	gen := NewMiniMaxMusicGenerator("sk-test", ts.URL, "music-2.6-free", ts.Client())
	audio, format, err := gen.GenerateMusic(context.Background(), "ambient piano", MusicGenerationOpts{
		LyricsOptimizer: true,
		Instrumental:    true,
	})
	if err != nil {
		t.Fatalf("GenerateMusic() error = %v", err)
	}
	if string(audio) != "mp3" || format != "mp3" {
		t.Fatalf("audio/format = %q/%q, want mp3/mp3", audio, format)
	}
	if gotPath != "/v1/music_generation" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "music-2.6-free" || gotBody["prompt"] != "ambient piano" {
		t.Fatalf("body = %#v", gotBody)
	}
	if gotBody["is_instrumental"] != true {
		t.Fatalf("music flags not sent: %#v", gotBody)
	}
	if gotBody["lyrics_optimizer"] != nil {
		t.Fatalf("lyrics_optimizer should not be sent for instrumental music: %#v", gotBody)
	}
}

func TestMiniMaxMusicGenerator_PromptOnlyEnablesLyricsOptimizer(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"audio":  "6d7033",
				"status": 2,
			},
			"base_resp": map[string]any{
				"status_code": 0,
				"status_msg":  "success",
			},
		})
	}))
	defer ts.Close()

	gen := NewMiniMaxMusicGenerator("sk-test", ts.URL, "", ts.Client())
	if _, _, err := gen.GenerateMusic(context.Background(), "upbeat pop chorus", MusicGenerationOpts{}); err != nil {
		t.Fatalf("GenerateMusic() error = %v", err)
	}
	if gotBody["lyrics_optimizer"] != true {
		t.Fatalf("lyrics_optimizer not auto-enabled for prompt-only song: %#v", gotBody)
	}
	if gotBody["is_instrumental"] != nil {
		t.Fatalf("is_instrumental should not be sent by default: %#v", gotBody)
	}
}

func TestMiniMaxVideoGenerator_GeneratePollsAndDownloads(t *testing.T) {
	var requested []string
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/video_generation":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if body["prompt"] != "cinematic sunset" || body["model"] != "MiniMax-Hailuo-2.3" {
				t.Fatalf("create body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task-1", "base_resp": map[string]any{"status_code": 0}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/query/video_generation":
			if r.URL.Query().Get("task_id") != "task-1" {
				t.Fatalf("task_id = %q", r.URL.Query().Get("task_id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "Success", "file_id": "file-1", "base_resp": map[string]any{"status_code": 0}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/files/retrieve":
			if r.URL.Query().Get("file_id") != "file-1" {
				t.Fatalf("file_id = %q", r.URL.Query().Get("file_id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"download_url": ts.URL + "/download.mp4"}, "base_resp": map[string]any{"status_code": 0}})
		case r.Method == http.MethodGet && r.URL.Path == "/download.mp4":
			_, _ = w.Write([]byte("video-bytes"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	gen := NewMiniMaxVideoGenerator("sk-test", ts.URL, "", ts.Client())
	gen.PollInterval = time.Millisecond
	gen.Timeout = time.Second
	gen.DownloadClient = ts.Client()

	video, format, err := gen.GenerateVideo(context.Background(), "cinematic sunset")
	if err != nil {
		t.Fatalf("GenerateVideo() error = %v", err)
	}
	if string(video) != "video-bytes" || format != "mp4" {
		t.Fatalf("video/format = %q/%q", video, format)
	}
	got := strings.Join(requested, "\n")
	for _, want := range []string{"POST /v1/video_generation", "GET /v1/query/video_generation?task_id=task-1", "GET /v1/files/retrieve?file_id=file-1", "GET /download.mp4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("requests =\n%s\nmissing %s", got, want)
		}
	}
}
