package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
