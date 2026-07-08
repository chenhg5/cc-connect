package moltybot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := New(map[string]any{"token": "secret"})
	if err == nil {
		t.Fatal("New returned nil error, want missing base_url error")
	}
}

func TestNewRequiresToken(t *testing.T) {
	_, err := New(map[string]any{"base_url": "http://127.0.0.1:12345", "token": "  "})
	if err == nil {
		t.Fatal("New returned nil error, want missing token error")
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
	if err := session.Send(
		"你好",
		[]core.ImageAttachment{{MimeType: "image/png", FileName: "pic.png", Data: []byte{1, 2, 3}}},
		[]core.FileAttachment{{MimeType: "text/plain", FileName: "note.txt", Data: []byte("hi")}},
	); err != nil {
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
	if _, ok := request["sessionKey"]; ok {
		t.Fatalf("unexpected top-level sessionKey in request: %#v", request["sessionKey"])
	}
	if _, ok := request["sessionId"]; ok {
		t.Fatalf("unexpected top-level sessionId in request: %#v", request["sessionId"])
	}
	if _, ok := request["images"]; ok {
		t.Fatalf("unexpected top-level images in request: %#v", request["images"])
	}
	if _, ok := request["files"]; ok {
		t.Fatalf("unexpected top-level files in request: %#v", request["files"])
	}
	source, ok := request["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v, want object", request["source"])
	}
	if source["platform"] != "weixin" {
		t.Fatalf("source.platform = %#v, want weixin", source["platform"])
	}
	if source["platformUserId"] != "u1" {
		t.Fatalf("source.platformUserId = %#v, want u1", source["platformUserId"])
	}
	attachments, ok := request["attachments"].([]any)
	if !ok || len(attachments) != 2 {
		t.Fatalf("attachments = %#v, want 2 attachments", request["attachments"])
	}
	image := attachments[0].(map[string]any)
	if image["kind"] != "image" || image["name"] != "pic.png" || image["mimeType"] != "image/png" || image["dataBase64"] != "AQID" {
		t.Fatalf("image attachment = %#v", image)
	}
	file := attachments[1].(map[string]any)
	if file["kind"] != "file" || file["name"] != "note.txt" || file["mimeType"] != "text/plain" || file["dataBase64"] != "aGk=" {
		t.Fatalf("file attachment = %#v", file)
	}
}

func TestSessionSendEmitsBridgeResponseAttachments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"sessionKey": "remote:weixin:u1",
			"replyText":  "已生成。",
			"attachments": []map[string]any{
				{"kind": "image", "name": "generated.png", "mimeType": "image/png", "dataBase64": "AQID"},
				{"kind": "file", "name": "report.txt", "mimeType": "text/plain", "dataBase64": "aGk="},
			},
		})
	}))
	defer server.Close()

	agent, err := New(map[string]any{
		"base_url": server.URL,
		"token":    "secret",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	session, err := agent.StartSession(context.Background(), "weixin:chat:u1")
	if err != nil {
		t.Fatalf("StartSession returned error: %v", err)
	}
	if err := session.Send("画图", nil, nil); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	ev := <-session.Events()
	if ev.Type != core.EventResult || ev.Content != "已生成。" {
		t.Fatalf("event = %#v, want result 已生成。", ev)
	}
	if len(ev.Images) != 1 || ev.Images[0].FileName != "generated.png" || string(ev.Images[0].Data) != "\x01\x02\x03" {
		t.Fatalf("images = %#v", ev.Images)
	}
	if len(ev.Files) != 1 || ev.Files[0].FileName != "report.txt" || string(ev.Files[0].Data) != "hi" {
		t.Fatalf("files = %#v", ev.Files)
	}
}

func TestSessionSendUsesInjectedCCSessionKeyWhenStartingFresh(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	inj, ok := agent.(core.SessionEnvInjector)
	if !ok {
		t.Fatal("moltybot agent does not implement core.SessionEnvInjector")
	}
	inj.SetSessionEnv([]string{"CC_PROJECT=moltybot", "CC_SESSION_KEY=weixin:dm:u1"})

	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession returned error: %v", err)
	}
	if got := session.CurrentSessionID(); got != "remote:weixin:u1" {
		t.Fatalf("CurrentSessionID = %q, want remote:weixin:u1", got)
	}
	if err := session.Send("你好", nil, nil); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	source, ok := request["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %#v, want object", request["source"])
	}
	if source["platform"] != "weixin" {
		t.Fatalf("source.platform = %#v, want weixin", source["platform"])
	}
	if source["platformUserId"] != "u1" {
		t.Fatalf("source.platformUserId = %#v, want u1", source["platformUserId"])
	}
}

func TestSessionSendEmitsEventErrorOnBridgeError(t *testing.T) {
	const token = "secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "boom " + token,
		})
	}))
	defer server.Close()

	agent, err := New(map[string]any{
		"base_url": server.URL,
		"token":    token,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	session, err := agent.StartSession(context.Background(), "weixin:chat:u1")
	if err != nil {
		t.Fatalf("StartSession returned error: %v", err)
	}
	err = session.Send("你好", nil, nil)
	if err != nil && strings.Contains(err.Error(), token) {
		t.Fatalf("Send error leaked token: %v", err)
	}

	select {
	case ev := <-session.Events():
		if ev.Type != core.EventError {
			t.Fatalf("event type = %s, want %s", ev.Type, core.EventError)
		}
		if ev.Error == nil {
			t.Fatal("event error is nil")
		}
		if strings.Contains(ev.Error.Error(), token) || strings.Contains(ev.Content, token) {
			t.Fatalf("EventError leaked token: %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for EventError")
	}
}
