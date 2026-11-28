package opencode

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Regression tests for the stale-/list bug: OpenCode v2 records new turns in
// session_message/session_v2 while the v1 message/session tables stop
// receiving writes, so counts/titles must prefer the v2 tables (with v1 as
// fallback for older databases). Reading only v1 surfaced as 0 msgs and
// outdated titles in /list.

// TestLegacyOpencodeDBPath_RespectsXdg pins the fallback location logic.
func TestLegacyOpencodeDBPath_RespectsXdg(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join("C:", "xdg"))
	want := filepath.Join("C:", "xdg", "opencode", "opencode.db")
	if got := legacyOpencodeDBPath(); got != want {
		t.Fatalf("legacy db path = %q, want %q", got, want)
	}
}

// TestQuerySessionMessageCounts_NoDatabase pins that an empty or missing
// database yields no counts.
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
// title.
func TestQuerySessionTitle_MissingDB(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if got := querySessionTitle(missing, "ses_doesnotexist"); got != "" {
		t.Fatalf("title for missing db = %q, want empty", got)
	}
}

const (
	testV1MessageSchema = `CREATE TABLE message (id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`
	testV1SessionSchema = `CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT)`
	testV2MessageSchema = `CREATE TABLE session_message (id TEXT, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT)`
	testV2SessionSchema = `CREATE TABLE session_v2 (id TEXT PRIMARY KEY, title TEXT, directory TEXT, parent_id TEXT, time_created INTEGER, time_updated INTEGER)`
)

// createTestDB builds a temp sqlite database with the given schema and seed
// statements, using the same in-process driver as the production code.
func createTestDB(t *testing.T, schemas []string, seeds []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	}()
	for _, s := range schemas {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema %q: %v", s, err)
		}
	}
	for _, s := range seeds {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return path
}

// TestQuerySessionMessageCounts_PrefersV2Tables pins that counts come from
// session_message when both v1 and v2 tables exist. Pre-v2-fix code read only
// the v1 message table, so it returned stale counts here.
func TestQuerySessionMessageCounts_PrefersV2Tables(t *testing.T) {
	path := createTestDB(t,
		[]string{testV1MessageSchema, testV2MessageSchema},
		[]string{
			`INSERT INTO message (id, session_id) VALUES ('m1', 'ses_a'), ('m2', 'ses_a')`,
			`INSERT INTO session_message (id, session_id, type) VALUES ('sm1', 'ses_a', 'user'), ('sm2', 'ses_a', 'assistant'), ('sm3', 'ses_a', 'assistant'), ('sm4', 'ses_b', 'user')`,
		})
	got := querySessionMessageCounts(path)
	if got["ses_a"] != 3 || got["ses_b"] != 1 {
		t.Fatalf("counts = %v, want ses_a=3 ses_b=1", got)
	}
}

// TestQuerySessionMessageCounts_FallsBackToV1 pins that older databases with
// only the v1 message table still yield counts.
func TestQuerySessionMessageCounts_FallsBackToV1(t *testing.T) {
	path := createTestDB(t,
		[]string{testV1MessageSchema},
		[]string{
			`INSERT INTO message (id, session_id) VALUES ('m1', 'ses_old'), ('m2', 'ses_old')`,
		})
	got := querySessionMessageCounts(path)
	if got["ses_old"] != 2 {
		t.Fatalf("counts = %v, want ses_old=2", got)
	}
}

// TestQuerySessionTitle_PrefersV2 pins that titles come from session_v2 when
// both tables exist. Pre-v2-fix code read only the v1 session table, so it
// returned the outdated title here.
func TestQuerySessionTitle_PrefersV2(t *testing.T) {
	path := createTestDB(t,
		[]string{testV1SessionSchema, testV2SessionSchema},
		[]string{
			`INSERT INTO session (id, title) VALUES ('ses_x', 'v1 stale title')`,
			`INSERT INTO session_v2 (id, title) VALUES ('ses_x', 'v2 live title')`,
		})
	if got := querySessionTitle(path, "ses_x"); got != "v2 live title" {
		t.Fatalf("title = %q, want %q", got, "v2 live title")
	}
}

// TestQuerySessionTitle_FallsBackToV1 pins that older databases with only
// the v1 session table still yield titles.
func TestQuerySessionTitle_FallsBackToV1(t *testing.T) {
	path := createTestDB(t,
		[]string{testV1SessionSchema},
		[]string{
			`INSERT INTO session (id, title) VALUES ('ses_old', 'old title')`,
		})
	if got := querySessionTitle(path, "ses_old"); got != "old title" {
		t.Fatalf("title = %q, want %q", got, "old title")
	}
}
