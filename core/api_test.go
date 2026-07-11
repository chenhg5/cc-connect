package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

type sendWorkDirAgent struct {
	name    string
	workDir string
	session AgentSession
}

func (a *sendWorkDirAgent) Name() string { return a.name }
func (a *sendWorkDirAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return a.session, nil
}
func (a *sendWorkDirAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *sendWorkDirAgent) Stop() error { return nil }
func (a *sendWorkDirAgent) GetWorkDir() string {
	return a.workDir
}

func TestHandleSend_WorkDirStartsSideSession(t *testing.T) {
	agentName := "test-send-workdir-agent"
	baseDir := t.TempDir()
	targetDir := t.TempDir()
	sessionKey := "test:user1"
	var workspaceSession *resultAgentSession

	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		workspaceSession = newResultAgentSession("agent result from " + workDir)
		return &sendWorkDirAgent{
			name:    agentName,
			workDir: workDir,
			session: workspaceSession,
		}, nil
	})

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "test"},
	}
	engine := NewEngine(
		"test",
		&sendWorkDirAgent{
			name:    agentName,
			workDir: baseDir,
			session: newResultAgentSession("base result"),
		},
		[]Platform{platform},
		filepath.Join(t.TempDir(), "sessions.json"),
		LangEnglish,
	)
	api := &APIServer{engines: map[string]*Engine{"test": engine}}

	body, err := json.Marshal(map[string]any{
		"project":     "test",
		"session_key": sessionKey,
		"message":     "please ask this person",
		"work_dir":    targetDir,
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
	sent := platform.getSent()
	if len(sent) != 1 || sent[0] != "please ask this person" {
		t.Fatalf("platform sent = %#v, want direct send of request content", sent)
	}

	_, sessions, err := engine.getOrCreateWorkspaceAgent(targetDir)
	if err != nil {
		t.Fatalf("get workspace agent: %v", err)
	}
	list := sessions.ListSessions(sessionKey)
	if len(list) != 1 {
		t.Fatalf("workspace sessions len = %d, want 1", len(list))
	}
	if got := list[0].GetName(); got != "send" {
		t.Fatalf("side session name = %q, want send", got)
	}

	platform.clearSent()
	engine.handleMessage(platform, &Message{
		SessionKey: sessionKey,
		Platform:   "test",
		UserID:     "user1",
		UserName:   "Target",
		Content:    "human answer",
		ReplyCtx:   "reply-ctx",
	})
	sent = waitForPlatformSend(&platform.stubPlatformEngine, 1, 3*time.Second)
	if len(sent) == 0 || !strings.Contains(strings.Join(sent, "\n"), "agent result from "+targetDir) {
		t.Fatalf("platform sent after reply = %#v, want agent result from target work dir", sent)
	}
	if workspaceSession == nil || len(workspaceSession.sentPrompts) != 1 || !strings.Contains(workspaceSession.sentPrompts[0], "human answer") {
		t.Fatalf("workspace session prompts = %#v, want human reply prompt", workspaceSession)
	}
}

func TestHandleSend_WorkDirFollowsDirectParticipantOnInboundSession(t *testing.T) {
	agentName := "test-send-workdir-direct-agent"
	baseDir := t.TempDir()
	targetDir := t.TempDir()
	syntheticKey := "test:d:proactive:user1"
	realInboundKey := "test:d:real-conversation:user1"
	var workspaceSession *resultAgentSession

	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		workspaceSession = newResultAgentSession("agent result from " + workDir)
		return &sendWorkDirAgent{
			name:    agentName,
			workDir: workDir,
			session: workspaceSession,
		}, nil
	})

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "test"},
	}
	engine := NewEngine(
		"test",
		&sendWorkDirAgent{
			name:    agentName,
			workDir: baseDir,
			session: newResultAgentSession("base result"),
		},
		[]Platform{platform},
		filepath.Join(t.TempDir(), "sessions.json"),
		LangEnglish,
	)
	api := &APIServer{engines: map[string]*Engine{"test": engine}}

	body, err := json.Marshal(map[string]any{
		"project":     "test",
		"session_key": syntheticKey,
		"message":     "please ask this person",
		"work_dir":    targetDir,
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

	platform.clearSent()
	engine.handleMessage(platform, &Message{
		SessionKey: realInboundKey,
		Platform:   "test",
		UserID:     "user1",
		UserName:   "Target",
		Content:    "human answer",
		ReplyCtx:   "reply-ctx",
	})
	sent := waitForPlatformSend(&platform.stubPlatformEngine, 1, 3*time.Second)
	if len(sent) == 0 || !strings.Contains(strings.Join(sent, "\n"), "agent result from "+targetDir) {
		t.Fatalf("platform sent after reply = %#v, want agent result from target work dir", sent)
	}
	if workspaceSession == nil || len(workspaceSession.sentPrompts) != 1 || !strings.Contains(workspaceSession.sentPrompts[0], "human answer") {
		t.Fatalf("workspace session prompts = %#v, want human reply prompt", workspaceSession)
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

func TestSendBodyLimit_DefaultAndOverride(t *testing.T) {
	// Zero-value APIServer (no SetMaxAttachmentSize call) falls back to the
	// default and sizes the body for one base64-expanded attachment + envelope.
	got := (&APIServer{}).sendBodyLimit()
	want := DefaultMaxAttachmentSize*4/3 + sendBodyEnvelope
	if got != want {
		t.Fatalf("default sendBodyLimit = %d, want %d", got, want)
	}

	// A configured limit scales the body limit proportionally.
	api := &APIServer{}
	api.SetMaxAttachmentSize(100 << 20) // 100 MiB
	got = api.sendBodyLimit()
	want = (100<<20)*4/3 + sendBodyEnvelope
	if got != want {
		t.Fatalf("overridden sendBodyLimit = %d, want %d", got, want)
	}

	// Non-positive values are a no-op: the previously configured limit is kept
	// (callers always pass a positive, resolved value; this just guards typos).
	api.SetMaxAttachmentSize(0)
	if got, want := api.sendBodyLimit(), (100<<20)*4/3+sendBodyEnvelope; got != want {
		t.Fatalf("sendBodyLimit after zero override = %d, want %d (100 MiB retained)", got, want)
	}
}

// TestSetMaxAttachmentSize_ConcurrentSafe exercises the reload-vs-request race
// window: SetMaxAttachmentSize (config reload goroutine) mutates the limit
// while handleSend's sendBodyLimit reads it. Run with -race to catch the
// unsynchronised access this guards against.
func TestSetMaxAttachmentSize_ConcurrentSafe(t *testing.T) {
	api := &APIServer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			api.SetMaxAttachmentSize(int64(50+i%10) << 20)
		}
	}()
	for i := 0; i < 300; i++ {
		_ = api.sendBodyLimit()
	}
	<-done
}
