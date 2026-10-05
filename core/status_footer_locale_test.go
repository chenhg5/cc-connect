package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRichFooter_ModelDoesNotHideQuota(t *testing.T) {
	e := newLegacyFooterEngine()
	e.i18n, e.ctx = NewI18n(LangEnglish), context.Background()
	s := newControllableSession("footer")
	s.model, s.reasoningEffort = "runtime-model", "xhigh"
	s.contextUsage = &ContextUsage{InputTokens: 127200, CachedInputTokens: 124700, OutputTokens: 0, UsedTokens: 127200, ContextWindow: 258400}
	s.report = &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{
		{WindowSeconds: 18000, UsedPercent: 34, ResetAtUnix: time.Now().Add(time.Hour).Unix()},
		{WindowSeconds: 604800, UsedPercent: 29, ResetAtUnix: time.Now().Add(24 * time.Hour).Unix()},
	}}}}
	for _, tc := range []struct {
		lang Language
		want []string
	}{
		{LangChinese, []string{"模型：runtime-model", "推理：极高（xhigh）", "输入 127.2k", "输出 0", "缓存命中 124.7k", "127.2k / 258.4k（已用 49%）", "五小时已用 34%", "本周已用 29%", "UTC", "工作目录："}},
		{LangEnglish, []string{"runtime-model · xhigh", "in 127.2k out 0 cr 124.7k", "ctx 127.2k/258.4k (49%)", "5h used 34%", "wk used 29%", "reset"}},
	} {
		e.i18n.SetLang(tc.lang)
		got := e.composeRichStatusFooter(false, time.Now(), e.agent, s, "D:/项目")
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s missing %q: %s", tc.lang, want, got)
			}
		}
	}
	if got := e.composeRichStatusFooter(true, time.Now(), e.agent, s, ""); got != "" {
		t.Errorf("streaming footer = %s", got)
	}
	e.SetShowContextIndicator(false)
	if got := e.composeRichStatusFooter(false, time.Now(), e.agent, s, ""); strings.Contains(got, "34%") || strings.Contains(got, "runtime-model") {
		t.Errorf("context toggle ignored: %s", got)
	}
	e.SetReplyFooterEnabled(false)
	if got := e.composeRichStatusFooter(false, time.Now(), e.agent, s, ""); got != "" {
		t.Errorf("master toggle ignored: %s", got)
	}
}

func TestQuota_ExpiredWindowsAndUnknownDurationsHidden(t *testing.T) {
	now := time.Date(2026, 10, 4, 22, 0, 0, 0, time.FixedZone("local", 8*3600))
	report := &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{
		{WindowSeconds: 18000, UsedPercent: 120, ResetAtUnix: now.Add(time.Minute).Unix()},
		{WindowSeconds: 604800, UsedPercent: -5, ResetAtUnix: now.Add(time.Hour).Unix()},
	}}}}
	got := localizedQuota(report, NewI18n(LangEnglish), now)
	if !strings.Contains(got, "5h used 100% · reset 10-04 22:01 UTC+08:00") || !strings.Contains(got, "wk used 0%") {
		t.Fatal(got)
	}
	for i := range report.Buckets[0].Windows {
		report.Buckets[0].Windows[i].ResetAtUnix = now.Unix()
	}
	if got := localizedQuota(report, NewI18n(LangChinese), now); got != "" {
		t.Fatalf("expired quota shown: %s", got)
	}
	report.Buckets[0].Windows = []UsageWindow{{WindowSeconds: 60, UsedPercent: 10}}
	if got := localizedQuota(report, NewI18n(LangEnglish), now); got != "" {
		t.Fatal(got)
	}
}

type localeUsageAgent struct {
	*stubFooterAgent
	calls int
	fail  bool
}

func (a *localeUsageAgent) GetUsage(ctx context.Context) (*UsageReport, error) {
	a.calls++
	if a.fail {
		<-ctx.Done()
		return nil, errors.New("timed out")
	}
	return &UsageReport{Buckets: []UsageBucket{{Windows: []UsageWindow{{WindowSeconds: 18000, UsedPercent: 17}}}}}, nil
}

func TestQuota_CachesRawDataAcrossLanguagesAndHidesTimeout(t *testing.T) {
	e := newLegacyFooterEngine()
	e.i18n, e.ctx = NewI18n(LangEnglish), context.Background()
	a := &localeUsageAgent{stubFooterAgent: &stubFooterAgent{}}
	zh := e.replyFooterUsageText(nil, a, NewI18n(LangChinese))
	en := e.replyFooterUsageText(nil, a, NewI18n(LangEnglish))
	if a.calls != 1 || !strings.Contains(zh, "五小时已用 17%") || en != "5h used 17%" {
		t.Fatalf("calls=%d zh=%s en=%s", a.calls, zh, en)
	}
	e.replyFooterUsage.fetchedAt = time.Now().Add(-31 * time.Second)
	a.fail = true
	start := time.Now()
	if got := e.replyFooterUsageText(nil, a); got != "" {
		t.Fatalf("stale quota shown: %s", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("quota delayed reply beyond timeout")
	}
}

func TestFooterTranslations_AllFiveLanguages(t *testing.T) {
	for key, translations := range messages {
		if !strings.HasPrefix(string(key), "footer_") && !strings.HasPrefix(string(key), "rich_") {
			continue
		}
		for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			if translations[lang] == "" {
				t.Errorf("%s missing %s", key, lang)
			}
		}
	}
}
