package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeCwdTranscript creates <home>/.claude/projects/<encodeClaudeProjectKey(cwd)>/<id>.jsonl,
// mirroring where Claude Code writes a session's transcript for a given cwd.
func writeCwdTranscript(t *testing.T, home, cwd, sessionID string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectKey(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestValidateSessionID_UsesStoredCwd: with a real cwd supplied, validation finds
// the transcript under the cwd's project dir even though it is a subdirectory of
// work_dir that findProjectDir(work_dir) would never construct — the resume bug.
func TestValidateSessionID_UsesStoredCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const workDir = "/workspace"
	const cwd = "/workspace/.sessions/abc123/owner-repo"
	const id = "sess-uses-cwd"
	writeCwdTranscript(t, home, cwd, id)

	a := &Agent{workDir: workDir}
	if !a.ValidateSessionID(context.Background(), id, cwd) {
		t.Errorf("with stored cwd %q, ValidateSessionID = false, want true", cwd)
	}
	// Without the cwd it falls back to work_dir, whose project dir holds no such
	// file — the pre-fix behavior, and precisely why capturing cwd is needed.
	if a.ValidateSessionID(context.Background(), id, "") {
		t.Errorf("empty cwd fell back to work_dir but returned true; transcript is only under the subdir")
	}
}

// TestValidateSessionID_EmptyCwdFallsBackToWorkDir: records with no captured cwd
// still validate against work_dir (backward compatibility — R4).
func TestValidateSessionID_EmptyCwdFallsBackToWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const workDir = "/workspace"
	const id = "sess-fallback"
	writeCwdTranscript(t, home, workDir, id)

	a := &Agent{workDir: workDir}
	if !a.ValidateSessionID(context.Background(), id, "") {
		t.Errorf("empty cwd with transcript under work_dir: got false, want true (fallback)")
	}
}

// TestValidateSessionID_CrossProjectSharedRow models the real issue-#599 vector:
// a Session row shared across two projects. Project A created the session, so the
// row carries A's session id AND A's cwd. Project B (a different work_dir) resumes
// the shared row, so validation runs with A's stored cwd. Because A's cwd is not
// within B's work_dir it is not trusted; validation falls back to B's work_dir,
// where A's transcript does not exist -> reject. The guard holds.
func TestValidateSessionID_CrossProjectSharedRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const projectA = "/work/projectA"
	const projectB = "/work/projectB"
	const id = "sess-shared-row"
	writeCwdTranscript(t, home, projectA, id) // transcript only under project A

	a := &Agent{workDir: projectB}
	if a.ValidateSessionID(context.Background(), id, projectA) {
		t.Errorf("stored cwd from project A validated under project B's work_dir (#599 leak)")
	}
}

// TestValidateSessionID_CwdOutsideWorkDirNotTrusted: a reported cwd outside the
// agent's work_dir is not trusted for locating the transcript (it cannot be told
// apart from a leaked foreign cwd, #599), so validation falls back to work_dir.
func TestValidateSessionID_CwdOutsideWorkDirNotTrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const workDir = "/workspace"
	const outside = "/somewhere/else"
	const id = "sess-outside"
	writeCwdTranscript(t, home, outside, id) // transcript only under the outside cwd

	a := &Agent{workDir: workDir}
	if a.ValidateSessionID(context.Background(), id, outside) {
		t.Errorf("cwd outside work_dir was trusted; must fall back to work_dir (#599 safety)")
	}
}

// TestClaudeSession_CapturesCwdFromInit: handleSystem records the cwd reported in
// the CLI init event; an init event without cwd leaves it empty (defensive).
func TestClaudeSession_CapturesCwdFromInit(t *testing.T) {
	cs := &claudeSession{}
	cs.handleSystem(map[string]any{"cwd": "/workspace/.sessions/x/repo"})
	if got := cs.CurrentCwd(); got != "/workspace/.sessions/x/repo" {
		t.Errorf("CurrentCwd() = %q, want the init cwd", got)
	}

	cs2 := &claudeSession{}
	cs2.handleSystem(map[string]any{"model": "claude-x"}) // no cwd field
	if got := cs2.CurrentCwd(); got != "" {
		t.Errorf("CurrentCwd() = %q, want empty when init omits cwd", got)
	}
}
