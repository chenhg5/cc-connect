package core

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestScheduledExecutionLogsDoNotExposePrompt(t *testing.T) {
	const secret = "SCHEDULED-PROMPT-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cronStore, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cronJob := &CronJob{ID: "redact-cron", Project: "test", SessionKey: "missing:1", CronExpr: "0 0 * * *", Prompt: secret, Enabled: true}
	if err := cronStore.Add(cronJob); err != nil {
		t.Fatal(err)
	}
	cronScheduler := NewCronScheduler(cronStore)
	cronScheduler.RegisterEngine("test", NewEngine("test", nil, nil, t.TempDir(), LangEnglish))
	cronScheduler.runJob(cronJob, true)

	timerStore, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	timerJob := &TimerJob{ID: "redact-timer", Project: "test", SessionKey: "missing:1", ScheduledAt: time.Now(), Prompt: secret}
	if err := timerStore.Add(timerJob); err != nil {
		t.Fatal(err)
	}
	timerScheduler := NewTimerScheduler(timerStore)
	timerScheduler.RegisterEngine("test", NewEngine("test", nil, nil, t.TempDir(), LangEnglish))
	timerScheduler.executeJob(timerJob.ID)

	if strings.Contains(logs.String(), secret) {
		t.Fatal("scheduled execution log exposed prompt")
	}
	if strings.Count(logs.String(), "prompt_len=") < 2 {
		t.Fatal("scheduled execution log lost prompt length")
	}
}
