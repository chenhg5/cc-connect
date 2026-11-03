package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestTranscriptExists(t *testing.T) {
	configHome := t.TempDir()
	workDir := filepath.Join(configHome, "proj")
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror Claude Code's project layout used by findProjectDir:
	// <configHome>/projects/<encoded work dir>/
	projDir := filepath.Join(configHome, "projects", encodeClaudeProjectKey(absWork))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !transcriptExists(configHome, workDir, sessionID) {
		t.Error("expected transcriptExists=true for a present transcript")
	}
	if transcriptExists(configHome, workDir, "99999999-8888-7777-6666-555555555555") {
		t.Error("expected transcriptExists=false for a pruned transcript")
	}
	// Nothing to verify — must not block the resume.
	if !transcriptExists(configHome, workDir, "") {
		t.Error("expected transcriptExists=true for an empty session ID")
	}
	if !transcriptExists("", workDir, sessionID) {
		t.Error("expected transcriptExists=true when config home is unknown")
	}
}

// TestBuildSpawnEnvConfigHomePrecedence: the resume guard must resolve
// CLAUDE_CONFIG_DIR exactly as the spawned CLI does — the per-project env
// wins over the supervisor's environment (core.MergeEnv drops the base key).
func TestBuildSpawnEnvConfigHomePrecedence(t *testing.T) {
	supervisorHome := t.TempDir()
	projectHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", supervisorHome)

	spawnEnv := buildSpawnEnv(t.TempDir(), []string{"CLAUDE_CONFIG_DIR=" + projectHome}, core.SpawnOptions{})
	if got := configHomeFromEnv(spawnEnv); got != projectHome {
		t.Fatalf("config home = %q, want the per-project value %q", got, projectHome)
	}

	// Without a per-project override the supervisor value applies.
	spawnEnv = buildSpawnEnv(t.TempDir(), nil, core.SpawnOptions{})
	if got := configHomeFromEnv(spawnEnv); got != supervisorHome {
		t.Fatalf("config home = %q, want the supervisor value %q", got, supervisorHome)
	}
}
