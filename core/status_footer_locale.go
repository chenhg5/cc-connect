package core

import (
	"fmt"
	"strings"
	"time"
)

func localizedStatusLines(model, effort string, usage *ContextUsage, i18n *I18n) []string {
	var lines, identity []string
	if model != "" {
		identity = append(identity, i18n.Tf(MsgFooterModel, model))
	}
	if effort != "" {
		name := effort
		if i18n.CurrentLang() != LangEnglish {
			if label := i18n.T(MsgKey("footer_effort_" + effort)); label != "footer_effort_"+effort {
				name = label + "（" + effort + "）"
			}
		}
		identity = append(identity, i18n.Tf(MsgFooterEffort, name))
	}
	if len(identity) > 0 {
		lines = append(lines, strings.Join(identity, " · "))
	}
	if usage == nil {
		return lines
	}
	counts := []string{i18n.Tf(MsgFooterInput, formatStatusTokenCount(usage.InputTokens)), i18n.Tf(MsgFooterOutput, formatStatusTokenCount(usage.OutputTokens))}
	if usage.CachedInputTokens > 0 {
		counts = append(counts, i18n.Tf(MsgFooterCacheRead, formatStatusTokenCount(usage.CachedInputTokens)))
	}
	if usage.CacheCreationInputTokens > 0 {
		counts = append(counts, i18n.Tf(MsgFooterCacheWrite, formatStatusTokenCount(usage.CacheCreationInputTokens)))
	}
	if i18n.CurrentLang() == LangEnglish && len(lines) > 0 {
		lines[len(lines)-1] += " · " + strings.Join(counts, " ")
	} else {
		lines = append(lines, i18n.Tf(MsgFooterTokens, strings.Join(counts, " · ")))
	}
	if usage.ContextWindow > 0 {
		used := usage.UsedTokens
		if used <= 0 {
			used = usage.TotalTokens
		}
		if used <= 0 {
			used = usage.InputTokens + usage.OutputTokens
		}
		pct := min(100, max(0, used*100/usage.ContextWindow))
		lines = append(lines, i18n.Tf(MsgFooterContext, formatStatusTokenCount(used), formatStatusTokenCount(usage.ContextWindow), pct))
	}
	return lines
}

func localizedQuota(report *UsageReport, i18n *I18n, now time.Time) string {
	if report == nil {
		return ""
	}
	var parts []string
	// Use one bucket so different model/service quotas cannot be conflated.
	for _, bucket := range report.Buckets {
		for _, window := range bucket.Windows {
			if window.ResetAtUnix > 0 && window.ResetAtUnix <= now.Unix() {
				continue
			}
			used := min(100, max(0, window.UsedPercent))
			switch window.WindowSeconds {
			case 5 * 60 * 60:
				part := i18n.Tf(MsgFooterFiveHour, used)
				if window.ResetAtUnix > 0 {
					reset := time.Unix(window.ResetAtUnix, 0).In(now.Location())
					_, offset := reset.Zone()
					sign := "+"
					if offset < 0 {
						sign, offset = "-", -offset
					}
					stamp := fmt.Sprintf("%s UTC%s%02d:%02d", reset.Format("01-02 15:04"), sign, offset/3600, offset%3600/60)
					part += i18n.Tf(MsgFooterReset, stamp)
				}
				parts = append(parts, part)
			case 7 * 24 * 60 * 60:
				parts = append(parts, i18n.Tf(MsgFooterWeek, used))
			}
		}
		if len(parts) > 0 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return i18n.Tf(MsgFooterQuota, strings.Join(parts, " · "))
}
