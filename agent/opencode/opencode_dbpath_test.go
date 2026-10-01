package opencode

import (
	"errors"
	"path/filepath"
	"testing"
)

// Regression test for the ghost-session bug: the driven OpenCode CLI can
// resolve to a different database file than the default opencode.db
// (depending on its environment), so session counts/titles must be read from
// the CLI's real database (discovered via `opencode db path`), not from a
// hardcoded path. Reading the wrong file surfaced as ghost sessions with
// 0 msgs in /list.

// TestResolveOpencodeDBPath_UsesCliDiscovery pins that a successful
// `db path` answer (even with trailing whitespace) wins over the default.
func TestResolveOpencodeDBPath_UsesCliDiscovery(t *testing.T) {
	got := resolveOpencodeDBPathWithRunner("opencode", t.TempDir(),
		func(cmd, workDir string) ([]byte, error) {
			if cmd != "opencode" {
				t.Fatalf("runner cmd = %q, want %q", cmd, "opencode")
			}
			return []byte("C:\\data\\opencode-tmp-win-deploy.db\n"), nil
		})
	want := `C:\data\opencode-tmp-win-deploy.db`
	if got != want {
		t.Fatalf("resolved db path = %q, want %q", got, want)
	}
}

// TestResolveOpencodeDBPath_FallsBackOnRunnerError pins the fallback to the
// historical default when discovery fails (e.g. older CLI without `db path`).
func TestResolveOpencodeDBPath_FallsBackOnRunnerError(t *testing.T) {
	got := resolveOpencodeDBPathWithRunner("opencode", t.TempDir(),
		func(cmd, workDir string) ([]byte, error) {
			return nil, errors.New("exit status 1")
		})
	if got != legacyOpencodeDBPath() {
		t.Fatalf("resolved db path = %q, want legacy %q", got, legacyOpencodeDBPath())
	}
}

// TestResolveOpencodeDBPath_FallsBackOnBlankOutput pins the fallback when
// discovery succeeds but prints nothing usable.
func TestResolveOpencodeDBPath_FallsBackOnBlankOutput(t *testing.T) {
	got := resolveOpencodeDBPathWithRunner("opencode", t.TempDir(),
		func(cmd, workDir string) ([]byte, error) {
			return []byte("  \n"), nil
		})
	if got != legacyOpencodeDBPath() {
		t.Fatalf("resolved db path = %q, want legacy %q", got, legacyOpencodeDBPath())
	}
}

// TestLegacyOpencodeDBPath_RespectsXdg pins the fallback location logic.
func TestLegacyOpencodeDBPath_RespectsXdg(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join("C:", "xdg"))
	want := filepath.Join("C:", "xdg", "opencode", "opencode.db")
	if got := legacyOpencodeDBPath(); got != want {
		t.Fatalf("legacy db path = %q, want %q", got, want)
	}
}

// TestQuerySessionMessageCounts_NoDatabase pins that an empty or missing
// database yields no counts without requiring the sqlite3 CLI.
func TestQuerySessionMessageCounts_NoDatabase(t *testing.T) {
	if got := querySessionMessageCounts(""); len(got) != 0 {
		t.Fatalf("counts for empty path = %v, want empty", got)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if got := querySessionMessageCounts(missing); len(got) != 0 {
		t.Fatalf("counts for missing db = %v, want empty", got)
	}
}

// TestQuerySessionTitle_MissingDB pins that a missing database yields no
// title. The session ID is deliberately bogus so the result is "" in every
// environment (no row can match), without needing any real database.
func TestQuerySessionTitle_MissingDB(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if got := querySessionTitle(missing, "ses_doesnotexist"); got != "" {
		t.Fatalf("title for missing db = %q, want empty", got)
	}
}
