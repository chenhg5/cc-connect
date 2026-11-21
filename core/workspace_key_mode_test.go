package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// withWorkspaceKeyMode pins the process-global workspace key form for one test
// and restores the previous one afterwards. The mode is process-global, so a
// leaked "slash" would silently change what every later test resolves.
func withWorkspaceKeyMode(t *testing.T, mode string) {
	t.Helper()
	prev, _ := workspaceKeyMode.Load().(string)
	SetWorkspaceKeyMode(mode)
	t.Cleanup(func() { SetWorkspaceKeyMode(prev) })
}

// The default has to behave exactly like the build it replaces: key separators
// are left alone and — above all — no legacy key is ever derived, so no fallback
// lookup can run at all.
func TestWorkspaceKeyMode_LegacyDerivesNoLegacyKey(t *testing.T) {
	withWorkspaceKeyMode(t, "")

	for _, key := range []string{
		`C:\Users\dev\repo`,
		"C:/Users/dev/repo",
		"//server/share/repo",
		"/data/a/b",
		`/data/weird\name`,
		`C:\Users\dev\repo:feishu:oc_1`,
	} {
		if got := legacyWorkspaceKey(key); got != "" {
			t.Errorf("legacyWorkspaceKey(%q) = %q, want empty in legacy mode", key, got)
		}
	}

	// The key form itself is a pass-through, so native separators survive.
	for _, path := range []string{`C:\Users\dev\repo`, "/data/a/b", `mixed\form/path`} {
		if got := workspaceKeyForm(path); got != path {
			t.Errorf("workspaceKeyForm(%q) = %q, want unchanged in legacy mode", path, got)
		}
	}
}

// Only the exact opt-in value switches forms. Anything unrecognised has to keep
// the historical behaviour, otherwise a typo in config.toml would silently move
// every workspace to a new key.
func TestSetWorkspaceKeyMode_OnlySlashOptsIn(t *testing.T) {
	for _, mode := range []string{"", "slashy", "Rich", "0", "legacy"} {
		withWorkspaceKeyMode(t, mode)
		if got := legacyWorkspaceKey("C:/Users/dev/repo"); got != "" {
			t.Fatalf("mode %q: legacyWorkspaceKey() = %q, want empty — only the opt-in value may switch forms", mode, got)
		}
	}

	// Case and surrounding whitespace are tolerated on the opt-in value, because
	// workspace_key_mode is hand-edited TOML.
	for _, mode := range []string{"slash", " Slash ", "SLASH"} {
		withWorkspaceKeyMode(t, mode)
		if got := legacyWorkspaceKey("C:/Users/dev/repo"); got != `C:\Users\dev\repo` {
			t.Fatalf("mode %q: legacyWorkspaceKey() = %q, want %q", mode, got, `C:\Users\dev\repo`)
		}
	}
}

func TestLegacyWorkspaceKey_SlashModeDerivesWindowsShapes(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	cases := []struct{ key, want string }{
		// Drive-letter path.
		{"C:/Users/dev/repo", `C:\Users\dev\repo`},
		{"c:/Users/dev/repo", `c:\Users\dev\repo`},
		// UNC path.
		{"//server/share/repo", `\\server\share\repo`},
		// Composite "<workspace>:<sessionKey>" as used for directory overrides.
		// Session keys carry no separators, so they must survive untouched.
		{"C:/Users/dev/repo:feishu:oc_1", `C:\Users\dev\repo:feishu:oc_1`},
		// Already native: the swap is a no-op, not a second transformation.
		{`C:\Users\dev\repo`, `C:\Users\dev\repo`},
	}
	for _, tc := range cases {
		if got := legacyWorkspaceKey(tc.key); got != tc.want {
			t.Errorf("legacyWorkspaceKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// The regression the fallback must not reintroduce: where "\" is a legal file-name
// character, an unconditional swap turns one workspace's canonical key into
// another workspace's key. "/data/a/b" would map to the perfectly valid Unix
// workspace "\data\a\b", so it would read that workspace's override and resume
// its session file — and ClearWorkspace*Override would delete its entry.
func TestLegacyWorkspaceKey_SlashModeIgnoresNonWindowsShapes(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	for _, key := range []string{
		"/data/a/b",
		`\data\a\b`,
		`/data/weird\name`,
		"/home/dev/repo:feishu:oc_1",
		// Relative workspaces get no fallback: a relative work_dir resolves
		// against a working directory that changes between releases, so the old
		// key would rarely be reproducible. Documented trade-off.
		"repo/sub",
		// Not a drive letter.
		"1:/data/a/b",
		"",
	} {
		if got := legacyWorkspaceKey(key); got != "" {
			t.Errorf("legacyWorkspaceKey(%q) = %q, want empty — deriving it could name an unrelated workspace", key, got)
		}
	}
}

func TestLooksLikeWindowsWorkspaceKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"C:/Users/dev", true},
		{`C:\Users\dev`, true},
		{"c:/Users/dev", true},
		{"C:/", true},
		{"//server/share", true},
		{`\\server\share`, true},

		{"/data/a/b", false},
		{`\data\a\b`, false},
		{"repo/sub", false},
		{"C:", false},
		{"1:/data", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeWindowsWorkspaceKey(tc.key); got != tc.want {
			t.Errorf("looksLikeWindowsWorkspaceKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// seededWorkspaceSessionFile is the file name pickWorkspaceSessionFile derives for
// a given workspace hash, so tests can seed and compare without duplicating the
// on-disk layout.
func seededWorkspaceSessionFile(storePath, engineName, hash string) string {
	return filepath.Join(filepath.Dir(storePath), fmt.Sprintf("%s_ws_%s.json", engineName, hash))
}

// An empty store path means "no persistence" everywhere else, so the helper must
// keep propagating it verbatim.
func TestPickWorkspaceSessionFile_EmptyStorePath(t *testing.T) {
	if got := pickWorkspaceSessionFile("", "test", "C:/work/repo"); got != "" {
		t.Fatalf("pickWorkspaceSessionFile(\"\") = %q, want empty", got)
	}
}

// Legacy mode: exactly one candidate, the canonical one. Nothing about the
// pre-upgrade behaviour may change, so no fallback file is ever considered.
func TestWorkspaceSessionCandidates_LegacyModeIsCanonicalOnly(t *testing.T) {
	withWorkspaceKeyMode(t, "")

	for _, workspace := range []string{"C:/work/repo", `C:\work\repo`, "/data/repo"} {
		got := workspaceSessionCandidates(workspace)
		want := workspaceHash(workspace)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("workspaceSessionCandidates(%q) = %v, want [%s]", workspace, got, want)
		}
	}
}

// Opt-in mode: the canonical hash comes first, the native-separator hash is the
// single fallback behind it.
func TestWorkspaceSessionCandidates_SlashModeHasLegacyFallback(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	got := workspaceSessionCandidates("C:/work/repo")
	if len(got) != 2 {
		t.Fatalf("workspaceSessionCandidates() = %v, want 2 candidates", got)
	}
	if want := workspaceHash("C:/work/repo"); got[0] != want {
		t.Fatalf("candidate[0] = %q, want canonical %q", got[0], want)
	}
	if want := workspaceHash(`C:\work\repo`); got[1] != want {
		t.Fatalf("candidate[1] = %q, want pre-upgrade %q", got[1], want)
	}

	// Non-Windows shapes have no fallback, so one workspace can never be pointed
	// at another's file.
	for _, workspace := range []string{"/data/repo", `\data\repo`, "repo/sub"} {
		if got := workspaceSessionCandidates(workspace); len(got) != 1 {
			t.Fatalf("workspaceSessionCandidates(%q) = %v, want a single canonical candidate", workspace, got)
		}
	}

	// Already-native Windows key: the derived key equals the canonical one, so the
	// duplicate has to be collapsed instead of yielding two identical entries.
	if got := workspaceSessionCandidates(`C:\work\repo`); len(got) != 1 {
		t.Fatalf("workspaceSessionCandidates(native key) = %v, want a single candidate", got)
	}
}

// Upgrade path: state written by an older Windows build has to stay reachable.
// This is the whole point of the fallback — without it an upgrade looks like a
// brand-new workspace and silently drops the active session.
func TestPickWorkspaceSessionFile_ResumesPreUpgradeFile(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)

	const (
		engine    = "test"
		workspace = "C:/work/repo"
		userKey   = "feishu:oc_1:ou_1"
	)
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	legacyPath := seededWorkspaceSessionFile(storePath, engine, workspaceHash(`C:\work\repo`))
	canonicalPath := seededWorkspaceSessionFile(storePath, engine, workspaceHash(workspace))
	if legacyPath == canonicalPath {
		t.Fatal("test needs distinct legacy and canonical session files")
	}

	// Exactly the shape an older build left behind.
	seed, err := json.Marshal(sessionSnapshot{
		Sessions:      map[string]*Session{"s1": {ID: "s1", Name: "legacy", AgentSessionID: "legacy-agent-sid"}},
		ActiveSession: map[string]string{userKey: "s1"},
		Counter:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, seed, 0o644); err != nil {
		t.Fatal(err)
	}

	got := pickWorkspaceSessionFile(storePath, engine, workspace)
	if got != legacyPath {
		t.Fatalf("pickWorkspaceSessionFile() = %q, want pre-upgrade file %q", got, legacyPath)
	}

	// The sessions inside it must actually load, not merely resolve.
	sm := NewSessionManager(got)
	if id := sm.ActiveSessionID(userKey); id != "s1" {
		t.Fatalf("ActiveSessionID(%q) = %q, want %q — pre-upgrade session content did not load", userKey, id, "s1")
	}

	// Once the canonical file exists it wins, so stale state cannot shadow fresh
	// state.
	if err := os.WriteFile(canonicalPath, []byte(`{"sessions":{},"active_session":{},"counter":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickWorkspaceSessionFile(storePath, engine, workspace); got != canonicalPath {
		t.Fatalf("with both files present got %q, want canonical %q", got, canonicalPath)
	}

	// The fallback is read-only: the pre-upgrade file is not moved or renamed.
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("pre-upgrade file must be left in place, stat error = %v", err)
	}
}

// Downgrade path: with the switch back at its default the fallback is gone, so a
// build without the opt-in resolves to the canonical file only — the historical
// file stays on disk untouched rather than being deleted.
func TestPickWorkspaceSessionFile_DowngradeIgnoresPreUpgradeFile(t *testing.T) {
	withWorkspaceKeyMode(t, "")

	const (
		engine    = "test"
		workspace = "C:/work/repo"
	)
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	legacyPath := seededWorkspaceSessionFile(storePath, engine, workspaceHash(`C:\work\repo`))
	if err := os.WriteFile(legacyPath, []byte(`{"sessions":{},"active_session":{},"counter":0}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := pickWorkspaceSessionFile(storePath, engine, workspace)
	want := seededWorkspaceSessionFile(storePath, engine, workspaceHash(workspace))
	if got != want {
		t.Fatalf("pickWorkspaceSessionFile() = %q, want canonical %q in legacy mode", got, want)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("pre-upgrade file must be left alone, stat error = %v", err)
	}
}

// A fresh workspace resolves to the canonical file, so it never starts out
// sharing another workspace's state.
func TestPickWorkspaceSessionFile_DefaultsToCanonical(t *testing.T) {
	for _, mode := range []string{"", WorkspaceKeyModeSlash} {
		withWorkspaceKeyMode(t, mode)
		storePath := filepath.Join(t.TempDir(), "sessions.json")
		got := pickWorkspaceSessionFile(storePath, "test", "C:/work/fresh")
		want := seededWorkspaceSessionFile(storePath, "test", workspaceHash("C:/work/fresh"))
		if got != want {
			t.Fatalf("mode %q: pickWorkspaceSessionFile() = %q, want canonical %q", mode, got, want)
		}
	}
}

// End to end through the engine: the workspace session manager has to land on the
// pre-upgrade file, which is what actually keeps a running deployment's sessions
// across the upgrade.
func TestGetOrCreateWorkspaceAgent_ResumesPreUpgradeSessionFile(t *testing.T) {
	withWorkspaceKeyMode(t, WorkspaceKeyModeSlash)
	agentName := "test-ws-store-preupgrade"
	registerWorkspaceStoreAgent(t, agentName)

	const workspace = "C:/work/repo"
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	legacyPath := seededWorkspaceSessionFile(storePath, "test", workspaceHash(`C:\work\repo`))
	if err := os.WriteFile(legacyPath, []byte(`{"sessions":{},"active_session":{},"counter":0}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &sendWorkDirAgent{name: agentName}, []Platform{p}, storePath, LangEnglish)

	_, sessions, err := e.getOrCreateWorkspaceAgent(workspace)
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent() error = %v", err)
	}
	if got := sessions.StorePath(); got != legacyPath {
		t.Fatalf("workspace store path = %q, want pre-upgrade %q", got, legacyPath)
	}
}
