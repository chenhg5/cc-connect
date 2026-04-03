package droid

import "testing"

func TestParseDroidUsage(t *testing.T) {
	status := "Plan: Pro\nUser: tester@example.com\n5h usage: 23%"
	cost := "Input tokens: 12.3k\nOutput tokens: 4.5k\n7d usage: 41%"

	report := parseDroidUsage(status, cost)
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.Email != "tester@example.com" {
		t.Fatalf("Email = %q, want tester@example.com", report.Email)
	}
	if report.Plan == "" {
		t.Fatal("Plan should not be empty")
	}
	if len(report.Buckets) == 0 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("windows = %#v, want two windows", report.Buckets)
	}
	if report.Buckets[0].Windows[0].WindowSeconds != 18000 || report.Buckets[0].Windows[0].UsedPercent != 23 {
		t.Fatalf("5h window = %#v, want 18000/23", report.Buckets[0].Windows[0])
	}
	if report.Buckets[0].Windows[1].WindowSeconds != 604800 || report.Buckets[0].Windows[1].UsedPercent != 41 {
		t.Fatalf("7d window = %#v, want 604800/41", report.Buckets[0].Windows[1])
	}
}

func TestExtractDroidResultText(t *testing.T) {
	raw := []byte("{\"type\":\"message\",\"role\":\"assistant\",\"text\":\"A\"}\n{\"type\":\"result\",\"content\":\"B\"}\n")
	got := extractDroidResultText(raw)
	if got != "A\nB" {
		t.Fatalf("extractDroidResultText = %q, want %q", got, "A\\nB")
	}
}

func TestParseDroidUsageFallbackPlan(t *testing.T) {
	report := parseDroidUsage("", "Total cost: $1.23\nInput: 12k\nOutput: 3k")
	if report.Plan == "" {
		t.Fatal("Plan should not be empty when fallback text exists")
	}
}

func TestParseTokenUsageSettings(t *testing.T) {
	data := []byte(`{
  "model": "custom:gpt",
  "tokenUsage": {
    "inputTokens": 100,
    "outputTokens": 20,
    "cacheCreationTokens": 3,
    "cacheReadTokens": 40,
    "thinkingTokens": 8
  }
}`)
	model, summary := parseTokenUsageSettings(data)
	if model != "custom:gpt" {
		t.Fatalf("model = %q, want custom:gpt", model)
	}
	want := "input 100, output 20, thinking 8, cache_read 40, cache_create 3"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}
