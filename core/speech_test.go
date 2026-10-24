package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestNewGeminiSTT_DefaultModel(t *testing.T) {
	g := NewGeminiSTT("test-key", "")
	if g.Model != "gemini-flash-latest" {
		t.Errorf("expected default model gemini-flash-latest, got %s", g.Model)
	}
	if g.BaseURL != "https://generativelanguage.googleapis.com/v1beta" {
		t.Errorf("unexpected base URL: %s", g.BaseURL)
	}
}

func TestNewGeminiSTT_CustomModel(t *testing.T) {
	g := NewGeminiSTT("test-key", "gemini-2.5-flash")
	if g.Model != "gemini-2.5-flash" {
		t.Errorf("expected gemini-2.5-flash, got %s", g.Model)
	}
}

func TestGeminiSTT_Transcribe_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("expected x-goog-api-key 'test-key', got %q", r.Header.Get("x-goog-api-key"))
		}
		if r.URL.Query().Get("key") != "" {
			t.Errorf("expected API key not in query string")
		}
		if !strings.Contains(r.URL.Path, "test-model") {
			t.Errorf("expected model in path, got %s", r.URL.Path)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		contents, ok := body["contents"].([]any)
		if !ok || len(contents) == 0 {
			t.Fatal("missing contents in request")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": "你好世界"},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	g := &GeminiSTT{
		APIKey:  "test-key",
		Model:   "test-model",
		BaseURL: server.URL,
		Client:  server.Client(),
	}

	text, err := g.Transcribe(context.Background(), []byte("fake-audio"), "mp3", "zh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "你好世界" {
		t.Errorf("expected '你好世界', got %q", text)
	}
}

func TestGeminiSTT_Transcribe_WithLanguage(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": "hello"}}}},
			},
		})
	}))
	defer server.Close()

	g := &GeminiSTT{APIKey: "k", Model: "m", BaseURL: server.URL, Client: server.Client()}
	_, err := g.Transcribe(context.Background(), []byte("audio"), "mp3", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the prompt includes language
	contents := gotBody["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	textPart := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(textPart, "en") {
		t.Errorf("expected language 'en' in prompt, got %q", textPart)
	}
}

func TestGeminiSTT_Transcribe_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"API key invalid","code":403}}`))
	}))
	defer server.Close()

	g := &GeminiSTT{APIKey: "bad", Model: "m", BaseURL: server.URL, Client: server.Client()}
	_, err := g.Transcribe(context.Background(), []byte("audio"), "mp3", "")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got: %v", err)
	}
}

func TestGeminiSTT_Transcribe_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"candidates": []map[string]any{}})
	}))
	defer server.Close()

	g := &GeminiSTT{APIKey: "k", Model: "m", BaseURL: server.URL, Client: server.Client()}
	_, err := g.Transcribe(context.Background(), []byte("audio"), "mp3", "")
	if err == nil {
		t.Fatal("expected error for empty candidates")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected 'empty response' in error, got: %v", err)
	}
}

func TestGeminiSTT_Transcribe_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	g := &GeminiSTT{APIKey: "k", Model: "m", BaseURL: server.URL, Client: server.Client()}
	_, err := g.Transcribe(context.Background(), []byte("audio"), "mp3", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("expected 'parse response' in error, got: %v", err)
	}
}

// TestConvertAudioToMP3_HonorsContextCancellation asserts that a cancelled
// context aborts the ffmpeg subprocess rather than running it to completion.
// Skips when ffmpeg is not installed (the conversion helper returns the
// "not found" error before the context is ever consulted).
func TestConvertAudioToMP3_HonorsContextCancellation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ConvertAudioToMP3(ctx, []byte{0, 1, 2, 3}, "mp3")
	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
}

// qwenCapture starts a fake Qwen ASR endpoint that records the request body.
func qwenCapture(t *testing.T) (*QwenASR, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return NewQwenASR("k", srv.URL, ""), &got
}

func TestQwenASR_SendsHintAsSystemMessage(t *testing.T) {
	q, got := qwenCapture(t)
	if _, err := q.TranscribeWithContext(context.Background(), []byte("a"), "mp3", "zh", "NapCat, cc-connect"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := (*got)["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want system + user", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Fatalf("first message role = %v, want system", sys["role"])
	}
	content, _ := sys["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["text"] != "NapCat, cc-connect" {
		t.Errorf("system text = %v", part["text"])
	}
}

func TestQwenASR_NoHintSendsOnlyAudio(t *testing.T) {
	q, got := qwenCapture(t)
	if _, err := q.Transcribe(context.Background(), []byte("a"), "mp3", "zh"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := (*got)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want only the user audio message", len(msgs))
	}
}

type plainSTT struct{ calls int }

func (s *plainSTT) Transcribe(context.Context, []byte, string, string) (string, error) {
	s.calls++
	return "plain", nil
}

type hintSTT struct {
	plainSTT
	hint string
}

func (s *hintSTT) TranscribeWithContext(_ context.Context, _ []byte, _, _, hint string) (string, error) {
	s.hint = hint
	return "with hint", nil
}

func TestTranscribeAudio_PassesHintOnlyToContextualProviders(t *testing.T) {
	audio := &AudioAttachment{Data: []byte("a"), Format: "mp3"}

	h := &hintSTT{}
	if got, _ := TranscribeAudio(context.Background(), h, audio, "zh", "vocab"); got != "with hint" || h.hint != "vocab" {
		t.Errorf("contextual provider: got %q, hint %q", got, h.hint)
	}
	if got, _ := TranscribeAudio(context.Background(), h, audio, "zh", ""); got != "plain" {
		t.Errorf("contextual provider without hint: got %q, want plain Transcribe", got)
	}

	p := &plainSTT{}
	if got, _ := TranscribeAudio(context.Background(), p, audio, "zh", "vocab"); got != "plain" || p.calls != 1 {
		t.Errorf("plain provider: got %q, calls %d", got, p.calls)
	}
}

func TestEngineSpeechHint(t *testing.T) {
	e := newTestEngine()
	e.SetSpeechConfig(SpeechCfg{Context: "NapCat, ALPDOJ", ContextHistory: 2})
	s := e.sessions.GetOrCreateActive("qq:1")
	s.AddHistory("user", "old message")
	s.AddHistory("user", "look at the cc-connect log")
	s.AddHistory("assistant", strings.Repeat("x", 1000))

	hint := e.speechHint("qq:1")
	for _, want := range []string{"NapCat, ALPDOJ", "user: look at the cc-connect log", "assistant: xxx"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q:\n%s", want, hint)
		}
	}
	if strings.Contains(hint, "old message") {
		t.Errorf("hint includes more than ContextHistory messages:\n%s", hint)
	}
	if strings.Contains(hint, strings.Repeat("x", speechHintEntryRunes+1)) {
		t.Error("long history entry was not truncated")
	}

	if got := e.speechHint("qq:unknown"); got != "NapCat, ALPDOJ" {
		t.Errorf("unknown session: hint = %q, want only the configured context", got)
	}
	e.SetSpeechConfig(SpeechCfg{})
	if got := e.speechHint("qq:1"); got != "" {
		t.Errorf("no context configured: hint = %q, want empty", got)
	}
}
