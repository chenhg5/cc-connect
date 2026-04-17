package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCronStore_MuteToggle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	job := &CronJob{
		ID:         "test1",
		Project:    "proj",
		SessionKey: "test:ch1",
		CronExpr:   "0 6 * * *",
		Prompt:     "hello",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	if store.Get("test1").Mute {
		t.Error("new job should not be muted")
	}

	if !store.SetMute("test1", true) {
		t.Error("SetMute should return true for existing job")
	}
	if !store.Get("test1").Mute {
		t.Error("job should be muted after SetMute(true)")
	}

	newState, ok := store.ToggleMute("test1")
	if !ok {
		t.Error("ToggleMute should return ok=true for existing job")
	}
	if newState {
		t.Error("ToggleMute should have toggled mute to false")
	}

	newState, ok = store.ToggleMute("test1")
	if !ok || !newState {
		t.Error("ToggleMute should toggle back to true")
	}

	if store.SetMute("nonexistent", true) {
		t.Error("SetMute should return false for nonexistent job")
	}
	_, ok = store.ToggleMute("nonexistent")
	if ok {
		t.Error("ToggleMute should return ok=false for nonexistent job")
	}
}

func TestCronStore_MutePersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	job := &CronJob{
		ID: "persist1", Project: "proj", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "hello", Enabled: true, CreatedAt: time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}
	if !store.SetMute("persist1", true) {
		t.Fatal("SetMute should return true")
	}

	store2, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j := store2.Get("persist1")
	if j == nil {
		t.Fatal("job not found after reload")
	}
	if !j.Mute {
		t.Error("mute flag should persist after reload")
	}
}

func TestMutePlatform_DiscardMessages(t *testing.T) {
	inner := &stubPlatformEngine{n: "test"}
	mp := &mutePlatform{inner}

	if err := mp.Reply(context.Background(), "ctx", "hello"); err != nil {
		t.Errorf("Reply should return nil, got %v", err)
	}
	if err := mp.Send(context.Background(), "key", "world"); err != nil {
		t.Errorf("Send should return nil, got %v", err)
	}

	if len(inner.getSent()) != 0 {
		t.Errorf("mutePlatform should discard messages, got %v", inner.getSent())
	}

	if mp.Name() != "test" {
		t.Errorf("mutePlatform should delegate Name(), got %q", mp.Name())
	}
}

func TestCronJob_MuteField(t *testing.T) {
	job := &CronJob{ID: "m1", Mute: false}
	if job.Mute {
		t.Error("default should be not muted")
	}
	job.Mute = true
	if !job.Mute {
		t.Error("should be muted after setting")
	}
}

func TestCronExprToHuman_BasicCases(t *testing.T) {
	tests := []struct {
		expr string
		lang Language
		want string
	}{
		{"0 6 * * *", LangEnglish, "Daily at 06:00"},
		{"0 6 * * *", LangChinese, "每天 06:00"},
		{"30 14 * * 1", LangEnglish, "Every Monday at 14:30"},
		// Step expressions
		{"*/5 * * * *", LangEnglish, "Every 5 min"},
		{"*/5 * * * *", LangChinese, "每5分钟"},
		{"*/30 * * * *", LangChinese, "每30分钟"},
		{"*/15 * * * *", LangJapanese, "15分ごと"},
		{"0 */2 * * *", LangEnglish, "Every 2 h (:00)"},
		{"0 */2 * * *", LangChinese, "每2小时 (:00)"},
		{"30 */6 * * *", LangEnglish, "Every 6 h (:30)"},
		// Regular cases still work
		{"0 0 1 * *", LangEnglish, "Monthly, day 1, 00:00"},
		{"0 0 1 * *", LangChinese, "每月1日 00:00"},
	}
	for _, tt := range tests {
		got := CronExprToHuman(tt.expr, tt.lang)
		if got != tt.want {
			t.Errorf("CronExprToHuman(%q, %v) = %q, want %q", tt.expr, tt.lang, got, tt.want)
		}
	}
}

func TestRenderCronCard_WithButtons(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(&CronJob{
		ID: "j1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "daily task", Enabled: true,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(&CronJob{
		ID: "j2", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 12 * * *", Prompt: "noon task", Enabled: false, Mute: true,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	card := e.renderCronCard("test:ch1", "")
	if card == nil {
		t.Fatal("card should not be nil")
	}

	hasButtons := card.HasButtons()
	if !hasButtons {
		t.Error("card should have interactive buttons")
	}

	allBtns := card.CollectButtons()
	var allValues []string
	for _, row := range allBtns {
		for _, btn := range row {
			allValues = append(allValues, btn.Data)
		}
	}

	found := map[string]bool{
		"disable j1": false,
		"enable j2":  false,
		"mute j1":    false,
		"unmute j2":  false,
		"delete j1":  false,
		"delete j2":  false,
	}
	for _, v := range allValues {
		for key := range found {
			if strings.Contains(v, key) {
				found[key] = true
			}
		}
	}
	for key, ok := range found {
		if !ok {
			t.Errorf("expected button containing %q not found in card buttons: %v", key, allValues)
		}
	}

	text := card.RenderText()
	if !strings.Contains(text, "[mute]") {
		t.Error("muted job should show [mute] tag in card text")
	}
}

func TestRenderCronCard_HasHint(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(&CronJob{
		ID: "h1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	card := e.renderCronCard("test:ch1", "")
	text := card.RenderText()
	if !strings.Contains(text, "/cron add") || !strings.Contains(text, "/cron mute") {
		t.Errorf("card should contain command hints, got:\n%s", text)
	}
}

func TestExecuteCardAction_CronActions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(&CronJob{
		ID: "act1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler
	scheduler.RegisterEngine("test", e)
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	defer scheduler.Stop()

	e.executeCardAction("/cron", "disable act1", "test:ch1")
	j := store.Get("act1")
	if j.Enabled {
		t.Error("job should be disabled after card action")
	}

	e.executeCardAction("/cron", "enable act1", "test:ch1")
	j = store.Get("act1")
	if !j.Enabled {
		t.Error("job should be re-enabled after card action")
	}

	e.executeCardAction("/cron", "mute act1", "test:ch1")
	j = store.Get("act1")
	if !j.Mute {
		t.Error("job should be muted after card action")
	}

	e.executeCardAction("/cron", "unmute act1", "test:ch1")
	j = store.Get("act1")
	if j.Mute {
		t.Error("job should be unmuted after card action")
	}

	e.executeCardAction("/cron", "delete act1", "test:ch1")
	if store.Get("act1") != nil {
		t.Error("job should be deleted after card action")
	}
}

func TestCmdCronMute_TextCommand(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(&CronJob{
		ID: "txt1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	msg := &Message{SessionKey: "test:ch1", UserID: "u1", ReplyCtx: "ctx"}

	e.cmdCronMute(p, msg, []string{"txt1"}, true)
	if !store.Get("txt1").Mute {
		t.Error("job should be muted via text command")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "🔇") {
		t.Errorf("should reply with muted confirmation, got: %v", sent)
	}

	e.cmdCronMute(p, msg, []string{"txt1"}, false)
	if store.Get("txt1").Mute {
		t.Error("job should be unmuted via text command")
	}
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "🔔") {
		t.Errorf("should reply with unmuted confirmation, got: %v", sent)
	}

	e.cmdCronMute(p, msg, []string{"nonexistent"}, true)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "not found") {
		t.Errorf("should reply with not found for bad id, got: %v", sent)
	}
}

func TestCronStore_JobsPath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, "crons", "jobs.json")
	if store.path != expected {
		t.Errorf("store path = %q, want %q", store.path, expected)
	}
}

func TestCronScheduler_RunJobNow_DisabledJobStillRuns(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "discord"},
	}
	agentSession := newResultAgentSession("manual run complete")
	agent := &resultAgent{session: agentSession}

	e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.cronScheduler = scheduler
	scheduler.RegisterEngine("test", e)

	job := &CronJob{
		ID:          "manual1",
		Project:     "test",
		SessionKey:  "discord:channel-1:user-1",
		CronExpr:    "0 6 * * *",
		Prompt:      "summarize activity",
		Description: "Disabled daily summary",
		Enabled:     false,
		CreatedAt:   time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunJobNow("manual1"); err != nil {
		t.Fatalf("RunJobNow() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent := platform.getSent()
		if len(sent) >= 2 {
			if sent[0] != "⏰ Disabled daily summary" {
				t.Fatalf("sent[0] = %q, want cron start notice", sent[0])
			}
			if sent[1] != "manual run complete" {
				t.Fatalf("sent[1] = %q, want final result", sent[1])
			}
			stored := store.Get("manual1")
			if stored == nil {
				t.Fatal("expected stored job")
			}
			if stored.LastRun.IsZero() {
				t.Fatal("expected LastRun to be set after manual run")
			}
			if stored.LastError != "" {
				t.Fatalf("LastError = %q, want empty", stored.LastError)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for manual run messages, sent=%v", platform.getSent())
}

func TestCronScheduler_RunJobNow_NotFound(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	err = scheduler.RunJobNow("missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("RunJobNow() error = %v, want not found", err)
	}
}

func TestCronScheduler_RunJobNow_ProjectMissingFailsSynchronously(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	job := &CronJob{
		ID:         "missing-project",
		Project:    "ghost",
		SessionKey: "discord:channel-1:user-1",
		CronExpr:   "0 6 * * *",
		Prompt:     "hello",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	err = scheduler.RunJobNow("missing-project")
	if err == nil || !errors.Is(err, ErrCronProjectNotFound) || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("RunJobNow() error = %v, want missing project", err)
	}
}

func TestCronScheduler_RunJobNow_UsesSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "discord"},
	}
	agentSession := newResultAgentSession("snapshot complete")
	agent := &resultAgent{session: agentSession}

	e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.cronScheduler = scheduler
	scheduler.RegisterEngine("test", e)

	job := &CronJob{
		ID:          "snapshot1",
		Project:     "test",
		SessionKey:  "discord:channel-1:user-1",
		CronExpr:    "0 6 * * *",
		Prompt:      "original prompt",
		Description: "Original description",
		Enabled:     true,
		CreatedAt:   time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunJobNow("snapshot1"); err != nil {
		t.Fatalf("RunJobNow() error = %v", err)
	}
	if !store.Update("snapshot1", "prompt", "mutated prompt") {
		t.Fatal("Update(prompt) returned false")
	}
	if !store.Update("snapshot1", "description", "Mutated description") {
		t.Fatal("Update(description) returned false")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent := platform.getSent()
		if len(sent) >= 2 {
			if sent[0] != "⏰ Original description" {
				t.Fatalf("sent[0] = %q, want original description", sent[0])
			}
			if len(agentSession.sentPrompts) != 1 || !strings.Contains(agentSession.sentPrompts[0], "original prompt") {
				t.Fatalf("agent prompts = %#v, want original prompt", agentSession.sentPrompts)
			}
			if strings.Contains(agentSession.sentPrompts[0], "mutated prompt") {
				t.Fatalf("agent prompt used mutated value: %#v", agentSession.sentPrompts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for snapshot run, sent=%v", platform.getSent())
}

func TestCronJob_ExecutionTimeout(t *testing.T) {
	j := &CronJob{}
	if got := j.ExecutionTimeout(); got != defaultCronJobTimeout {
		t.Errorf("nil TimeoutMins: got %v, want %v", got, defaultCronJobTimeout)
	}
	zero := 0
	j.TimeoutMins = &zero
	if got := j.ExecutionTimeout(); got != 0 {
		t.Errorf("TimeoutMins=0: got %v, want 0", got)
	}
	five := 5
	j.TimeoutMins = &five
	if got := j.ExecutionTimeout(); got != 5*time.Minute {
		t.Errorf("TimeoutMins=5: got %v", got)
	}
}

func TestCronScheduler_AddJob_InvalidSessionMode(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(store)
	err = cs.AddJob(&CronJob{
		ID: "x1", Project: "p", SessionKey: "test:1:1",
		CronExpr: "0 6 * * *", Prompt: "hi", SessionMode: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid session_mode")
	}
}

func TestCronJob_UsesNewSessionPerRun(t *testing.T) {
	for _, mode := range []string{"new_per_run", "new-per-run", "NEW_PER_RUN"} {
		j := &CronJob{SessionMode: mode}
		if !j.UsesNewSessionPerRun() {
			t.Errorf("UsesNewSessionPerRun(%q) = false", mode)
		}
	}
	j := &CronJob{SessionMode: "reuse"}
	if j.UsesNewSessionPerRun() {
		t.Error("reuse should not use new session per run")
	}
}

func TestCronJob_JSONLegacyUnmarshal(t *testing.T) {
	raw := `{"id":"1","project":"p","session_key":"t:1:1","cron_expr":"0 6 * * *","prompt":"x","enabled":true}`
	var j CronJob
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		t.Fatal(err)
	}
	if j.SessionMode != "" {
		t.Errorf("legacy JSON: SessionMode = %q, want empty", j.SessionMode)
	}
	if j.TimeoutMins != nil {
		t.Errorf("legacy JSON: TimeoutMins = %v, want nil", j.TimeoutMins)
	}
}

func TestCronScheduler_AddJob_NegativeTimeoutMins(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(store)
	n := -1
	err = cs.AddJob(&CronJob{
		ID: "t1", Project: "p", SessionKey: "test:1:1",
		CronExpr: "0 6 * * *", Prompt: "hi", TimeoutMins: &n,
	})
	if err == nil {
		t.Fatal("expected error for negative timeout_mins")
	}
}

func TestCronScheduler_AddJob_NormalizesSessionMode(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(store)
	job := &CronJob{
		ID: "n1", Project: "p", SessionKey: "test:1:1",
		CronExpr: "0 6 * * *", Prompt: "hi", SessionMode: "new-per-run",
	}
	if err := cs.AddJob(job); err != nil {
		t.Fatal(err)
	}
	if job.SessionMode != "new_per_run" {
		t.Errorf("SessionMode = %q, want new_per_run", job.SessionMode)
	}
}

func TestCronScheduler_UsesNewSession_GlobalDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(store)

	// Test 1: global default is "new_per_run", job has no session_mode set
	cs.SetDefaultSessionMode("new_per_run")
	job := &CronJob{SessionMode: ""}
	if !cs.UsesNewSession(job) {
		t.Error("global new_per_run + job empty: expected UsesNewSession=true")
	}

	// Test 2: per-job "reuse" overrides global "new_per_run"
	job.SessionMode = "reuse"
	if cs.UsesNewSession(job) {
		t.Error("global new_per_run + job reuse: expected UsesNewSession=false")
	}

	// Test 3: per-job "new_per_run" overrides global default (reuse)
	cs.SetDefaultSessionMode("")
	job.SessionMode = "new_per_run"
	if !cs.UsesNewSession(job) {
		t.Error("global reuse + job new_per_run: expected UsesNewSession=true")
	}

	// Test 4: both global and job are default (reuse)
	job.SessionMode = ""
	if cs.UsesNewSession(job) {
		t.Error("global reuse + job empty: expected UsesNewSession=false")
	}
}

func TestCronStore_MarkRun(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	job := &CronJob{
		ID:         "markrun-test",
		Project:    "proj",
		SessionKey: "test:ch1",
		CronExpr:   "0 6 * * *",
		Prompt:     "hello",
		Enabled:    true,
	}
	if err := store.Add(job); err != nil {
		t.Fatal(err)
	}

	// MarkRun should update LastRun
	before := time.Now()
	store.MarkRun("markrun-test", nil)
	after := time.Now()

	updated := store.Get("markrun-test")
	if updated.LastRun.IsZero() {
		t.Error("LastRun should be set after MarkRun")
	}
	if updated.LastRun.Before(before) || updated.LastRun.After(after) {
		t.Error("LastRun should be between before and after MarkRun call")
	}
}

func TestCronStore_ListByProject(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add jobs for different projects
	jobs := []*CronJob{
		{ID: "j1", Project: "proj1", SessionKey: "s1", CronExpr: "0 6 * * *", Prompt: "p1"},
		{ID: "j2", Project: "proj1", SessionKey: "s2", CronExpr: "0 7 * * *", Prompt: "p2"},
		{ID: "j3", Project: "proj2", SessionKey: "s3", CronExpr: "0 8 * * *", Prompt: "p3"},
	}
	for _, j := range jobs {
		j.Enabled = true
		if err := store.Add(j); err != nil {
			t.Fatal(err)
		}
	}

	list := store.ListByProject("proj1")
	if len(list) != 2 {
		t.Errorf("ListByProject(proj1) = %d jobs, want 2", len(list))
	}

	list2 := store.ListByProject("proj2")
	if len(list2) != 1 {
		t.Errorf("ListByProject(proj2) = %d jobs, want 1", len(list2))
	}

	list3 := store.ListByProject("nonexistent")
	if len(list3) != 0 {
		t.Errorf("ListByProject(nonexistent) = %d jobs, want 0", len(list3))
	}
}
