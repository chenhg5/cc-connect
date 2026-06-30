package cloudweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestGatewayWebhook(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token":        "secret",
		"transport":    "gateway",
		"base_url":     "http://127.0.0.1:1",
		"listen":       ":0",
		"webhook_path": "/hook",
	})

	got := make(chan string, 1)
	if err := p.Start(func(_ core.Platform, msg *core.Message) {
		got <- msg.Content
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	gt := p.tp.(*gatewayTransport)
	if gt.listener == nil {
		t.Fatal("gateway listener not started")
	}
	url := "http://" + gt.listener.Addr().String() + gt.webhookPath
	body, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g1", SessionKey: "cloud_web:x:y",
		UserID: "y", Content: "from gateway", ReplyCtx: "ctx",
	})
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	select {
	case content := <-got:
		if content != "from gateway" {
			t.Fatalf("content = %q", content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for webhook message")
	}
}

func TestGatewayAuthenticate(t *testing.T) {
	// Empty token must fail closed: reject every request.
	empty := newGatewayTransport("", "", "n", "p", ":0", "", "", "", "")
	if empty.authenticate(httptest.NewRequest(http.MethodPost, "/hook", nil)) {
		t.Fatal("empty token must reject all requests (fail closed)")
	}

	gt := newGatewayTransport("", "secret", "n", "p", ":0", "", "", "", "")

	noCreds := httptest.NewRequest(http.MethodPost, "/hook", nil)
	if gt.authenticate(noCreds) {
		t.Fatal("request without credentials must be rejected")
	}

	wrong := httptest.NewRequest(http.MethodPost, "/hook", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")
	if gt.authenticate(wrong) {
		t.Fatal("wrong bearer token must be rejected")
	}

	bearer := httptest.NewRequest(http.MethodPost, "/hook", nil)
	bearer.Header.Set("Authorization", "Bearer secret")
	if !gt.authenticate(bearer) {
		t.Fatal("valid Bearer token must be accepted")
	}

	header := httptest.NewRequest(http.MethodPost, "/hook", nil)
	header.Header.Set("X-Cloud-Web-Token", "secret")
	if !gt.authenticate(header) {
		t.Fatal("valid X-Cloud-Web-Token header must be accepted")
	}

	query := httptest.NewRequest(http.MethodPost, "/hook?token=secret", nil)
	if !gt.authenticate(query) {
		t.Fatal("valid token query param must be accepted")
	}
}

func TestGatewayDerivesBaseURLFromRegister(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token":        "t",
		"transport":    "gateway",
		"register_url": "https://gateway.example.com/cloud-web/v1/register",
		"public_url":   "https://my.example.com/cloud-web/webhook",
		"listen":       ":0",
	})
	gt := p.tp.(*gatewayTransport)
	if gt.baseURL != "https://gateway.example.com" {
		t.Fatalf("baseURL = %q", gt.baseURL)
	}
}
