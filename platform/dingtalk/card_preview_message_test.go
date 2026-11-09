package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestCardFinalPreviewMessage_DefaultsOff(t *testing.T) {
	p, err := New(map[string]any{
		"client_id":        "cid",
		"client_secret":    "secret",
		"card_template_id": "template.schema",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.(*Platform).cardFinalPreviewMessage {
		t.Fatal("card_final_preview_message defaults to true")
	}
}

func TestCardFinalPreviewMessage_OptIn(t *testing.T) {
	p, err := New(map[string]any{
		"client_id":                  "cid",
		"client_secret":              "secret",
		"card_template_id":           "template.schema",
		"card_final_preview_message": true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.(*Platform).cardFinalPreviewMessage {
		t.Fatal("card_final_preview_message = false, want true")
	}
}

func TestAICardFinalize_FinalPreviewMessageIsOptIn(t *testing.T) {
	for _, tt := range []struct {
		name       string
		enabled    bool
		wantNormal bool
	}{
		{name: "disabled", enabled: false, wantNormal: false},
		{name: "enabled", enabled: true, wantNormal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var streamRequests int
			cardClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				streamRequests++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
				}, nil
			})}

			var normalRequests int
			var normalPayload map[string]any
			normalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				normalRequests++
				if err := json.NewDecoder(r.Body).Decode(&normalPayload); err != nil {
					t.Fatalf("decode normal message: %v", err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer normalServer.Close()

			previousHTTPClient := core.HTTPClient
			core.HTTPClient = normalServer.Client()
			defer func() { core.HTTPClient = previousHTTPClient }()

			card := &aiCard{
				outTrackId:  "test-track",
				templateKey: "content",
				platform: &Platform{
					httpClient:              cardClient,
					accessToken:             "test-token",
					tokenExpiry:             time.Now().Add(time.Hour),
					cardFinalPreviewMessage: tt.enabled,
				},
				replyCtx: replyContext{sessionWebhook: normalServer.URL},
				state:    "processing",
				done:     make(chan struct{}),
			}

			if err := card.Finalize(context.Background(), "final content"); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if streamRequests != 1 {
				t.Fatalf("stream requests = %d, want 1", streamRequests)
			}
			if got := normalRequests > 0; got != tt.wantNormal {
				t.Fatalf("normal message sent = %v, want %v", got, tt.wantNormal)
			}
			if tt.wantNormal && normalPayload["msgtype"] != "markdown" {
				t.Fatalf("normal message msgtype = %v, want markdown", normalPayload["msgtype"])
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
