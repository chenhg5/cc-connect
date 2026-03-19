package core

import (
	"context"
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
	_ = store.Add(job)
	store.SetMute("persist1", true)

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

	_ = store.Add(&CronJob{
		ID: "j1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "daily task", Enabled: true,
		CreatedAt: time.Now(),
	})
	_ = store.Add(&CronJob{
		ID: "j2", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 12 * * *", Prompt: "noon task", Enabled: false, Mute: true,
		CreatedAt: time.Now(),
	})

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	card := e.renderCronCard("test:ch1")
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

	_ = store.Add(&CronJob{
		ID: "h1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	})

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	card := e.renderCronCard("test:ch1")
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

	_ = store.Add(&CronJob{
		ID: "act1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	})

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler
	scheduler.RegisterEngine("test", e)
	_ = scheduler.Start()
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

	_ = store.Add(&CronJob{
		ID: "txt1", Project: "test", SessionKey: "test:ch1",
		CronExpr: "0 6 * * *", Prompt: "task", Enabled: true,
		CreatedAt: time.Now(),
	})

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
