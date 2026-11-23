package antigravity

import (
	"context"

	"io"
	"net/http"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

type subscriptionAPITransport struct{}

func (subscriptionAPITransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro"}]}`)), Header: make(http.Header)}, nil
}

func TestSubscriptionModelWithAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "fixture-key")
	old := http.DefaultTransport
	http.DefaultTransport = subscriptionAPITransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cmd := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\nprintf 'gemini-pro-orchestrator-neon\tOrchestrator (Neon)\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: cmd, workDir: t.TempDir(), activeIdx: -1}
	if err := a.ValidateModel(context.Background(), "gemini-pro-orchestrator-neon"); err != nil {
		t.Fatalf("CLI offers valid subscription model but validator rejects it: %v", err)
	}
}
