package core

import (
	"strings"
	"testing"
	"time"
)

// cachedQuotaSession is a session that already holds a quota snapshot
// (like the Codex app-server session) and exposes it via CachedUsageReporter.
type cachedQuotaSession struct {
	*controllableAgentSession
	cached *UsageReport
}

func (s *cachedQuotaSession) CachedUsage() *UsageReport { return s.cached }

// Values from the 龙娘 rollout token_count event on 2026-10-15:
// primary used 9% (300 min), secondary used 41% (10080 min).
func plusQuotaReport() *UsageReport {
	return &UsageReport{
		Provider: "codex",
		Buckets: []UsageBucket{{
			Name: "codex",
			Windows: []UsageWindow{
				{Name: "Primary", UsedPercent: 9, WindowSeconds: 300 * 60},
				{Name: "Secondary", UsedPercent: 41, WindowSeconds: 10080 * 60},
			},
		}},
	}
}

func codexFooterSession(cached *UsageReport) *cachedQuotaSession {
	return &cachedQuotaSession{
		controllableAgentSession: &controllableAgentSession{
			model:           "gpt-6.1-sol",
			reasoningEffort: "high",
			workDir:         "/tmp/ws",
			contextUsage: &ContextUsage{
				InputTokens:       1200,
				OutputTokens:      2400,
				CachedInputTokens: 99300,
				ContextWindow:     258400,
				UsedTokens:        103300,
			},
		},
		cached: cached,
	}
}

func TestFormatReplyFooterUsage_TwoWindows(t *testing.T) {
	cases := map[Language]string{
		LangChinese: "5h 剩 91% · 周 59%",
		LangEnglish: "5h 91% left · wk 59%",
	}
	for lang, want := range cases {
		if got := formatReplyFooterUsage(plusQuotaReport(), NewI18n(lang)); got != want {
			t.Errorf("lang %s: got %q, want %q", lang, got, want)
		}
	}
}

func TestFormatReplyFooterUsage_NoWindows(t *testing.T) {
	// Relay codex: rate_limits present but primary/secondary null.
	report := &UsageReport{Provider: "codex"}
	if got := formatReplyFooterUsage(report, NewI18n(LangChinese)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuildClaudeStatusLineFooter_AppendsCachedQuota(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangChinese)
	got := e.buildClaudeStatusLineFooter(nil, codexFooterSession(plusQuotaReport()), "/tmp/ws")
	line := strings.Split(got, "\n")[0]
	want := "gpt-6.1-sol · effort:high · out 2.4k · in 1.2k cw 0 cr 99.3k · ctx 40% · 5h 剩 91% · 周 59%"
	if line != want {
		t.Errorf("line 1 = %q\nwant      %q", line, want)
	}
}

func TestBuildClaudeStatusLineFooter_NoQuotaWhenCacheEmpty(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangChinese)
	got := e.buildClaudeStatusLineFooter(nil, codexFooterSession(nil), "/tmp/ws")
	line := strings.Split(got, "\n")[0]
	if !strings.HasSuffix(line, "ctx 40%") {
		t.Errorf("line 1 = %q, want it to end at ctx (no quota)", line)
	}
}

// Claude Code sessions do not implement CachedUsageReporter; even when the
// session can report usage over the network, the footer must stay as it was.
func TestBuildClaudeStatusLineFooter_ClaudeSessionUnchanged(t *testing.T) {
	session := &controllableAgentSession{
		model:   "claude-opus-4-7[1m]",
		workDir: "/tmp/ws",
		report:  plusQuotaReport(),
		contextUsage: &ContextUsage{
			InputTokens:              1,
			OutputTokens:             168,
			CacheCreationInputTokens: 971,
			CachedInputTokens:        40800,
			ContextWindow:            1_000_000,
			UsedTokens:               1 + 971 + 40800,
		},
	}
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangChinese)
	got := strings.Split(e.buildClaudeStatusLineFooter(nil, session, "/tmp/ws"), "\n")[0]
	want := "claude-opus-4-7[1m] · out 168 · in 1 cw 971 cr 40.8k · ctx 4%"
	if got != want {
		t.Errorf("line 1 = %q, want %q", got, want)
	}
}

func TestBuildReplyFooter_ContextThenCachedQuota(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangChinese)
	session := codexFooterSession(plusQuotaReport())
	got := e.buildReplyFooter(&stubModelModeAgent{}, session, "/tmp/ws", "[ctx: ~40%]")
	if !strings.HasPrefix(got, "[ctx: ~40%] · gpt-6.1-sol · high · 5h 剩 91% · 周 59%") {
		t.Errorf("footer = %q", got)
	}
}

func TestComposeRichStatusFooter_AppendsCachedQuota(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangChinese)
	got := e.composeRichStatusFooter(false, time.Now(), &stubModelModeAgent{}, codexFooterSession(plusQuotaReport()), "/tmp/ws")
	if !strings.Contains(got, "ctx 39% · 5h 剩 91% · 周 59%") {
		t.Errorf("rich footer = %q", got)
	}
}
