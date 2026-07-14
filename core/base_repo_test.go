package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitRepoWithMarker creates a real git repo at dir with a single commit on
// branch main containing a file named marker. Returns nothing; fails the test
// on any git error. Used to distinguish which base repo a worktree came from.
func gitInitRepoWithMarker(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "seed")
}

func TestResolveBaseRepoFromLetter(t *testing.T) {
	root := t.TempDir()

	repo := filepath.Join(root, "repoB")
	gitInitRepoWithMarker(t, repo, "REPO_B_MARKER")

	writeLetter := func(name, frontMatter string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(frontMatter), 0o644); err != nil {
			t.Fatalf("write letter: %v", err)
		}
		return p
	}

	// Present + valid git repo → returned verbatim.
	withRepo := writeLetter("with.query.md", "---\nID: L-0500\nBase-Repo: "+repo+"\n---\nbody\n")
	if got := resolveBaseRepoFromLetter(withRepo); got != repo {
		t.Fatalf("resolveBaseRepoFromLetter(with) = %q, want %q", got, repo)
	}

	// Header absent → "".
	noHeader := writeLetter("none.query.md", "---\nID: L-0501\n---\nbody\n")
	if got := resolveBaseRepoFromLetter(noHeader); got != "" {
		t.Fatalf("resolveBaseRepoFromLetter(no header) = %q, want empty", got)
	}

	// Header present but not a git repo → "" (defensive; falls back to work_dir).
	notGit := writeLetter("notgit.query.md", "---\nID: L-0502\nBase-Repo: "+filepath.Join(root, "nope")+"\n---\nbody\n")
	if got := resolveBaseRepoFromLetter(notGit); got != "" {
		t.Fatalf("resolveBaseRepoFromLetter(not git) = %q, want empty", got)
	}
}

func TestDispatchStoreBaseRepoForLetter(t *testing.T) {
	s := newDispatchStore(t.TempDir())
	if err := s.upsert(DispatchExpectation{Letter: "L-0600", To: "dev-pro", BaseRepo: `F:\GitHub\cc-connect`, State: dispatchStateDispatched}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.upsert(DispatchExpectation{Letter: "L-0601", To: "dev-pro", State: dispatchStateDispatched}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := s.baseRepoForLetter("L-0600"); got != `F:\GitHub\cc-connect` {
		t.Fatalf("baseRepoForLetter(L-0600) = %q", got)
	}
	if got := s.baseRepoForLetter("L-0601"); got != "" {
		t.Fatalf("baseRepoForLetter(L-0601 without BaseRepo) = %q, want empty", got)
	}
	if got := s.baseRepoForLetter("L-9999"); got != "" {
		t.Fatalf("baseRepoForLetter(missing) = %q, want empty", got)
	}
}

// TestWorktreeUsesPerLetterBaseRepoOverStaticWorkDir is the end-to-end proof for
// L-0422: a seat whose static work_dir is repoA still creates its dynamic
// worktree from repoB when the dispatched letter's ledger entry names repoB.
func TestWorktreeUsesPerLetterBaseRepoOverStaticWorkDir(t *testing.T) {
	root := t.TempDir()

	repoA := filepath.Join(root, "repoA") // seat's static work_dir
	repoB := filepath.Join(root, "repoB") // per-letter base repo (e.g. cc-connect)
	gitInitRepoWithMarker(t, repoA, "REPO_A_MARKER")
	gitInitRepoWithMarker(t, repoB, "REPO_B_MARKER")

	agent := &dummyAgentWithWorkDir{workDir: repoA, name: "l0422-agent"}
	RegisterAgent("l0422-agent", func(opts map[string]any) (Agent, error) {
		return &dummyAgentWithWorkDir{workDir: repoA, name: "l0422-agent"}, nil
	})

	e := NewEngine("dev-pro", agent, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:   "L-0700",
		To:       "dev-pro",
		BaseRepo: repoB,
		State:    dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	workspace := filepath.Join(root, "worktrees", "letter-L-0700")
	if _, _, err := e.getOrCreateWorkspaceAgent(workspace); err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent: %v", err)
	}

	// The worktree must carry repoB's marker, never repoA's.
	if _, err := os.Stat(filepath.Join(workspace, "REPO_B_MARKER")); err != nil {
		t.Fatalf("worktree missing REPO_B_MARKER (not created from per-letter base repo): %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "REPO_A_MARKER")); err == nil {
		t.Fatalf("worktree contains REPO_A_MARKER: fell back to static work_dir instead of per-letter base repo")
	}

	// And the worktree must be a worktree of repoB (its commondir points back
	// into repoB's .git), never repoA.
	out, err := exec.Command("git", "-C", workspace, "rev-parse", "--git-common-dir").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse in worktree: %v (%s)", err, out)
	}
	commonDir := filepath.ToSlash(strings.TrimSpace(string(out)))
	if !strings.Contains(strings.ToLower(commonDir), "repob") {
		t.Fatalf("worktree common dir %q is not under repoB", commonDir)
	}
	if strings.Contains(strings.ToLower(commonDir), "repoa") {
		t.Fatalf("worktree common dir %q is under repoA (wrong base repo)", commonDir)
	}
}
