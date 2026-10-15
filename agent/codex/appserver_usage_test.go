package codex

import (
	"encoding/json"
	"testing"
)

func decodeRateLimits(t *testing.T, raw string) appServerRateLimitsResponse {
	t.Helper()
	var n appServerRateLimitsResponse
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Plus account (龙娘): both windows arrive; values from the 2026-10-15 rollout.
const plusRateLimits = `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":9,"windowDurationMins":300,"resetsAt":1792051363},"secondary":{"usedPercent":41,"windowDurationMins":10080,"resetsAt":1792479505},"planType":"plus"}}`

// Relay (鲸鱼娘): every rate_limits field is null in all 467 token_count
// events under ~/.codex-relay/sessions.
const relayRateLimits = `{"rateLimits":{"limitId":"codex","limitName":null,"primary":null,"secondary":null,"credits":null,"planType":null}}`

func TestCachedUsage_PlusNotification(t *testing.T) {
	s := &appServerSession{}
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, plusRateLimits)))
	r := s.CachedUsage()
	if r == nil || len(r.Buckets) != 1 || len(r.Buckets[0].Windows) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if w := r.Buckets[0].Windows; w[0].UsedPercent != 9 || w[0].WindowSeconds != 18000 || w[1].UsedPercent != 41 || w[1].WindowSeconds != 604800 {
		t.Errorf("windows = %+v", w)
	}
}

func TestCachedUsage_RelayNullsGiveNothing(t *testing.T) {
	s := &appServerSession{}
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, relayRateLimits)))
	if r := s.CachedUsage(); r != nil {
		t.Fatalf("relay should report no quota, got %+v", r)
	}
}

// A sparse update with only the primary window must keep the weekly one.
func TestMergeUsage_SparseUpdateKeepsOtherWindow(t *testing.T) {
	s := &appServerSession{}
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, plusRateLimits)))
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":12,"windowDurationMins":300}}}`)))
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, relayRateLimits)))
	r := s.CachedUsage()
	if r == nil || r.Plan != "plus" {
		t.Fatalf("report = %+v", r)
	}
	w := r.Buckets[0].Windows
	if len(w) != 2 || w[0].UsedPercent != 12 || w[1].UsedPercent != 41 {
		t.Errorf("windows = %+v", w)
	}
}

// The session-start read names the bucket (limitName) while rolling updates
// carry only limitId; they must land in the same bucket.
func TestMergeUsage_MatchesByLimitIDNotName(t *testing.T) {
	s := &appServerSession{}
	s.storeUsage(mapAppServerRateLimits(decodeRateLimits(t, `{"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":"Codex","primary":{"usedPercent":9,"windowDurationMins":300},"secondary":{"usedPercent":41,"windowDurationMins":10080},"planType":"plus"}}}`)))
	s.mergeUsage(mapAppServerRateLimits(decodeRateLimits(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":15,"windowDurationMins":300},"secondary":{"usedPercent":43,"windowDurationMins":10080}}}`)))
	r := s.CachedUsage()
	if r == nil || len(r.Buckets) != 1 {
		t.Fatalf("want one bucket, got %+v", r)
	}
	b := r.Buckets[0]
	if b.ID != "codex" || b.Name != "Codex" {
		t.Errorf("bucket id/name = %q/%q", b.ID, b.Name)
	}
	if b.Windows[0].UsedPercent != 15 || b.Windows[1].UsedPercent != 43 {
		t.Errorf("windows not updated: %+v", b.Windows)
	}
}

func TestMapAppServerRateLimits_ByLimitIDKeyFillsID(t *testing.T) {
	r := mapAppServerRateLimits(decodeRateLimits(t, `{"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":1,"windowDurationMins":300}}}}`))
	if len(r.Buckets) != 1 || r.Buckets[0].ID != "codex" {
		t.Fatalf("buckets = %+v", r.Buckets)
	}
}
