package core

import (
	"strings"
	"testing"
	"time"
)

// TestCronExprToHuman_TimezonePrefix: robfig/cron accepts a leading
// CRON_TZ=/TZ= field, but CronExprToHuman only understood 5 fields and fell
// back to echoing the raw expression.
func TestCronExprToHuman_TimezonePrefix(t *testing.T) {
	for _, prefix := range []string{"CRON_TZ=", "TZ="} {
		if got := CronExprToHuman(prefix+"Asia/Shanghai 0 15 * * *", LangChinese); got != "每天 15:00" {
			t.Errorf("%s: got %q, want %q", prefix, got, "每天 15:00")
		}
	}
	// A pinned zone is surfaced next to the human text; plain expressions are untouched.
	if got := cronDisplaySchedule("CRON_TZ=Asia/Shanghai 0 15 * * *", LangEnglish); got != "Daily at 15:00 (Asia/Shanghai)" {
		t.Errorf("got %q", got)
	}
	if got := cronDisplaySchedule("0 15 * * *", LangEnglish); got != "Daily at 15:00" {
		t.Errorf("got %q", got)
	}
}

// TestCronDisplay_UsesScheduleTimezone is the regression test for /cron
// output rendering next/last run of a CRON_TZ job in the process-local zone,
// which made "0 15 * * *" (Asia/Shanghai) show up as e.g. 03:00 on a UTC-4
// host. Both the card and the plain-text list must format in the schedule's
// own zone and say which zone that is.
func TestCronDisplay_UsesScheduleTimezone(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("UTC-4", -4*3600)
	defer func() { time.Local = prev }()

	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expr := "CRON_TZ=Asia/Shanghai 0 15 * * *"
	last, _ := time.Parse(time.RFC3339, "2026-09-19T03:00:00-04:00") // 15:00 in Asia/Shanghai
	if err := store.Add(&CronJob{ID: "display", Project: "test", SessionKey: "test:ch1", CronExpr: expr, Prompt: "task", Enabled: true, LastRun: last}); err != nil {
		t.Fatal(err)
	}
	e := NewEngine("test", &stubAgent{}, nil, "", LangChinese)
	e.cronScheduler = NewCronScheduler(store)
	if err := e.cronScheduler.AddJob(&CronJob{ID: "next", Project: "test", SessionKey: "test:ch1", CronExpr: expr, Prompt: "next", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	loc := cronDisplayLocation(expr)
	if loc.String() != "Asia/Shanghai" {
		t.Fatalf("cronDisplayLocation = %v", loc)
	}
	next := e.cronScheduler.NextRun("next").In(loc)
	if next.Hour() != 15 || next.Minute() != 0 {
		t.Fatalf("next run in schedule zone = %v, want 15:00", next)
	}
	wantNext := "下次执行: " + next.Format(cronTimeFormat(next, time.Now().In(loc)))

	text := e.renderCronCard("test:ch1", "").RenderText()
	for _, want := range []string{"每天 15:00 (Asia/Shanghai)", "09-19 15:00", wantNext} {
		if !strings.Contains(text, want) {
			t.Errorf("card missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "09-19 03:00") {
		t.Errorf("card rendered last run in local zone:\n%s", text)
	}

	p := &stubPlatformEngine{n: "test"}
	e.cmdCronList(p, &Message{SessionKey: "test:ch1"})
	plain := strings.Join(p.getSent(), "\n")
	for _, want := range []string{"每天 15:00 (Asia/Shanghai)", "09-19 15:00", wantNext} {
		if !strings.Contains(plain, want) {
			t.Errorf("list missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "09-19 03:00") {
		t.Errorf("list rendered last run in local zone:\n%s", plain)
	}
}
