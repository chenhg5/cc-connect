package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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

func TestCodexFirstTurnRestartRetainsUnreportedChildren(t *testing.T) {
	root := t.TempDir()
	var started codexSidechainMeter
	started.begin(root, "parent", false)
	writeChildRollout(t, root, "child", "parent", 1000, 500, 20)
	var resumed codexSidechainMeter
	resumed.begin(root, "parent", true)
	if got := resumed.collect(); got == nil || got.InputTokens != 500 || got.OutputTokens != 20 {
		t.Fatalf("restart before first result lost unreported child usage: %+v", got)
	}
}

// This opt-in test consumes real inference. Keep it distinct from protocol
// fixtures: a missing login, unsupported model or absent child must fail it.
func TestCodexSidechainLiveCLI(t *testing.T) {
	if os.Getenv("CC_CODEX_LIVE_USAGE") != "1" {
		t.Skip("set CC_CODEX_LIVE_USAGE=1 and CC_CODEX_LIVE_MODEL for a live child-usage test")
	}
	model := os.Getenv("CC_CODEX_LIVE_MODEL")
	if model == "" {
		t.Fatal("CC_CODEX_LIVE_MODEL must explicitly select the test model")
	}
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	authHome := os.Getenv("CODEX_HOME")
	if authHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		authHome = filepath.Join(home, ".codex")
	}
	home, workDir := t.TempDir(), t.TempDir()
	if err := os.Symlink(filepath.Join(authHome, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("model = %q\n[features]\nmulti_agent = true\n[agents]\nmax_threads = 2\n", model)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	s, err := newAppServerSession(ctx, cli, nil, "stdio://", workDir, model, "low", "suggest", "", "", "", []string{"CODEX_HOME=" + home}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	prompt := "Use exactly one subagent, with your own model, to reply CHILD_OK without tools. Wait for it to finish, then reply DONE. Do not read files, run commands or spawn any other agents. This tests native child token accounting."
	if err := s.Send(prompt, "live-child-usage", nil, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event, open := <-s.Events():
			if !open {
				t.Fatal("session closed without a metered result")
			}
			if event.Type == core.EventError {
				t.Fatalf("live request failed: %v", event.Error)
			}
			if event.Type != core.EventResult || !event.Done {
				continue
			}
			sc := event.Sidechain
			if sc == nil || sc.InputTokens+sc.CacheReadInputTokens <= 0 || sc.OutputTokens <= 0 || sc.ByModel[model].OutputTokens <= 0 {
				t.Fatalf("live child usage missing: %+v", sc)
			}
			if event.InputTokens+event.CacheReadInputTokens <= 0 || event.OutputTokens <= 0 {
				t.Fatal("parent usage missing")
			}
			var restored codexSidechainMeter
			restored.begin(home, s.CurrentSessionID(), true)
			if replay := restored.collect(); replay != nil {
				t.Fatalf("live usage rebilled after meter restart: %+v", replay)
			}
			t.Logf("live parent input=%d cached=%d output=%d; child=%+v", event.InputTokens, event.CacheReadInputTokens, event.OutputTokens, sc)
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
