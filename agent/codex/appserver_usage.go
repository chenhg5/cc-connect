package codex

import "github.com/chenhg5/cc-connect/core"

// CachedUsage implements core.CachedUsageReporter: the rate-limit snapshot the
// app-server already pushed (account/rateLimits/updated after each model
// response, plus the read at session start). No I/O, so the reply footer can
// show it on every reply. Returns nil when no window is known — e.g. a relay
// provider whose token_count rate_limits are all null.
func (s *appServerSession) CachedUsage() *core.UsageReport {
	report := s.cachedUsage()
	if report == nil {
		return nil
	}
	for _, bucket := range report.Buckets {
		if len(bucket.Windows) > 0 {
			return report
		}
	}
	return nil
}

// mergeUsage folds a rolling account/rateLimits/updated notification into the
// cached snapshot. The app-server schema documents these updates as sparse
// ("clients should merge available values into the most recent
// account/rateLimits/read response"), so a missing window or plan must not
// erase the one we already have.
func (s *appServerSession) mergeUsage(update *core.UsageReport) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.usage = mergeUsageReports(s.usage, update)
}

func mergeUsageReports(base, update *core.UsageReport) *core.UsageReport {
	if update == nil {
		return cloneUsageReport(base)
	}
	if base == nil {
		return cloneUsageReport(update)
	}
	merged := cloneUsageReport(base)
	if update.Plan != "" {
		merged.Plan = update.Plan
	}
	if update.Credits != nil {
		credits := *update.Credits
		merged.Credits = &credits
	}
	for _, ub := range update.Buckets {
		idx := -1
		for i := range merged.Buckets {
			if merged.Buckets[i].Name == ub.Name {
				idx = i
				break
			}
		}
		if idx < 0 {
			ub.Windows = append([]core.UsageWindow(nil), ub.Windows...)
			merged.Buckets = append(merged.Buckets, ub)
			continue
		}
		dst := &merged.Buckets[idx]
		for _, uw := range ub.Windows {
			replaced := false
			for j := range dst.Windows {
				if dst.Windows[j].Name == uw.Name {
					dst.Windows[j] = uw
					replaced = true
					break
				}
			}
			if !replaced {
				dst.Windows = append(dst.Windows, uw)
			}
		}
		limitReached := false
		for _, w := range dst.Windows {
			if w.UsedPercent >= 100 { // percent scale, same rule as mapAppServerRateLimits
				limitReached = true
			}
		}
		dst.LimitReached = limitReached
		dst.Allowed = !limitReached
	}
	return merged
}
