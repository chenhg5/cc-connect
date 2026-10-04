package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type managedUsageSnapshot struct {
	appServerRateLimitSnapshot
	NormalModelSlug      string `json:"normalModelSlug"`
	RateLimitReachedType string `json:"rateLimitReachedType"`
	SpendControlReached  *bool  `json:"spendControlReached"`
	IndividualLimit      *struct {
		RemainingPercent float64 `json:"remainingPercent"`
		ResetsAt         int64   `json:"resetsAt"`
	} `json:"individualLimit"`
}

// Rolling notifications are sparse: unavailable/null fields retain prior values.
func mergeManagedUsage(old, update managedUsageSnapshot) managedUsageSnapshot {
	if update.LimitID != "" {
		old.LimitID = update.LimitID
	}
	if update.LimitName != "" {
		old.LimitName = update.LimitName
	}
	if update.PlanType != "" {
		old.PlanType = update.PlanType
	}
	if update.NormalModelSlug != "" {
		old.NormalModelSlug = update.NormalModelSlug
	}
	if update.Primary != nil {
		old.Primary = update.Primary
	}
	if update.Secondary != nil {
		old.Secondary = update.Secondary
	}
	if update.Credits != nil {
		old.Credits = update.Credits
	}
	if update.RateLimitReachedType != "" {
		old.RateLimitReachedType = update.RateLimitReachedType
	}
	if update.SpendControlReached != nil {
		old.SpendControlReached = update.SpendControlReached
	}
	if update.IndividualLimit != nil {
		old.IndividualLimit = update.IndividualLimit
	}
	if update.RateLimitReachedType == "" && update.SpendControlReached != nil && !*update.SpendControlReached {
		switch old.RateLimitReachedType {
		case "workspace_owner_usage_limit_reached", "workspace_member_usage_limit_reached":
			old.RateLimitReachedType = ""
		}
	}
	// A non-null reason is an explicit limit signal. Fresh rolling windows
	// without a reason can establish recovery from a rolling-window limit.
	if update.RateLimitReachedType == "" && (old.RateLimitReachedType == "rate_limit_reached" || old.RateLimitReachedType == "token_limit_reached") && (update.Primary != nil || update.Secondary != nil) {
		if (old.Primary == nil || old.Primary.UsedPercent < 100) && (old.Secondary == nil || old.Secondary.UsedPercent < 100) {
			old.RateLimitReachedType = ""
		}
	}
	return old
}

func managedUsageReport(snapshot managedUsageSnapshot) (*core.UsageReport, bool) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{RateLimits: snapshot.appServerRateLimitSnapshot})
	reached := snapshot.RateLimitReachedType != "" || (snapshot.SpendControlReached != nil && *snapshot.SpendControlReached)
	if snapshot.IndividualLimit != nil && snapshot.IndividualLimit.RemainingPercent <= 0 {
		reached = true
	}
	if len(report.Buckets) == 0 {
		report.Buckets = append(report.Buckets, core.UsageBucket{Name: appServerBucketName(snapshot.appServerRateLimitSnapshot), Allowed: true})
	}
	bucket := &report.Buckets[0]
	reached = reached || bucket.LimitReached
	bucket.LimitReached = reached
	bucket.Allowed = !reached
	if snapshot.NormalModelSlug != "" {
		bucket.Name = snapshot.NormalModelSlug
	}
	if snapshot.IndividualLimit != nil {
		bucket.Windows = append(bucket.Windows, core.UsageWindow{UsedPercent: int(100 - snapshot.IndividualLimit.RemainingPercent), ResetAtUnix: snapshot.IndividualLimit.ResetsAt})
	}
	return report, reached
}

func (s *managedSession) handleUsageUpdate(raw json.RawMessage) error {
	var payload struct {
		RateLimits managedUsageSnapshot `json:"rateLimits"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	update := payload.RateLimits
	key := update.LimitID
	if key == "" {
		key = "default"
	}
	s.mu.Lock()
	if s.usageSnapshots == nil {
		s.usageSnapshots = map[string]managedUsageSnapshot{}
	}
	snapshot := mergeManagedUsage(s.usageSnapshots[key], update)
	s.usageSnapshots[key] = snapshot
	combined := &core.UsageReport{Provider: "codex"}
	for _, snap := range s.usageSnapshots {
		report, _ := managedUsageReport(snap)
		combined.Buckets = append(combined.Buckets, report.Buckets...)
		if report.Credits != nil {
			combined.Credits = report.Credits
		}
		if report.Plan != "" {
			combined.Plan = report.Plan
		}
	}
	report, reached := managedUsageReport(snapshot)
	if !reached && s.noticeKeys != nil && usageBelowWarning(report) {
		delete(s.noticeKeys, "usage:"+key)
	}
	if !reached && usageBelowWarning(report) {
		for warningKey := range s.usageWarningLevels {
			if warningKey == key || strings.HasPrefix(warningKey, key+"/") {
				delete(s.usageWarningLevels, warningKey)
			}
		}
	}
	s.mu.Unlock()
	s.decoder.storeUsage(combined)
	if !reached {
		threshold := 0
		for _, window := range report.Buckets[0].Windows {
			for _, level := range []int{50, 75, 90, 95} {
				if window.UsedPercent >= level && level > threshold {
					threshold = level
				}
			}
		}
		windowKey := key
		for _, window := range report.Buckets[0].Windows {
			windowKey += fmt.Sprintf("/%d", window.ResetAtUnix)
		}
		s.mu.Lock()
		if s.usageWarningLevels == nil {
			s.usageWarningLevels = map[string]int{}
		}
		if threshold <= s.usageWarningLevels[windowKey] {
			threshold = 0
		} else {
			if len(s.usageWarningLevels) >= 256 {
				s.usageWarningLevels = map[string]int{}
			}
			s.usageWarningLevels[windowKey] = threshold
		}
		s.mu.Unlock()
		if threshold > 0 {
			s.emitManagedNotice("usage:"+key, "", &core.AgentNotice{Kind: "usage_warning", Usage: report, UsageThreshold: threshold})
		}
	}
	if reached {
		s.emitManagedNotice("usage:"+key, "", &core.AgentNotice{Kind: "usage_limit", Code: snapshot.RateLimitReachedType, Usage: report})
	}
	return nil
}

func (s *managedSession) emitManagedNotice(key, turnID string, notice *core.AgentNotice) {
	if notice.Message == "" && notice.Usage == nil && notice.ToModel == "" {
		return
	}
	// ResetAfterSeconds and changing percentages must not turn one exhausted
	// quota window into repeated alerts. Recovery explicitly clears its key.
	signature := fmt.Sprint(notice.UsageThreshold) + notice.Kind + notice.Code + notice.Message + notice.FromModel + notice.ToModel
	if notice.Usage != nil {
		for _, bucket := range notice.Usage.Buckets {
			signature += bucket.Name
			for _, window := range bucket.Windows {
				signature += fmt.Sprintf("/%d", window.ResetAtUnix)
			}
		}
	}
	s.mu.Lock()
	if s.noticeKeys == nil {
		s.noticeKeys = map[string]string{}
	}
	if s.noticeKeys[key] == signature {
		s.mu.Unlock()
		return
	}
	if len(s.noticeKeys) >= 256 {
		s.noticeKeys = map[string]string{}
	}
	s.noticeKeys[key] = signature
	s.mu.Unlock()
	s.emit(core.Event{Type: core.EventNotice, TurnID: turnID, Notice: notice})
}

func usageBelowWarning(report *core.UsageReport) bool {
	for _, bucket := range report.Buckets {
		for _, window := range bucket.Windows {
			if window.UsedPercent >= 50 {
				return false
			}
		}
	}
	return true
}
