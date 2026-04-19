package claudecode

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Sample /usage panel text matching parseClaudeUsageReport + parseClaudeUsageWindow expectations.
const sampleUsageScreen = `Current session
  10% used
  Resets 11:59pm

Current week
  25% used
  Resets 11:59pm`

func TestParseClaudeUsageReport_EndToEnd(t *testing.T) {
	now := time.Date(2026, 4, 18, 10, 0, 0, 0, time.Local)
	report, err := parseClaudeUsageReport(sampleUsageScreen, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport: %v", err)
	}
	if report.Provider != "claudecode" {
		t.Errorf("Provider = %q", report.Provider)
	}
	if len(report.Buckets) != 1 || report.Buckets[0].Name != "Usage" {
		t.Fatalf("Buckets = %#v", report.Buckets)
	}
	windows := report.Buckets[0].Windows
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(windows))
	}
	if windows[0].Name != "Current session" || windows[0].UsedPercent != 10 {
		t.Errorf("session window: %#v", windows[0])
	}
	if windows[0].WindowSeconds != claudeUsageSessionWindowSeconds {
		t.Errorf("session WindowSeconds = %d", windows[0].WindowSeconds)
	}
	if windows[1].Name != "Current week" || windows[1].UsedPercent != 25 {
		t.Errorf("week window: %#v", windows[1])
	}
	if windows[1].WindowSeconds != claudeUsageWeekWindowSeconds {
		t.Errorf("week WindowSeconds = %d", windows[1].WindowSeconds)
	}
}

func TestNormalizeClaudeUsageText_StripsNoise(t *testing.T) {
	raw := "line  one  \r\n\r\n  line two  \n"
	got := normalizeClaudeUsageText(raw)
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("got %q", got)
	}
}

func TestParseClaudeUsageResetTime_PM(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 4, 18, 9, 0, 0, 0, loc)
	got, err := parseClaudeUsageResetTime("3:04pm", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hour() != 15 || got.Minute() != 4 {
		t.Fatalf("got %v", got)
	}
}

func TestNewClaudeUsageTerminal_WriteAndString(t *testing.T) {
	term := newClaudeUsageTerminal()
	term.Write([]byte("hello\r\nworld"))
	s := term.String()
	if !strings.Contains(s, "hello") || !strings.Contains(s, "world") {
		t.Fatalf("String() = %q", s)
	}
}

func TestClaudeUsageTerminal_Interface(t *testing.T) {
	var _ *claudeUsageTerminal = newClaudeUsageTerminal()
}

// verify optional UsageReporter surface
var _ core.UsageReporter = (*Agent)(nil)
