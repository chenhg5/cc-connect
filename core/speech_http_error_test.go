package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhisperHTTPErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"authentication failed","secret":"` + marker + `"}`))
	}))
	defer server.Close()
	whisper := NewOpenAIWhisper(marker, server.URL, "test")
	_, err := whisper.Transcribe(context.Background(), []byte("audio"), "wav", "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("transcribe error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("speech API error exposed response secret")
	}
}
