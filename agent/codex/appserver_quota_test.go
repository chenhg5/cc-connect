package codex

import "testing"

func TestMapAppServerRateLimits_MainAccountQuotaPrecedesModelQuota(t *testing.T) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{LimitID: "main-account"},
		RateLimitsByLimitID: map[string]appServerRateLimitSnapshot{
			"a-model":      {LimitID: "a-model", LimitName: "Model reserve", Secondary: &appServerRateLimitWindow{UsedPercent: 97, WindowDurationMins: 10080}},
			"main-account": {LimitID: "main-account", LimitName: "Account", Primary: &appServerRateLimitWindow{UsedPercent: 23, WindowDurationMins: 300}, Secondary: &appServerRateLimitWindow{UsedPercent: 67, WindowDurationMins: 10080}},
		},
	})
	if len(report.Buckets) != 2 || report.Buckets[0].Name != "Account" {
		t.Fatalf("main account quota must be first: %#v", report.Buckets)
	}
	windows := report.Buckets[0].Windows
	if len(windows) != 2 || windows[0].WindowSeconds != 18000 || windows[0].UsedPercent != 23 || windows[1].WindowSeconds != 604800 || windows[1].UsedPercent != 67 {
		t.Fatalf("account windows = %#v", windows)
	}
	if report.Buckets[1].Name != "Model reserve" {
		t.Fatal("model-specific quota was lost")
	}
}

func TestMapAppServerRateLimits_MissingMainQuotaKeepsSortedFallback(t *testing.T) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{LimitID: "missing"},
		RateLimitsByLimitID: map[string]appServerRateLimitSnapshot{
			"z": {LimitID: "z", Primary: &appServerRateLimitWindow{UsedPercent: 20, WindowDurationMins: 300}},
			"a": {LimitID: "a", Primary: &appServerRateLimitWindow{UsedPercent: 10, WindowDurationMins: 300}},
		},
	})
	if len(report.Buckets) != 2 || report.Buckets[0].Name != "a" || report.Buckets[1].Name != "z" {
		t.Fatalf("fallback order = %#v", report.Buckets)
	}
}
