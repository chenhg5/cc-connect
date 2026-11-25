package core

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguredURLsDoNotExposeSecretsInLogs(t *testing.T) {
	const secret = "CONFIG-URL-SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	url := server.URL + "/private-" + secret + "?access_token=" + secret + "#" + secret
	hm := NewHookManager("test", nil, "sh", "-c", "")
	hm.executeHTTP(&HookConfig{URL: url}, HookEvent{Event: HookEventError})
	proxy, _, err := NewProviderProxy(url, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if strings.Contains(logs.String(), secret) {
		t.Fatal("configured URL log exposed secret")
	}
	if !strings.Contains(logs.String(), "status=403") || !strings.Contains(logs.String(), "127.0.0.1") {
		t.Fatal("configured URL log lost safe status or host")
	}
}

func TestRedactURLKeepsOnlySchemeAndHost(t *testing.T) {
	const secret = "CONFIG-URL-SENTINEL"
	got := RedactURL("https://user:" + secret + "@example.com:8443/private-" + secret + "?access_token=" + secret + "#" + secret)
	if got != "https://example.com:8443" {
		t.Fatalf("redacted URL = %q", got)
	}
	if got := RedactURL("://" + secret); got != "[invalid URL]" {
		t.Fatalf("invalid URL = %q", got)
	}
}
