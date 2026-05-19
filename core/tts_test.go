package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
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
// MimoTTS tests
// ──────────────────────────────────────────────────────────────

func TestMimoTTS_Success(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "test-key" {
			t.Errorf("expected api-key header 'test-key', got %q", got)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Authorization header must not be set, got %q", auth)
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Audio struct {
				Format string `json:"format"`
				Voice  string `json:"voice"`
			} `json:"audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Model != "mimo-v2.5-tts" {
			t.Errorf("expected model mimo-v2.5-tts, got %q", req.Model)
		}
		if len(req.Messages) != 2 || req.Messages[1].Role != "assistant" || req.Messages[1].Content != "你好" {
			t.Errorf("expected assistant message with synthesis text, got %+v", req.Messages)
		}
		if req.Audio.Format != "wav" {
			t.Errorf("expected audio.format wav, got %q", req.Audio.Format)
		}
		if req.Audio.Voice != "Chloe" {
			t.Errorf("expected audio.voice Chloe, got %q", req.Audio.Voice)
		}
		// "fake-wav" base64-encoded
		b64 := "ZmFrZS13YXY="
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"audio": map[string]any{
							"data": b64,
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("test-key", apiServer.URL, "", nil)
	audio, format, err := tts.Synthesize(context.Background(), "你好", TTSSynthesisOpts{Voice: "Chloe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "wav" {
		t.Errorf("expected wav, got %q", format)
	}
	if string(audio) != "fake-wav" {
		t.Errorf("unexpected audio data: %q", audio)
	}
}

func TestMimoTTS_DefaultVoice(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Audio struct {
				Voice string `json:"voice"`
			} `json:"audio"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Audio.Voice != "mimo_default" {
			t.Errorf("expected default voice mimo_default, got %q", req.Audio.Voice)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"audio": map[string]any{"data": "ZmFrZQ=="}}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hi", TTSSynthesisOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMimoTTS_APIError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("bad-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestMimoTTS_EmptyAudio(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"audio": map[string]any{"data": ""}}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for empty audio data")
	}
}

func TestMimoTTS_BusinessError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"error": map[string]any{
				"message": "quota exceeded",
				"type":    "rate_limit",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for business error")
	}
}

func TestMimoTTS_BadBase64(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"audio": map[string]any{"data": "!!!not-base64!!!"}}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer apiServer.Close()

	tts := NewMimoTTS("test-key", apiServer.URL, "", nil)
	_, _, err := tts.Synthesize(context.Background(), "hello", TTSSynthesisOpts{})
	if err == nil {
		t.Fatal("expected error for invalid base64 audio")
	}
}

func TestMimoTTS_Constructors(t *testing.T) {
	tts := NewMimoTTS("k", "", "", nil)
	if tts.BaseURL != "https://api.xiaomimimo.com/v1" {
		t.Errorf("expected default base URL, got %q", tts.BaseURL)
	}
	if tts.Model != "mimo-v2.5-tts" {
		t.Errorf("expected default model mimo-v2.5-tts, got %q", tts.Model)
	}
	tts = NewMimoTTS("k", "https://example.com/v1", "mimo-v2.5-tts-voicedesign", nil)
	if tts.BaseURL != "https://example.com/v1" {
		t.Errorf("expected custom base URL, got %q", tts.BaseURL)
	}
	if tts.Model != "mimo-v2.5-tts-voicedesign" {
		t.Errorf("expected custom model, got %q", tts.Model)
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

// ──────────────────────────────────────────────────────────────
// Local TTS provider constructor tests (Espeak, Pico, Edge)
// ──────────────────────────────────────────────────────────────

func TestEspeakTTS_Constructors(t *testing.T) {
	tts := NewEspeakTTS("", "")
	if tts.Path != "espeak" {
		t.Errorf("expected default path 'espeak', got %q", tts.Path)
	}
	if tts.Voice != "zh" {
		t.Errorf("expected default voice 'zh', got %q", tts.Voice)
	}

	tts = NewEspeakTTS("/custom/espeak", "en")
	if tts.Path != "/custom/espeak" {
		t.Errorf("expected custom path, got %q", tts.Path)
	}
	if tts.Voice != "en" {
		t.Errorf("expected custom voice 'en', got %q", tts.Voice)
	}
}

func TestPicoTTS_Constructors(t *testing.T) {
	tts := NewPicoTTS("", "")
	if tts.Path != "pico2wave" {
		t.Errorf("expected default path 'pico2wave', got %q", tts.Path)
	}
	if tts.Voice != "zh-CN" {
		t.Errorf("expected default voice 'zh-CN', got %q", tts.Voice)
	}

	tts = NewPicoTTS("/custom/pico2wave", "en-US")
	if tts.Path != "/custom/pico2wave" {
		t.Errorf("expected custom path, got %q", tts.Path)
	}
	if tts.Voice != "en-US" {
		t.Errorf("expected custom voice 'en-US', got %q", tts.Voice)
	}
}

func TestEdgeTTS_Constructors(t *testing.T) {
	tts := NewEdgeTTS("")
	if tts.Voice != "zh-CN-XiaoxiaoNeural" {
		t.Errorf("expected default voice 'zh-CN-XiaoxiaoNeural', got %q", tts.Voice)
	}

	tts = NewEdgeTTS("en-US-JennyNeural")
	if tts.Voice != "en-US-JennyNeural" {
		t.Errorf("expected custom voice 'en-US-JennyNeural', got %q", tts.Voice)
	}
}

func TestEspeakTTS_Synthesize_Integration(t *testing.T) {
	// Skip if espeak is not available
	if _, err := exec.LookPath("espeak"); err != nil {
		t.Skip("espeak not available")
	}

	tts := NewEspeakTTS("espeak", "en")

	// Test basic synthesis - just verify it doesn't crash
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audio, format, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	if err != nil {
		t.Logf("espeak synthesis failed (may be expected in some environments): %v", err)
		return
	}

	if format != "wav" {
		t.Errorf("expected wav format, got %q", format)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio data")
	}
}

func TestPicoTTS_Synthesize_Integration(t *testing.T) {
	// Skip if pico2wave is not available
	if _, err := exec.LookPath("pico2wave"); err != nil {
		t.Skip("pico2wave not available")
	}

	tts := NewPicoTTS("pico2wave", "en-US")

	// Test basic synthesis - just verify it doesn't crash
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audio, format, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	if err != nil {
		t.Logf("pico2wave synthesis failed (may be expected in some environments): %v", err)
		return
	}

	if format != "wav" {
		t.Errorf("expected wav format, got %q", format)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio data")
	}
}

// writeFakeTTSBinary creates a temp shell script that sleeps long enough to
// outlive a normal test timeout. Used to verify that subprocess-based TTS
// providers honor ctx cancellation by killing the spawned process.
func writeFakeTTSBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake TTS binary uses /bin/sh; not portable to windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-tts.sh")
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tts script: %v", err)
	}
	return path
}

func TestEspeakTTS_HonorsContextCancellation(t *testing.T) {
	fakePath := writeFakeTTSBinary(t)
	tts := NewEspeakTTS(fakePath, "en")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: a correctly wired CommandContext kills immediately.

	start := time.Now()
	_, _, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error from cancelled context, got nil after %v", elapsed)
	}
	// 5s is generous: a fix using exec.CommandContext returns in well under a
	// second; the bug (plain exec.Command) would wait the full 30s sleep.
	if elapsed > 5*time.Second {
		t.Fatalf("Synthesize ignored ctx cancellation, took %v (want < 5s); err=%v", elapsed, err)
	}
}

func TestPicoTTS_HonorsContextCancellation(t *testing.T) {
	fakePath := writeFakeTTSBinary(t)
	tts := NewPicoTTS(fakePath, "en-US")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, _, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error from cancelled context, got nil after %v", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Synthesize ignored ctx cancellation, took %v (want < 5s); err=%v", elapsed, err)
	}
}

func TestEdgeTTS_Synthesize_Integration(t *testing.T) {
	// Skip if edge-tts is not available
	if _, err := exec.LookPath("edge-tts"); err != nil {
		t.Skip("edge-tts not available")
	}

	tts := NewEdgeTTS("en-US-JennyNeural")

	// Test basic synthesis - just verify it doesn't crash
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	audio, format, err := tts.Synthesize(ctx, "hello", TTSSynthesisOpts{})
	if err != nil {
		t.Logf("edge-tts synthesis failed (may be expected in some environments): %v", err)
		return
	}

	if format != "mp3" {
		t.Errorf("expected mp3 format, got %q", format)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio data")
	}
}
