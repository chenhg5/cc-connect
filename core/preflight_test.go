package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreflightManager_HTTPBlockDecisionOmitsContentByDefault(t *testing.T) {
	var got PreflightEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"block","code":"missing_scope","message":"请先完成授权"}`))
	}))
	defer srv.Close()

	pm := NewPreflightManager("proj", []PreflightCheckConfig{
		{
			Event:   string(PreflightEventMessage),
			Type:    "http",
			URL:     srv.URL,
			Timeout: 1,
		},
	})

	decision := pm.Evaluate(PreflightEvent{
		Event:      PreflightEventMessage,
		SessionKey: "feishu:oc_group",
		Platform:   "feishu",
		MessageID:  "om_1",
		ChannelID:  "oc_group",
		UserID:     "ou_user",
		UserName:   "张三",
		ChatName:   "售后群",
		Content:    "sensitive prompt",
	})

	if decision.Decision != PreflightBlock {
		t.Fatalf("decision = %q, want %q", decision.Decision, PreflightBlock)
	}
	if decision.Code != "missing_scope" || decision.Message != "请先完成授权" {
		t.Fatalf("unexpected decision payload: %+v", decision)
	}
	if got.Project != "proj" {
		t.Fatalf("project = %q, want proj", got.Project)
	}
	if got.Event != PreflightEventMessage || got.MessageID != "om_1" || got.ChannelID != "oc_group" {
		t.Fatalf("unexpected request event: %+v", got)
	}
	if got.Content != "" {
		t.Fatalf("content should be omitted by default, got %q", got.Content)
	}
}

func TestPreflightManager_HTTPIncludeContentAndErrorPolicy(t *testing.T) {
	t.Run("include content", func(t *testing.T) {
		var got PreflightEvent
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_, _ = w.Write([]byte(`{"decision":"continue"}`))
		}))
		defer srv.Close()

		pm := NewPreflightManager("proj", []PreflightCheckConfig{
			{Type: "http", URL: srv.URL, IncludeContent: true},
		})

		decision := pm.Evaluate(PreflightEvent{Event: PreflightEventMessage, Content: "hello"})
		if decision.Decision != PreflightContinue {
			t.Fatalf("decision = %q, want %q", decision.Decision, PreflightContinue)
		}
		if got.Content != "hello" {
			t.Fatalf("content = %q, want hello", got.Content)
		}
	})

	t.Run("defaults to block on error", func(t *testing.T) {
		pm := NewPreflightManager("proj", []PreflightCheckConfig{
			{Type: "http", URL: "http://127.0.0.1:1", Timeout: 1},
		})

		decision := pm.Evaluate(PreflightEvent{Event: PreflightEventMessage})
		if decision.Decision != PreflightBlock {
			t.Fatalf("decision = %q, want %q", decision.Decision, PreflightBlock)
		}
		if !strings.Contains(decision.Code, "preflight") {
			t.Fatalf("expected preflight error code, got %+v", decision)
		}
	})

	t.Run("can continue on error", func(t *testing.T) {
		pm := NewPreflightManager("proj", []PreflightCheckConfig{
			{Type: "http", URL: "http://127.0.0.1:1", Timeout: 1, OnError: string(PreflightContinue)},
		})

		decision := pm.Evaluate(PreflightEvent{Event: PreflightEventMessage})
		if decision.Decision != PreflightContinue {
			t.Fatalf("decision = %q, want %q", decision.Decision, PreflightContinue)
		}
	})
}
