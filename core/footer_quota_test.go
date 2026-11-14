package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func footerQuotaReport() *UsageReport {
	return &UsageReport{Buckets: []UsageBucket{{Name: "account", Windows: []UsageWindow{
		{Name: "primary", WindowSeconds: 18000, UsedPercent: 23},
		{Name: "secondary", WindowSeconds: 604800, UsedPercent: 67},
	}}}}
}

func TestFormatReplyFooterQuota_WindowsAndMissingData(t *testing.T) {
	for _, tc := range []struct {
		name           string
		report         *UsageReport
		fiveHour, week bool
		want           string
	}{
		{"both", footerQuotaReport(), true, true, "5h 77% left · week 33% left"},
		{"5h only", footerQuotaReport(), true, false, "5h 77% left"},
		{"week only", footerQuotaReport(), false, true, "week 33% left"},
		{"disabled", footerQuotaReport(), false, false, ""},
		{"no data", nil, true, true, ""},
		{"unknown duration", &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 3600, UsedPercent: 50}}}}}, true, true, ""},
		{"weekly is not 5h", &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 604800, UsedPercent: 12}}}}}, true, true, "week 88% left"},
		{"clamp", &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 18000, UsedPercent: -1}, {WindowSeconds: 604800, UsedPercent: 120}}}}}, true, true, "5h 100% left · week 0% left"},
		{"keep account bucket", &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 18000, UsedPercent: 1}}}, {Windows: []UsageWindow{{WindowSeconds: 604800, UsedPercent: 99}}}}}, true, true, "5h 99% left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatReplyFooterQuota(tc.report, NewI18n(LangEnglish), tc.fiveHour, tc.week)
			if got != tc.want {
				t.Fatalf("quota = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatReplyFooterQuota_EnglishRemainingInEveryLanguage(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		got := formatReplyFooterQuota(footerQuotaReport(), NewI18n(lang), true, true)
		if legacy := formatReplyFooterUsage(footerQuotaReport(), NewI18n(lang)); legacy != "77% left" {
			t.Fatalf("%s legacy quota = %q", lang, legacy)
		}
		if got != "5h 77% left · week 33% left" {
			t.Fatalf("%s quota = %q", lang, got)
		}
	}
}

func TestFormatReplyFooterQuota_MissingWeekDoesNotUseModelQuota(t *testing.T) {
	report := &UsageReport{Buckets: []UsageBucket{
		{Windows: []UsageWindow{{WindowSeconds: 18000, UsedPercent: 20}}},
		{Windows: []UsageWindow{{WindowSeconds: 604800, UsedPercent: 90}}},
	}}
	if got := formatReplyFooterQuota(report, NewI18n(LangEnglish), false, true); got != "" {
		t.Fatalf("main account has no week quota: %q", got)
	}
}

type quotaFooterSession struct {
	*controllableAgentSession
	report *UsageReport
	err    error
	calls  int
}

func (s *quotaFooterSession) GetUsage(context.Context) (*UsageReport, error) {
	s.calls++
	return s.report, s.err
}

func TestReplyFooterQuota_AllLayoutsAndToggles(t *testing.T) {
	e := newLegacyFooterEngine()
	e.ctx = context.Background()
	e.i18n = NewI18n(LangEnglish)
	a := &stubFooterAgent{model: "model", effort: "high"}
	s := &quotaFooterSession{controllableAgentSession: newControllableSession("quota"), report: footerQuotaReport()}
	s.contextUsage = &ContextUsage{ContextWindow: 10000, UsedTokens: 2000, CachedInputTokens: 100, InputTokens: 1900}
	layouts := []struct {
		name   string
		render func() string
	}{
		{"legacy", func() string { return e.buildReplyFooter(a, s, "", "ctx 20%") }},
		{"rich", func() string { return e.composeRichStatusFooter(false, time.Now(), a, s, "") }},
		{"cache token footer", func() string { return e.buildClaudeStatusLineFooter(a, s, "") }},
	}
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			for _, flags := range []struct{ master, context, fiveHour, week bool }{
				{true, true, false, false}, {true, true, true, true}, {true, true, true, false}, {true, false, false, true}, {false, true, true, true},
			} {
				e.SetReplyFooterEnabled(flags.master)
				e.SetShowContextIndicator(flags.context)
				e.SetShowUsage5h(flags.fiveHour)
				e.SetShowUsageWeek(flags.week)
				before := s.calls
				text := layout.render()
				if strings.Contains(text, "5h 77% left") != (flags.master && flags.fiveHour) || strings.Contains(text, "week 33% left") != (flags.master && flags.week) {
					t.Fatalf("flags=%+v footer=%q", flags, text)
				}
				if (!flags.master || (!flags.fiveHour && !flags.week)) && s.calls != before {
					t.Fatal("disabled quotas fetched account usage")
				}
			}
		})
	}
}

func TestReplyFooterQuota_SessionPrecedenceAndCachedFallback(t *testing.T) {
	e := newLegacyFooterEngine()
	e.ctx = context.Background()
	e.i18n = NewI18n(LangEnglish)
	e.SetShowUsage5h(true)
	e.SetShowUsageWeek(true)
	a := &stubReplyFooterAgent{report: footerQuotaReport()}
	s := &quotaFooterSession{controllableAgentSession: newControllableSession("quota"), report: &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 604800, UsedPercent: 9}}}}}}
	if got := e.replyFooterQuotaText(s, a); got != "week 91% left" {
		t.Fatalf("session quota = %q", got)
	}
	s.err = errors.New("session usage unavailable")
	if got := e.replyFooterQuotaText(s, a); got != "5h 77% left · week 33% left" {
		t.Fatalf("agent fallback = %q", got)
	}
	// A failed refresh keeps the last successful snapshot, formatted using the
	// current display flags instead of caching a preformatted footer string.
	e.replyFooterUsage.fetchedAt = time.Now().Add(-time.Hour)
	a.err = errors.New("offline")
	e.SetShowUsage5h(false)
	if got := e.replyFooterQuotaText(s, a); got != "week 33% left" {
		t.Fatalf("cached fallback = %q", got)
	}
}
