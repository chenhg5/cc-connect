package cloudweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestLongPollIntegration(t *testing.T) {
	registered := false
	var outbound []map[string]any
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case defaultRegister:
			registered = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(wireRegisterAck{
				Type:         "register_ack",
				OK:           true,
				Capabilities: allCapabilities,
			})
		case defaultEvents:
			ev, _ := json.Marshal(wireInboundMessage{
				Type: "message", MsgID: "p1", SessionKey: "cloud_web:c:u",
				UserID: "u", Content: "ping", ReplyCtx: "ctx",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []json.RawMessage{ev}})
		case defaultSend:
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			mu.Lock()
			outbound = append(outbound, m)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := mustNew(t, map[string]any{
		"token":     "secret",
		"transport": "long_poll",
		"base_url":  srv.URL,
	})

	got := make(chan string, 1)
	err := p.Start(func(_ core.Platform, msg *core.Message) {
		got <- msg.Content
		_ = p.Reply(context.Background(), msg.ReplyCtx, "pong")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()

	select {
	case content := <-got:
		if content != "ping" {
			t.Fatalf("content = %q", content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !registered {
		t.Fatal("register not called")
	}
	found := false
	for _, m := range outbound {
		if m["type"] == "reply" && m["content"] == "pong" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outbound = %#v", outbound)
	}
}

func TestCapabilityDegradeCard(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	p.tp.(*pollTransport).setCaps(map[string]bool{"text": true})

	card := &core.Card{Elements: []core.CardElement{core.CardMarkdown{Content: "hello card"}}}
	err := p.SendCard(context.Background(), replyContext{SessionKey: "s", ReplyCtx: "r"}, card)
	if err == nil {
		t.Fatal("expected error when send path unreachable")
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	p.tp.(*pollTransport).setCaps(capabilitySet([]string{"text", "reconstruct_reply"}))
	p.storeReplyCtx("cloud_web:c:u", "stored-ctx")

	rc, err := p.ReconstructReplyCtx("cloud_web:c:u")
	if err != nil {
		t.Fatal(err)
	}
	got := rc.(replyContext)
	if got.ReplyCtx != "stored-ctx" {
		t.Fatalf("reply_ctx = %q", got.ReplyCtx)
	}
}

func TestGroupReplyRequiresMention(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
		"group_reply_all": false,
	})

	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	mentioned := false
	raw, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g1", UserID: "u1", Content: "hi",
		ChatType: "group", Mentioned: &mentioned, ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if called {
		t.Fatal("expected group message without mention to be ignored")
	}

	mentioned = true
	raw, _ = json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "g2", UserID: "u1", Content: "hi",
		ChatType: "group", Mentioned: &mentioned, ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if !called {
		t.Fatal("expected mentioned group message to be accepted")
	}
}

func TestCardActionPermAllow(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})

	done := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { done <- msg }

	raw, _ := json.Marshal(wireCardAction{
		Type: "card_action", SessionKey: "cloud_web:c:u", Action: "perm:allow", ReplyCtx: "ctx",
	})
	p.handleCardAction(raw)
	select {
	case msg := <-done:
		if msg.Content != "allow" {
			t.Fatalf("content = %q, want allow", msg.Content)
		}
		if !msg.IsPermissionResponse {
			t.Fatal("expected IsPermissionResponse=true for perm:allow card action")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for perm action dispatch")
	}
}

func TestEmptyMessageIgnored(t *testing.T) {
	p := mustNew(t, map[string]any{
		"token": "t", "transport": "long_poll", "base_url": "http://127.0.0.1",
	})
	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	raw, _ := json.Marshal(wireInboundMessage{
		Type: "message", MsgID: "e1", UserID: "u1", Content: "   ", ReplyCtx: "ctx",
	})
	p.handleMessage(raw)
	if called {
		t.Fatal("expected empty message to be ignored")
	}
}
