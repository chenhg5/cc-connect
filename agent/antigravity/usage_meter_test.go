package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func meterSessionForTest(t *testing.T, resume string) *antigravitySession {
	t.Helper()
	s, err := newAntigravitySession(context.Background(), "agy", nil, t.TempDir(), "gemini-3.8-flash", "yolo", resume, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAntigravityChildLogsDeduplicateAndSurviveRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "child.jsonl")
	line := `{"id":"msg1","type":"gemini","model":"child-model","tokens":{"input":100,"cached":90,"output":20,"thoughts":5}}` + "\n"
	if err := os.WriteFile(path, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	var info agySubagentInfo
	if err := json.Unmarshal([]byte(`{"subagents":[{"conversation_id":"child","log_uri":`+strconv.Quote(path)+`}]}`), &info); err != nil {
		t.Fatal(err)
	}
	s := meterSessionForTest(t, "")
	s.trackSubagentLogs("parent", &info)
	event := s.turnUsageEvent("parent", agyUsage{InputTokens: 200, OutputTokens: 30})
	if sc := event.Sidechain; sc == nil || sc.InputTokens != 10 || sc.OutputTokens != 25 || sc.CacheReadInputTokens != 90 || sc.ByModel["child-model"].OutputTokens != 25 {
		t.Fatalf("bad child usage: %+v", sc)
	}
	if event.UsageIncomplete {
		t.Fatal("available child log marked incomplete")
	}
	if next := s.turnUsageEvent("parent", agyUsage{InputTokens: 200, OutputTokens: 30}); next.Sidechain != nil || next.InputTokens != 0 {
		t.Fatalf("history rebilled: %+v", next)
	}
	line += `{"id":"msg2","type":"gemini","model":"child-model","tokens":{"input":7,"output":8}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	resumed := meterSessionForTest(t, "parent")
	next := resumed.turnUsageEvent("parent", agyUsage{InputTokens: 210, OutputTokens: 35})
	if next.InputTokens != 10 || next.OutputTokens != 5 || next.Sidechain == nil || next.Sidechain.InputTokens != 7 || next.Sidechain.OutputTokens != 8 {
		t.Fatalf("restart usage: %+v", next)
	}
}

func TestAntigravityResumedAggregateWithoutBaselineIsNotBilled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := meterSessionForTest(t, "old-session")
	event := s.turnUsageEvent("old-session", agyUsage{InputTokens: 900000, OutputTokens: 10000})
	if event.InputTokens != 0 || event.OutputTokens != 0 || !event.UsageIncomplete {
		t.Fatalf("old history charged or missing baseline hidden: %+v", event)
	}
}

func TestAntigravityResumedTurnUsesCurrentStepUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := meterSessionForTest(t, "old-session")
	s.turnSteps = map[int]agyUsage{4: {InputTokens: 278, CacheReadTokens: 30214, OutputTokens: 4}}
	event := s.turnUsageEvent("old-session", agyUsage{InputTokens: 30662, CacheReadTokens: 30214, OutputTokens: 8})
	if event.InputTokens != 278 || event.CacheReadInputTokens != 30214 || event.OutputTokens != 4 || event.UsageIncomplete {
		t.Fatalf("current step lost: %+v", event)
	}
}

func TestAntigravityMissingChildLogDoesNotPretendUsageIsComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := meterSessionForTest(t, "")
	s.prepareUsageMeter("parent")
	s.usageMeter.TrackSource("child", filepath.Join(t.TempDir(), "absent.jsonl"))
	event := s.turnUsageEvent("parent", agyUsage{InputTokens: 10})
	if !event.UsageIncomplete || event.Sidechain != nil {
		t.Fatalf("missing child log hidden: %+v", event)
	}
}
