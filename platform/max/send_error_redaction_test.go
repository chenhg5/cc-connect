package max

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostMessageErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":"denied","message":"not allowed","secret":"` + marker + `"}`))
	}))
	defer api.Close()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{apiBase: api.URL, token: "test", client: api.Client()}
	err := p.postMessage(context.Background(), replyContext{chatID: "123"}, &maxSendBody{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("send error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(logs.String(), marker) {
		t.Fatal("MAX send error exposed response secret")
	}
}
