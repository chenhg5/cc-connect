package moltybot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := New(map[string]any{"token": "secret"})
	if err == nil {
		t.Fatal("New returned nil error, want missing base_url error")
	}
}

func TestSessionSendPostsToBridge(t *testing.T) {
	var authHeader string
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s, want /v1/messages", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"sessionKey": "remote:weixin:u1",
			"replyText":  "收到",
		})
	}))
	defer server.Close()

	agent, err := New(map[string]any{
		"base_url":     server.URL,
		"token":        "secret",
		"session_mode": "per_remote_user",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	session, err := agent.StartSession(context.Background(), "weixin:chat:u1")
	if err != nil {
		t.Fatalf("StartSession returned error: %v", err)
	}
	if err := session.Send("你好", nil, nil); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	ev := <-session.Events()
	if ev.Type != core.EventResult || ev.Content != "收到" {
		t.Fatalf("event = %#v, want result 收到", ev)
	}
	if authHeader != "Bearer secret" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if request["text"] != "你好" {
		t.Fatalf("text = %#v", request["text"])
	}
}
