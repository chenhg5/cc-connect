package max

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type synchronizedLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *synchronizedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestWebhookLogsOmitURLSecretAndErrorBody(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			_, _ = w.Write([]byte(`{"name":"test","user_id":1}`))
		case "/subscriptions":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"secret":"` + marker + `"}`))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	logs := &synchronizedLogBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	p, err := New(map[string]any{
		"token":          "test",
		"api_base":       api.URL,
		"webhook_url":    "https://example.com/webhook?s=" + marker,
		"webhook_listen": "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := p.(*Platform)
	if err := platform.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := platform.Stop(); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if strings.Contains(got, marker) {
		t.Fatal("MAX webhook URL or response secret appeared in logs")
	}
	if !strings.Contains(got, "https://example.com/webhook") {
		t.Fatal("MAX webhook log lost safe host and path")
	}
}

func TestSubscribeErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"secret":"` + marker + `"}`))
	}))
	defer api.Close()
	p, err := New(map[string]any{"token": "test", "api_base": api.URL, "webhook_secret": marker})
	if err != nil {
		t.Fatal(err)
	}
	err = p.(*Platform).subscribe(context.Background(), "https://example.com/webhook")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("subscribe error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("MAX subscribe error exposed response secret")
	}
}
