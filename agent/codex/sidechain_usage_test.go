package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeChildRollout(t *testing.T, root, id, parent string, input, cached, output int) {
	t.Helper()
	path := filepath.Join(root, "sessions", "2026", "10", "29", "rollout-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{
		"id": id, "source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": parent}}}}})
	token, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "token_count", "info": map[string]any{"total_token_usage": map[string]any{
			"input_tokens": input, "cached_input_tokens": cached, "output_tokens": output}}}})
	if err := os.WriteFile(path, append(append(metadata, '\n'), append(token, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSidechainRolloutsAreScopedIncrementalAndDurable(t *testing.T) {
	root := t.TempDir()
	var first codexSidechainMeter
	first.begin(root, "parent", false)
	writeChildRollout(t, root, "child", "parent", 3000, 2000, 50)
	writeChildRollout(t, root, "nested", "child", 1000, 500, 20)
	writeChildRollout(t, root, "foreign", "other", 99999, 0, 999)
	first.observe("child", codexTokenUsage{InputTokens: 3000, CachedInputTokens: 2000, OutputTokens: 50})
	got := first.collect()
	if got == nil || got.InputTokens != 1500 || got.CacheReadInputTokens != 2500 || got.OutputTokens != 70 {
		t.Fatalf("missing descendants, foreign usage, or event/rollout double count: %+v", got)
	}
	if got := first.collect(); got != nil {
		t.Fatalf("same snapshot billed twice: %+v", got)
	}
	// A background agent keeps writing after the bridge exits. Restoring the
	// meter must bill this growth, without baselining it away on restart.
	writeChildRollout(t, root, "child", "parent", 3500, 2200, 80)
	var resumed codexSidechainMeter
	resumed.begin(root, "parent", true)
	got = resumed.collect()
	if got == nil || got.InputTokens != 300 || got.CacheReadInputTokens != 200 || got.OutputTokens != 30 {
		t.Fatalf("restart lost background growth or rebilled history: %+v", got)
	}
}

func TestCodexSidechainUpgradeBaselinesOldHistory(t *testing.T) {
	root := t.TempDir()
	writeChildRollout(t, root, "child", "parent", 3000, 2000, 50)
	var meter codexSidechainMeter
	meter.begin(root, "parent", true)
	if got := meter.collect(); got != nil {
		t.Fatalf("old session billed on upgrade: %+v", got)
	}
	writeChildRollout(t, root, "child", "parent", 3500, 2200, 80)
	if got := meter.collect(); got == nil || got.OutputTokens != 30 {
		t.Fatalf("new growth lost: %+v", got)
	}
}

func TestCodexFreshThreadDoesNotBaselineAwayFastChildren(t *testing.T) {
	root := t.TempDir()
	writeChildRollout(t, root, "child", "parent", 1000, 500, 20)
	var meter codexSidechainMeter
	meter.begin(root, "parent", false)
	if got := meter.collect(); got == nil || got.InputTokens != 500 || got.OutputTokens != 20 {
		t.Fatalf("fast child was mistaken for old history: %+v", got)
	}
}
