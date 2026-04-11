package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleSend_NewThread_PostsTopLevel(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "slack"}}
	engine := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:    "test",
		SessionKey: "slack:C123:U456",
		Message:    "New completion file: STORY-22.3.md",
		NewThread:  true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// Verify the message was sent via p.Send (top-level), not via an interactiveState
	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "New completion file: STORY-22.3.md" {
		t.Fatalf("sent = %v, want one message", sent)
	}
	// Verify ReconstructReplyCtx was called with the session key
	if p.reconstructSessionKey != "slack:C123:U456" {
		t.Fatalf("reconstructSessionKey = %q, want slack:C123:U456", p.reconstructSessionKey)
	}
	// Verify no interactiveState was created
	engine.interactiveMu.Lock()
	nStates := len(engine.interactiveStates)
	engine.interactiveMu.Unlock()
	if nStates != 0 {
		t.Fatalf("expected 0 interactiveStates, got %d", nStates)
	}
}

func TestHandleSend_NewThread_InfersSessionKeyFromActiveSession(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "slack"}}
	engine := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	engine.interactiveStates["slack:C123:U456"] = &interactiveState{
		platform: p,
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:   "test",
		Message:   "inferred session",
		NewThread: true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "inferred session" {
		t.Fatalf("sent = %v, want one message", sent)
	}
}

func TestHandleSend_NewThread_ErrorsWithNoSessions(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "slack"}}
	engine := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:   "test",
		Message:   "no sessions at all",
		NewThread: true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error when no active sessions and no session_key")
	}
}

func TestHandleSend_AllowsAttachmentOnly(t *testing.T) {
	engine := NewEngine("test", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:    "test",
		SessionKey: "session-1",
		Images: []ImageAttachment{{
			MimeType: "image/png",
			Data:     []byte("img"),
			FileName: "chart.png",
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
