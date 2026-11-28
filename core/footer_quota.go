package core

import "strings"

// SetShowUsage5h controls the 5-hour quota segment independently of the
// model/context segment. It remains subordinate to the master footer toggle.
func (e *Engine) SetShowUsage5h(show bool) { e.showUsage5h.Store(show) }

// SetShowUsageWeek controls the weekly quota segment; default false.
func (e *Engine) SetShowUsageWeek(show bool) { e.showUsageWeek.Store(show) }

func (e *Engine) replyFooterQuotaText(session AgentSession, agent Agent) string {
	show5h, showWeek := e.showUsage5h.Load(), e.showUsageWeek.Load()
	if !e.replyFooterEnabled || (!show5h && !showWeek) {
		return ""
	}
	return formatReplyFooterQuota(e.replyFooterUsageReport(session, agent), e.i18n, show5h, showWeek)
}

func formatReplyFooterQuota(report *UsageReport, i18n *I18n, show5h, showWeek bool) string {
	if report == nil || i18n == nil {
		return ""
	}
	// Adapters order the main account quota first. Use the first non-empty
	// bucket and exact durations so missing windows are never substituted
	// with another account/model quota or mislabeled as 5h/week.
	for _, bucket := range report.Buckets {
		if len(bucket.Windows) == 0 {
			continue
		}
		var fiveHour, week *UsageWindow
		for i := range bucket.Windows {
			window := &bucket.Windows[i]
			switch window.WindowSeconds {
			case 5 * 60 * 60:
				if show5h && fiveHour == nil {
					fiveHour = window
				}
			case 7 * 24 * 60 * 60:
				if showWeek && week == nil {
					week = window
				}
			}
		}
		var parts []string
		for _, segment := range []struct {
			label  string
			window *UsageWindow
		}{{"5h", fiveHour}, {"week", week}} {
			if segment.window == nil {
				continue
			}
			used := segment.window.UsedPercent
			if used < 0 {
				used = 0
			}
			if used > 100 {
				used = 100
			}
			// Status metadata intentionally uses Codex-style English in every UI language.
			parts = append(parts, i18n.Tf(MsgReplyFooterQuotaLeft, segment.label, 100-used))
		}
		return strings.Join(parts, " · ")
	}
	return ""
}
