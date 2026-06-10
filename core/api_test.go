package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestHandleSend_AllowsTTSTextOnly(t *testing.T) {
	tts := &recordingTTS{}
	platform := &audioStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	engine := NewEngine("test", &stubAgent{}, []Platform{platform}, "", LangEnglish)
	engine.SetTTSConfig(&TTSCfg{
		Enabled:  true,
		Provider: "minimax",
		Voice:    "voice-x",
		Speed:    0.98,
		TTS:      tts,
	})
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: platform,
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	body, err := json.Marshal(SendRequest{
		Project:    "test",
		SessionKey: "session-1",
		TTSText:    "hello voice",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	text, opts, calls := tts.snapshot()
	if calls != 1 {
		t.Fatalf("tts calls = %d, want 1", calls)
	}
	if text != "hello voice" {
		t.Fatalf("tts text = %q", text)
	}
	if opts.Voice != "voice-x" || opts.Speed != 0.98 {
		t.Fatalf("tts opts = %#v", opts)
	}
	if _, format, audioCalls := platform.audioSnapshot(); audioCalls != 1 || format != "mp3" {
		t.Fatalf("audio calls/format = %d/%q", audioCalls, format)
	}
}

// TestHandleSend_UnknownProjectReturns404 ensures the API does NOT silently
// fall back to the only registered engine when the caller named a different
// project. Previously a typo'd project name routed messages to whatever
// single engine happened to be loaded.
func TestHandleSend_UnknownProjectReturns404(t *testing.T) {
	engine := NewEngine("projectA", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"projectA": engine}}
	body, err := json.Marshal(SendRequest{
		Project:    "projectB", // typo; does NOT match the loaded engine
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"projectB"`) {
		t.Errorf("body should mention the unknown project name, got: %s", rec.Body.String())
	}
}

// TestHandleSend_EmptyProjectFallsBackToSingleEngine documents the intended
// convenience behavior: when the caller omits project entirely AND only one
// engine is loaded, the API picks it automatically.
func TestHandleSend_EmptyProjectFallsBackToSingleEngine(t *testing.T) {
	engine := NewEngine("solo", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"solo": engine}}
	body, err := json.Marshal(SendRequest{
		// Project deliberately omitted.
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleSend_EmptyProjectMultipleEnginesRequiresName ensures the API
// refuses to guess when more than one engine is loaded and the caller did
// not specify which one to send to.
func TestHandleSend_EmptyProjectMultipleEnginesRequiresName(t *testing.T) {
	engineA := NewEngine("a", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engineB := NewEngine("b", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"a": engineA, "b": engineB}}

	body, err := json.Marshal(SendRequest{
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCronExec_TriggersJob(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "discord"},
	}
	agentSession := newResultAgentSession("triggered from local api")
	engine := NewEngine("test", &resultAgent{session: agentSession}, []Platform{platform}, "", LangEnglish)
	defer engine.cancel()
	engine.cronScheduler = scheduler
	scheduler.RegisterEngine("test", engine)

	job := &CronJob{
		ID:          "job-run-api",
		Project:     "test",
		SessionKey:  "discord:channel-1:user-1",
		CronExpr:    "0 6 * * *",
		Prompt:      "run now",
		Description: "Run from API",
		Enabled:     false,
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}, cron: scheduler}
	body, err := json.Marshal(map[string]any{"id": job.ID})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/cron/exec", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleCronExec(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(platform.getSent()) >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for local api trigger, sent=%v", platform.getSent())
}

func TestHandleCronExec_RunAliasRouteTriggersJob(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "discord"},
	}
	agentSession := newResultAgentSession("triggered from local api alias")
	engine := NewEngine("test", &resultAgent{session: agentSession}, []Platform{platform}, "", LangEnglish)
	defer engine.cancel()
	engine.cronScheduler = scheduler
	scheduler.RegisterEngine("test", engine)

	job := &CronJob{
		ID:          "job-run-api-alias",
		Project:     "test",
		SessionKey:  "discord:channel-1:user-1",
		CronExpr:    "0 6 * * *",
		Prompt:      "run alias now",
		Description: "Run from API alias",
		Enabled:     false,
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}, cron: scheduler, mux: http.NewServeMux()}
	api.mux.HandleFunc("/cron/exec", api.handleCronExec)
	api.mux.HandleFunc("/cron/run", api.handleCronExec)
	body, err := json.Marshal(map[string]any{"id": job.ID})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/cron/run", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(platform.getSent()) >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for local api alias trigger, sent=%v", platform.getSent())
}

func TestHandleCronExec_ProjectMissingIsBadRequest(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	job := &CronJob{
		ID:         "job-run-missing-project",
		Project:    "ghost",
		SessionKey: "discord:channel-1:user-1",
		CronExpr:   "0 6 * * *",
		Prompt:     "run now",
		Enabled:    true,
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	api := &APIServer{cron: scheduler}
	body, err := json.Marshal(map[string]any{"id": job.ID})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/cron/exec", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleCronExec(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
