package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookServer_AuthBearer(t *testing.T) {
	ws := NewWebhookServer(0, "my-secret", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	r.Header.Set("Authorization", "Bearer my-secret")
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with correct Bearer token")
	}
	r.Header.Set("Authorization", "Bearer wrong")
	if ws.authenticate(r) {
		t.Error("expected auth to fail with wrong Bearer token")
	}
}

func TestWebhookServer_AuthHeader(t *testing.T) {
	ws := NewWebhookServer(0, "tok123", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	r.Header.Set("X-Webhook-Token", "tok123")
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with X-Webhook-Token")
	}
}

func TestWebhookServer_AuthQuery(t *testing.T) {
	ws := NewWebhookServer(0, "qsecret", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook?token=qsecret", nil)
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with query token")
	}
}

func TestWebhookServer_NoTokenRequired(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	if !ws.authenticate(r) {
		t.Error("expected auth to pass when no token configured")
	}
}

func TestWebhookServer_HandleHook_MethodNotAllowed(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/hook", nil)
	ws.handleHook(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestWebhookServer_HandleHook_Unauthorized(t *testing.T) {
	ws := NewWebhookServer(0, "secret", "/hook")
	w := httptest.NewRecorder()
	body, _ := json.Marshal(WebhookRequest{SessionKey: "tg:1:1", Prompt: "hi"})
	r := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	ws.handleHook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestWebhookServer_HandleHook_Validation(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")

	tests := []struct {
		name string
		body WebhookRequest
		code int
	}{
		{"missing session_key", WebhookRequest{Prompt: "hi"}, http.StatusBadRequest},
		{"missing prompt and exec", WebhookRequest{SessionKey: "tg:1:1"}, http.StatusBadRequest},
		{"both prompt and exec", WebhookRequest{SessionKey: "tg:1:1", Prompt: "hi", Exec: "ls"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tt.body)
			r := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
			ws.handleHook(w, r)
			if w.Code != tt.code {
				t.Errorf("expected %d, got %d: %s", tt.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestWebhookServer_DefaultValues(t *testing.T) {
	ws := NewWebhookServer(0, "", "")
	if ws.port != 9111 {
		t.Errorf("expected default port 9111, got %d", ws.port)
	}
	if ws.path != "/hook" {
		t.Errorf("expected default path /hook, got %s", ws.path)
	}
}
