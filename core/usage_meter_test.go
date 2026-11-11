package core

import (
	"path/filepath"
	"testing"
)

func TestUsageMeterRestartAndOutOfOrderSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meter.json")
	meter, restored := NewUsageMeter(path)
	if restored {
		t.Fatal("new meter restored unexpectedly")
	}
	first := TokenUsage{InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30}
	if got := meter.Observe("one", first); got != first {
		t.Fatalf("first: %+v", got)
	}
	meter.Save()
	meter, restored = NewUsageMeter(path)
	if !restored {
		t.Fatal("saved meter not restored")
	}
	if got := meter.Observe("one", TokenUsage{InputTokens: 5}); !got.Empty() {
		t.Fatalf("old snapshot rebilled: %+v", got)
	}
	if got := meter.Observe("one", TokenUsage{InputTokens: 15, OutputTokens: 25, CacheReadInputTokens: 40}); got.InputTokens != 5 || got.OutputTokens != 5 || got.CacheReadInputTokens != 10 {
		t.Fatalf("growth: %+v", got)
	}
	if got := meter.Observe("two", first); got != first {
		t.Fatalf("different request lost: %+v", got)
	}
}

func TestBillableUsageIncludesChildrenWithoutChangingParent(t *testing.T) {
	event := Event{InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 30,
		Sidechain: &SidechainUsage{InputTokens: 1, OutputTokens: 2, CacheReadInputTokens: 3}}
	total := event.BillableUsage()
	if total.InputTokens != 11 || total.OutputTokens != 22 || total.CacheReadInputTokens != 33 {
		t.Fatalf("family usage: %+v", total)
	}
	if event.InputTokens != 10 || event.OutputTokens != 20 {
		t.Fatal("parent context polluted")
	}
}
