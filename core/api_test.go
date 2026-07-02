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

type livenessStubAgentSession struct {
	stubAgentSession
	alive bool
}

func (s *livenessStubAgentSession) Alive() bool {
	return s.alive
}

func TestHandleProjectStatus(t *testing.T) {
	// 1. Idle status (no busy sessions)
	engine := NewEngine("test", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}

	req := httptest.NewRequest(http.MethodGet, "/project/status?project=test", nil)
	rec := httptest.NewRecorder()
	api.handleProjectStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if res["project"] != "test" {
		t.Errorf("got project %v, want test", res["project"])
	}
	if res["status"] != "idle" {
		t.Errorf("got status %v, want idle", res["status"])
	}
	if res["process_alive"] != false {
		t.Errorf("got process_alive %v, want false", res["process_alive"])
	}
	if res["active_sessions"].(float64) != 1 {
		t.Errorf("got active_sessions %v, want 1", res["active_sessions"])
	}

	// 2. Working status (busy session + process alive)
	session := engine.sessions.GetOrCreateActive("session-1")
	session.TryLock() // lock it to make it busy
	engine.interactiveStates["session-1"].agentSession = &livenessStubAgentSession{alive: true}

	rec2 := httptest.NewRecorder()
	api.handleProjectStatus(rec2, req)
	if err := json.Unmarshal(rec2.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response 2: %v", err)
	}
	if res["status"] != "working" {
		t.Errorf("got status %v, want working", res["status"])
	}
	if res["process_alive"] != true {
		t.Errorf("got process_alive %v, want true", res["process_alive"])
	}

	// 3. Crashed status (busy session + process not alive)
	engine.interactiveStates["session-1"].agentSession = &livenessStubAgentSession{alive: false}
	rec3 := httptest.NewRecorder()
	api.handleProjectStatus(rec3, req)
	if err := json.Unmarshal(rec3.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response 3: %v", err)
	}
	if res["status"] != "crashed" {
		t.Errorf("got status %v, want crashed", res["status"])
	}
	if res["process_alive"] != false {
		t.Errorf("got process_alive %v, want false", res["process_alive"])
	}

	// 4. Hung status (busy session + process alive + time since UpdatedAt > timeoutLimit)
	engine.interactiveStates["session-1"].agentSession = &livenessStubAgentSession{alive: true}
	session.UpdatedAt = time.Now().Add(-5 * time.Minute) // set UpdatedAt to 5 minutes ago to trigger timeout

	rec4 := httptest.NewRecorder()
	api.handleProjectStatus(rec4, req)
	if err := json.Unmarshal(rec4.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response 4: %v", err)
	}
	if res["status"] != "hung" {
		t.Errorf("got status %v, want hung", res["status"])
	}
}

// TestHandleProjectStatus_StaleOutputTriggersHung proves the LastOutputAt
// signal (L-0085/L-0097) independently marks a session "hung" even when
// UpdatedAt alone would still say "working" — this is the exact gap that
// let the L-0081 hang go undetected for the old UpdatedAt-only check.
func TestHandleProjectStatus_StaleOutputTriggersHung(t *testing.T) {
	engine := NewEngine("test-stale", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test-stale"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test-stale"}},
		replyCtx: "reply-ctx",
	}
	engine.interactiveStates["session-1"].agentSession = &livenessStubAgentSession{alive: true}

	api := &APIServer{engines: map[string]*Engine{"test-stale": engine}}
	req := httptest.NewRequest(http.MethodGet, "/project/status?project=test-stale", nil)

	session := engine.sessions.GetOrCreateActive("session-1")
	session.TryLock()
	// UpdatedAt is recent — the old, coarse signal alone would say "working".
	session.UpdatedAt = time.Now()
	// But LastOutputAt is stale beyond defaultHungAfter (90s) — the new,
	// finer signal must independently mark this hung regardless.
	session.LastOutputAt = time.Now().Add(-100 * time.Second)

	rec := httptest.NewRecorder()
	api.handleProjectStatus(rec, req)

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res["status"] != "hung" {
		t.Errorf("got status %v, want hung (stale LastOutputAt must trigger hung even with recent UpdatedAt)", res["status"])
	}
	lastOutputMsAgo, ok := res["last_output_ms_ago"].(float64)
	if !ok {
		t.Fatal("expected last_output_ms_ago in response")
	}
	if lastOutputMsAgo < 99000 {
		t.Errorf("got last_output_ms_ago %v, want >= ~100000", lastOutputMsAgo)
	}
}

// TestHandleProjectStatus_RecentOutputIsWorkingNotHung is the inverse
// case: fresh LastOutputAt within the threshold must NOT be misreported
// as hung, even though UpdatedAt (turn-boundary only) hasn't moved yet.
func TestHandleProjectStatus_RecentOutputIsWorkingNotHung(t *testing.T) {
	engine := NewEngine("test-fresh", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test-fresh"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test-fresh"}},
		replyCtx: "reply-ctx",
	}
	engine.interactiveStates["session-1"].agentSession = &livenessStubAgentSession{alive: true}

	api := &APIServer{engines: map[string]*Engine{"test-fresh": engine}}
	req := httptest.NewRequest(http.MethodGet, "/project/status?project=test-fresh", nil)

	session := engine.sessions.GetOrCreateActive("session-1")
	session.TryLock()
	session.UpdatedAt = time.Now()
	session.TouchOutput() // fresh event, well within the stale-output threshold

	rec := httptest.NewRecorder()
	api.handleProjectStatus(rec, req)

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res["status"] != "working" {
		t.Errorf("got status %v, want working", res["status"])
	}
}
