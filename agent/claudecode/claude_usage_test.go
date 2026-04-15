package claudecode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSanitizeClaudeUsageOutput_RendersCursorMoves(t *testing.T) {
	raw := "" +
		"\x1b[?2026h\rSettings:\x1b[1CStatus\x1b[1CConfig\x1b[1CUsage (tab to cycle)\r\n" +
		"\x1b]8;;https://code.claude.com/docs/en/security\aSecurity guide\x1b]8;;\a\r\n" +
		"Current session\r\n" +
		"\x1b[32m1% used\x1b[0m\r\n"

	terminal := newClaudeUsageTerminal()
	terminal.Write([]byte(raw))
	got := normalizeClaudeUsageText(terminal.String())
	if strings.Contains(got, "\x1b") {
		t.Fatalf("sanitizeClaudeUsageOutput still contains escape codes: %q", got)
	}
	if !strings.Contains(got, "Settings: Status Config Usage (tab to cycle)") {
		t.Fatalf("sanitized output missing spaced header: %q", got)
	}
	if !strings.Contains(got, "Security guide") {
		t.Fatalf("sanitized output missing link text: %q", got)
	}
}

func TestParseClaudeUsageReport_Success(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
1% used
Resets 3:59pm (Asia/Seoul)

Current week (all models)
2% used
Resets Dec 23, 4:59pm (Asia/Seoul)

Extra usage
Extra usage not enabled
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if report.Provider != "claudecode" {
		t.Fatalf("Provider = %q, want claudecode", report.Provider)
	}
	if len(report.Buckets) != 1 {
		t.Fatalf("Buckets = %d, want 1", len(report.Buckets))
	}
	if len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("Windows = %d, want 2", len(report.Buckets[0].Windows))
	}

	session := report.Buckets[0].Windows[0]
	if session.Name != "Current session" {
		t.Fatalf("session.Name = %q", session.Name)
	}
	if session.WindowSeconds != 18000 {
		t.Fatalf("session.WindowSeconds = %d, want 18000", session.WindowSeconds)
	}
	if session.UsedPercent != 1 {
		t.Fatalf("session.UsedPercent = %d, want 1", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 14340 {
		t.Fatalf("session.ResetAfterSeconds = %d, want 14340", session.ResetAfterSeconds)
	}

	week := report.Buckets[0].Windows[1]
	if week.Name != "Current week" {
		t.Fatalf("week.Name = %q", week.Name)
	}
	if week.WindowSeconds != 604800 {
		t.Fatalf("week.WindowSeconds = %d, want 604800", week.WindowSeconds)
	}
	if week.UsedPercent != 2 {
		t.Fatalf("week.UsedPercent = %d, want 2", week.UsedPercent)
	}
	if week.ResetAfterSeconds != 104340 {
		t.Fatalf("week.ResetAfterSeconds = %d, want 104340", week.ResetAfterSeconds)
	}
}

func TestParseClaudeUsageReport_MissingOptionalFields(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
15% used
Resets 3:00pm (Asia/Seoul)

Current week
47% used
Resets Dec 23, 11:30am (Asia/Seoul)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if report.Email != "" {
		t.Fatalf("Email = %q, want empty", report.Email)
	}
	if report.AccountID != "" {
		t.Fatalf("AccountID = %q, want empty", report.AccountID)
	}
	if report.UserID != "" {
		t.Fatalf("UserID = %q, want empty", report.UserID)
	}
	if report.Plan != "" {
		t.Fatalf("Plan = %q, want empty", report.Plan)
	}
	if len(report.Buckets) != 1 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("unexpected bucket layout: %+v", report.Buckets)
	}
}

func TestParseClaudeUsageReport_UpgradeRequired(t *testing.T) {
	text := "Unknown command: /usage"
	_, err := parseClaudeUsageReport(text, time.Now())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "upgrade") {
		t.Fatalf("err = %v, want upgrade guidance", err)
	}
}

func TestParseClaudeUsageReport_LoginRequired(t *testing.T) {
	text := "Please run `claude auth login` to continue."
	_, err := parseClaudeUsageReport(text, time.Now())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "login") {
		t.Fatalf("err = %v, want login guidance", err)
	}
}

func TestParseClaudeUsageReport_MissingWindowFields(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
Resets 3:00pm (Asia/Seoul)

Current week
47% used
Resets Dec 23, 11:30am (Asia/Seoul)
`)

	_, err := parseClaudeUsageReport(text, now)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "current session") {
		t.Fatalf("err = %v, want current session parse error", err)
	}
}

func TestParseClaudeUsageReport_UnknownResetTimeDoesNotFail(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
14% used
Resets later today somehow

Current week
33% used
Resets Apr 2, 4pm (America/Los_Angeles)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if len(report.Buckets) != 1 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("unexpected bucket layout: %+v", report.Buckets)
	}
	session := report.Buckets[0].Windows[0]
	if session.UsedPercent != 14 {
		t.Fatalf("session.UsedPercent = %d, want 14", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 0 || session.ResetAtUnix != 0 {
		t.Fatalf("session reset should be unknown, got %+v", session)
	}
}

func TestParseClaudeUsageReport_MissingResetTimeDoesNotFail(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
14% used

Current week
33% used
Resets Apr 2, 4pm (America/Los_Angeles)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	session := report.Buckets[0].Windows[0]
	if session.UsedPercent != 14 {
		t.Fatalf("session.UsedPercent = %d, want 14", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 0 || session.ResetAtUnix != 0 {
		t.Fatalf("session reset should be absent, got %+v", session)
	}
}

func TestParseClaudeUsageResetTime_AllowsWholeHourWithTimezone(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)

	resetAt, err := parseClaudeUsageResetTime("2pm (America/Los_Angeles)", now)
	if err != nil {
		t.Fatalf("parseClaudeUsageResetTime returned error: %v", err)
	}

	want := time.Date(2026, time.March, 30, 14, 0, 0, 0, loc)
	if !resetAt.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", resetAt, want)
	}
}

func TestParseClaudeUsageResetTime_AllowsMonthDayWholeHour(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)

	resetAt, err := parseClaudeUsageResetTime("Apr 2, 4pm (America/Los_Angeles)", now)
	if err != nil {
		t.Fatalf("parseClaudeUsageResetTime returned error: %v", err)
	}

	want := time.Date(2026, time.April, 2, 16, 0, 0, 0, loc)
	if !resetAt.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", resetAt, want)
	}
}

func TestAgentGetUsageSmoke(t *testing.T) {
	if os.Getenv("CC_CONNECT_SMOKE_CLAUDE_USAGE") == "" {
		t.Skip("set CC_CONNECT_SMOKE_CLAUDE_USAGE=1 to run")
	}
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skipf("environment not suitable: %v", err)
	}

	a := &Agent{workDir: ".", mode: "plan"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	report, err := a.GetUsage(ctx)
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if report == nil || len(report.Buckets) == 0 {
		t.Fatalf("unexpected empty report: %+v", report)
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}
