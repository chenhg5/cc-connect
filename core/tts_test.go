package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ──────────────────────────────────────────────────────────────
// TTSCfg concurrency tests
// ──────────────────────────────────────────────────────────────

func TestTTSCfg_GetSetMode(t *testing.T) {
	cfg := TTSCfg{}
	// default when empty
	if got := cfg.GetTTSMode(); got != "voice_only" {
		t.Errorf("expected default voice_only, got %q", got)
	}
	cfg.SetTTSMode("always")
	if got := cfg.GetTTSMode(); got != "always" {
		t.Errorf("expected always, got %q", got)
	}
	cfg.SetTTSMode("voice_only")
	if got := cfg.GetTTSMode(); got != "voice_only" {
		t.Errorf("expected voice_only, got %q", got)
	}
}

func TestTTSCfg_ConcurrentGetSet(t *testing.T) {
	cfg := TTSCfg{}
	cfg.SetTTSMode("voice_only")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			cfg.SetTTSMode("always")
		}()
		go func() {
			defer wg.Done()
			_ = cfg.GetTTSMode()
		}()
	}
	wg.Wait()
}

// ──────────────────────────────────────────────────────────────
// QwenTTS tests
// ──────────────────────────────────────────────────────────────

func TestQwenTTS_Success(t *testing.T) {
	// Stub: returns audio URL
	audioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-wav-data"))
	}))
	defer audioServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"audio": map[string]any{
					"url": audioServer.URL + "/audio.wav",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewQwenTTS("test-key", apiServer.URL, "qwen3-tts-flash", nil)
	audio, format, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "wav" {
		t.Errorf("expected wav, got %q", format)
	}
	if string(audio) != "fake-wav-data" {
		t.Errorf("unexpected audio data: %q", audio)
	}
}

func TestQwenTTS_APIError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer apiServer.Close()

	tts := NewQwenTTS("bad-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestQwenTTS_BusinessErrorCode(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code":    "InvalidApiKey",
			"message": "api key is invalid",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewQwenTTS("bad-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for business error code")
	}
}

func TestQwenTTS_EmptyAudioURL(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"audio": map[string]any{
					"url": "",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewQwenTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for empty audio URL")
	}
}

func TestQwenTTS_AudioDownloadFailed(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"audio": map[string]any{
					"url": "http://127.0.0.1:1/nonexistent.wav",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewQwenTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error when audio download fails")
	}
}

// ──────────────────────────────────────────────────────────────
// OpenAITTS tests
// ──────────────────────────────────────────────────────────────

func TestOpenAITTS_Success(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fake-mp3-data"))
	}))
	defer apiServer.Close()

	tts := NewOpenAITTS("test-key", apiServer.URL, "tts-1", nil)
	audio, format, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{Voice: "alloy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "mp3" {
		t.Errorf("expected mp3, got %q", format)
	}
	if string(audio) != "fake-mp3-data" {
		t.Errorf("unexpected audio data: %q", audio)
	}
}

func TestOpenAITTS_APIError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer apiServer.Close()

	tts := NewOpenAITTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

// ──────────────────────────────────────────────────────────────
// MiniMaxTTS tests
// ──────────────────────────────────────────────────────────────

func TestMiniMaxTTS_Success(t *testing.T) {
	// Stub SSE server returning hex-encoded audio chunks
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/t2a_v2" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// "fake-mp3" hex-encoded
		hexAudio := "66616b652d6d7033"
		chunk := map[string]any{
			"data":      map[string]any{"audio": hexAudio, "status": 1},
			"base_resp": map[string]any{"status_code": 0, "status_msg": "success"},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data:%s\n\n", data)
		// Final chunk with status 2
		finalChunk := map[string]any{
			"data":      map[string]any{"audio": "", "status": 2},
			"base_resp": map[string]any{"status_code": 0, "status_msg": "success"},
		}
		finalData, _ := json.Marshal(finalChunk)
		fmt.Fprintf(w, "data:%s\n\n", finalData)
	}))
	defer apiServer.Close()

	tts := NewMiniMaxTTS("test-key", apiServer.URL, "speech-2.8-hd", nil)
	audio, format, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "mp3" {
		t.Errorf("expected mp3, got %q", format)
	}
	if string(audio) != "fake-mp3" {
		t.Errorf("unexpected audio data: %q", audio)
	}
}

func TestMiniMaxTTS_APIError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer apiServer.Close()

	tts := NewMiniMaxTTS("bad-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestMiniMaxTTS_BusinessError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{
			"data":      map[string]any{"audio": "", "status": 0},
			"base_resp": map[string]any{"status_code": 1001, "status_msg": "invalid api key"},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data:%s\n\n", data)
	}))
	defer apiServer.Close()

	tts := NewMiniMaxTTS("bad-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for business error code")
	}
}

func TestMiniMaxTTS_EmptyAudio(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{
			"data":      map[string]any{"audio": "", "status": 2},
			"base_resp": map[string]any{"status_code": 0, "status_msg": "success"},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data:%s\n\n", data)
	}))
	defer apiServer.Close()

	tts := NewMiniMaxTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for empty audio data")
	}
}

// ──────────────────────────────────────────────────────────────
// MaxTextLen skip test (via TTSCfg)
// ──────────────────────────────────────────────────────────────

func TestTTSCfg_MaxTextLen(t *testing.T) {
	cfg := TTSCfg{
		Enabled:    true,
		MaxTextLen: 5,
	}
	// 6 runes — should exceed limit
	text := "你好世界！！"
	runeLen := len([]rune(text))
	if runeLen <= cfg.MaxTextLen {
		t.Fatalf("test setup error: %d <= %d", runeLen, cfg.MaxTextLen)
	}
	// MaxTextLen check logic (mirrors sendTTSReply)
	exceeded := cfg.MaxTextLen > 0 && runeLen > cfg.MaxTextLen
	if !exceeded {
		t.Error("expected text to exceed MaxTextLen")
	}
}

// ──────────────────────────────────────────────────────────────
// Context cancellation test
// ──────────────────────────────────────────────────────────────

func TestMiniMaxTTS_ContextCancelled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("server does not support flushing")
		}
		// Send one chunk then hang to let the client cancel
		chunk := `{"data":{"audio":"48656c6c6f","status":1},"base_resp":{"status_code":0}}`
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
		// Block until client disconnects
		<-r.Context().Done()
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	tts := NewMiniMaxTTS("test-key", srv.URL, "", nil)
	_, _, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}
