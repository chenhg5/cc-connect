package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Regression tests for the global-/list change: /list must mirror the native
// OpenCode session picker — every project, newest first, top-level sessions
// only — instead of only the current work_dir project's sessions.

// seedDBAt creates a sqlite database at path with the given schema and seed
// statements.
func seedDBAt(t *testing.T, path string, schemas []string, seeds []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for test db: %v", err)
	}
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
}

// writeFakeSessionListBin writes an executable fake `opencode` that answers
// `session list --format json` with the given entries. Portable: shell script
// on POSIX, batch file on Windows.
func writeFakeSessionListBin(t *testing.T, entries []opencodeSessionEntry) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal fake sessions: %v", err)
	}
	tmpDir := t.TempDir()
	if runtime.GOOS == "windows" {
		name := filepath.Join(tmpDir, "fake-opencode.bat")
		var body strings.Builder
		body.WriteString("@echo off\r\n")
		body.WriteString("if not \"%~1\"==\"session\" exit /b 0\r\n")
		body.WriteString("if not \"%~2\"==\"list\" exit /b 0\r\n")
		body.WriteString("echo " + string(data) + "\r\n")
		body.WriteString("exit /b 0\r\n")
		if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
			t.Fatal(err)
		}
		return name
	}
	name := filepath.Join(tmpDir, "fake-opencode")
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	body.WriteString("if [ \"$1\" = \"session\" ] && [ \"$2\" = \"list\" ]; then\n")
	body.WriteString("  printf '%s\\n' '" + strings.ReplaceAll(string(data), "'", "'\\''") + "'\n")
	body.WriteString("  exit 0\n")
	body.WriteString("fi\n")
	body.WriteString("exit 0\n")
	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

// writeFakeDeleteSessionBin writes an executable fake `opencode` that
// accepts `session delete <wantID>` and fails otherwise. Portable across
// POSIX and Windows.
func writeFakeDeleteSessionBin(t *testing.T, wantID string) string {
	t.Helper()
	tmpDir := t.TempDir()
	if runtime.GOOS == "windows" {
		name := filepath.Join(tmpDir, "fake-opencode-del.bat")
		var body strings.Builder
		body.WriteString("@echo off\r\n")
		body.WriteString("if not \"%~1\"==\"session\" exit /b 0\r\n")
		body.WriteString("if not \"%~2\"==\"delete\" exit /b 0\r\n")
		body.WriteString("if not \"%~3\"==\"" + wantID + "\" echo unexpected session id: %~3>&2\r\n")
		body.WriteString("if not \"%~3\"==\"" + wantID + "\" exit /b 1\r\n")
		body.WriteString("exit /b 0\r\n")
		if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
			t.Fatal(err)
		}
		return name
	}
	name := filepath.Join(tmpDir, "fake-opencode-del")
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	body.WriteString("if [ \"$1\" = \"session\" ] && [ \"$2\" = \"delete\" ]; then\n")
	body.WriteString("  if [ \"$3\" != \"" + wantID + "\" ]; then\n")
	body.WriteString("    echo \"unexpected session id: $3\" >&2\n")
	body.WriteString("    exit 1\n")
	body.WriteString("  fi\n")
	body.WriteString("fi\n")
	body.WriteString("exit 0\n")
	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

// TestListOpencodeSessionsGlobal_CoversAllProjects pins that sessions from
// different directories are all listed with their directory, newest first,
// while child sessions stay out (matching `session list` top-level semantics).
func TestListOpencodeSessionsGlobal_CoversAllProjects(t *testing.T) {
	path := createTestDB(t,
		[]string{
			testV2MessageSchema,
			testV2SessionSchema,
		},
		[]string{
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated) VALUES ('ses_old', 'old', 'C:/a', 1000, 1000)`,
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated) VALUES ('ses_new', 'new', 'C:/b', 2000, 2000)`,
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated) VALUES ('ses_mid', 'mid', 'C:/a', 1500, 1500)`,
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated, parent_id) VALUES ('ses_child', 'child', 'C:/a', 2500, 2500, 'ses_new')`,
			`INSERT INTO session_message (id, session_id, type) VALUES ('m1', 'ses_new', 'user'), ('m2', 'ses_new', 'assistant')`,
		})
	got, err := listOpencodeSessionsGlobal(path)
	if err != nil {
		t.Fatalf("listOpencodeSessionsGlobal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("sessions = %d, want 3 (child excluded)", len(got))
	}
	if got[0].ID != "ses_new" || got[1].ID != "ses_mid" || got[2].ID != "ses_old" {
		t.Fatalf("order = [%s %s %s], want [ses_new ses_mid ses_old]", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[0].Directory != "C:/b" || got[2].Directory != "C:/a" {
		t.Fatalf("directories = [%q %q], want [C:/b C:/a]", got[0].Directory, got[2].Directory)
	}
	if got[0].MessageCount != 2 {
		t.Fatalf("ses_new count = %d, want 2", got[0].MessageCount)
	}
	if got[0].Summary != "new" {
		t.Fatalf("ses_new title = %q, want new", got[0].Summary)
	}
}

// TestListOpencodeSessionsGlobal_TieBreaksByID pins deterministic ordering
// when several sessions share a timestamp: higher IDs first, so /list and
// /switch resolve an index against the same order.
func TestListOpencodeSessionsGlobal_TieBreaksByID(t *testing.T) {
	path := createTestDB(t,
		[]string{testV2SessionSchema},
		[]string{
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated) VALUES ('ses_aaa', 'a', 'C:/a', 1000, 1000)`,
			`INSERT INTO session_v2 (id, title, directory, time_created, time_updated) VALUES ('ses_zzz', 'z', 'C:/a', 1000, 1000)`,
		})
	got, err := listOpencodeSessionsGlobal(path)
	if err != nil {
		t.Fatalf("listOpencodeSessionsGlobal: %v", err)
	}
	if len(got) != 2 || got[0].ID != "ses_zzz" || got[1].ID != "ses_aaa" {
		t.Fatalf("order = %v, want [ses_zzz ses_aaa]", got)
	}
}

// TestListOpencodeSessionsGlobal_EmptyTable pins the contract an empty v2
// table reports: no rows and no error, so the caller falls back to the CLI.
func TestListOpencodeSessionsGlobal_EmptyTable(t *testing.T) {
	path := createTestDB(t, []string{testV2SessionSchema}, nil)
	got, err := listOpencodeSessionsGlobal(path)
	if err != nil {
		t.Fatalf("listOpencodeSessionsGlobal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("sessions = %d, want 0", len(got))
	}
}

// TestListOpencodeSessionsGlobal_NoV2Table pins the fallback signal: without
// session_v2 the caller must fall back to the server list.
func TestListOpencodeSessionsGlobal_NoV2Table(t *testing.T) {
	path := createTestDB(t, []string{testV1MessageSchema}, nil)
	if _, err := listOpencodeSessionsGlobal(path); err == nil {
		t.Fatal("expected error for db without session_v2, got nil")
	}
}

// TestListOpencodeSessionsGlobal_MissingDB pins that a missing database
// surfaces an error (so the caller falls back) instead of an empty list.
func TestListOpencodeSessionsGlobal_MissingDB(t *testing.T) {
	if _, err := listOpencodeSessionsGlobal(filepath.Join(t.TempDir(), "does-not-exist.db")); err == nil {
		t.Fatal("expected error for missing db, got nil")
	}
}

// TestListOpencodeSessions_FallsBackToCliWhenGlobalEmpty pins the empty-v2
// fallback: an existing-but-empty session_v2 falls back to `session list`,
// and server entries carry their directory for cross-project labelling.
func TestListOpencodeSessions_FallsBackToCliWhenGlobalEmpty(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	seedDBAt(t, filepath.Join(xdg, "opencode", "opencode.db"), []string{testV2SessionSchema}, nil)

	fake := writeFakeSessionListBin(t, []opencodeSessionEntry{
		{ID: "ses_cli", Title: "cli title", Directory: "C:/cli", Updated: 4000, Created: 4000},
	})
	got, err := listOpencodeSessions(fake, t.TempDir())
	if err != nil {
		t.Fatalf("listOpencodeSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ses_cli" {
		t.Fatalf("sessions = %v, want [ses_cli]", got)
	}
	if got[0].Directory != "C:/cli" {
		t.Fatalf("directory = %q, want C:/cli", got[0].Directory)
	}
}

// TestQuerySessionMessageCounts_FallsBackToV1WhenV2Empty pins that a
// session_message table which exists but holds no rows must not zero out
// sessions the v1 message table still knows about.
func TestQuerySessionMessageCounts_FallsBackToV1WhenV2Empty(t *testing.T) {
	path := createTestDB(t,
		[]string{testV1MessageSchema, testV2MessageSchema},
		[]string{
			`INSERT INTO message (id, session_id) VALUES ('m1', 'ses_a'), ('m2', 'ses_a')`,
		})
	got := querySessionMessageCounts(path)
	if got["ses_a"] != 2 {
		t.Fatalf("counts = %v, want ses_a=2 from v1", got)
	}
}

// TestDeleteSession_RefusesForeignDirectory pins the cross-project guard:
// deleting a session that lives in another project directory is refused
// before any CLI call happens (the binary below does not exist, so reaching
// it would fail differently).
func TestDeleteSession_RefusesForeignDirectory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	seedDBAt(t, filepath.Join(xdg, "opencode", "opencode.db"),
		[]string{testV2SessionSchema},
		[]string{
			`INSERT INTO session_v2 (id, title, directory) VALUES ('ses_foreign', 'f', 'C:/other')`,
		})
	a := &Agent{cmd: "definitely-not-a-real-binary", workDir: "C:/proj"}
	err := a.DeleteSession(context.Background(), "ses_foreign")
	if err == nil || !strings.Contains(err.Error(), "refuse to delete") {
		t.Fatalf("DeleteSession = %v, want refusal for foreign directory", err)
	}
}

// TestDeleteSession_RefusesWhenDBUnreadable pins the fail-closed rule: with
// no local database at all there is nothing the guard can verify, so the
// delete is refused instead of reaching the unscoped CLI (the binary below
// does not exist, so reaching it would fail differently).
func TestDeleteSession_RefusesWhenDBUnreadable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := &Agent{cmd: "definitely-not-a-real-binary", workDir: t.TempDir()}
	err := a.DeleteSession(context.Background(), "ses_anything")
	if err == nil || !strings.Contains(err.Error(), "refuse to delete") {
		t.Fatalf("DeleteSession = %v, want refusal without a database", err)
	}
}

// TestDeleteSession_RefusesWhenDBCorrupt pins the fail-closed rule for a
// present-but-unreadable database: garbage bytes where sqlite is expected
// must refuse, not fall through to the unscoped CLI.
func TestDeleteSession_RefusesWhenDBCorrupt(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	dbPath := filepath.Join(xdg, "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("this is definitely not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: "definitely-not-a-real-binary", workDir: t.TempDir()}
	err := a.DeleteSession(context.Background(), "ses_anything")
	if err == nil || !strings.Contains(err.Error(), "refuse to delete") {
		t.Fatalf("DeleteSession = %v, want refusal on corrupt database", err)
	}
}

// TestDeleteSession_AllowsOwnDirectory pins that a session in the agent's
// own directory still deletes through the CLI.
func TestDeleteSession_AllowsOwnDirectory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	proj := t.TempDir()
	projSlashes := filepath.ToSlash(proj)
	seedDBAt(t, filepath.Join(xdg, "opencode", "opencode.db"),
		[]string{testV2SessionSchema},
		[]string{
			`INSERT INTO session_v2 (id, title, directory) VALUES ('ses_own', 'o', '` + projSlashes + `')`,
		})
	bin := writeFakeDeleteSessionBin(t, "ses_own")
	a := &Agent{cmd: bin, workDir: proj}
	if err := a.DeleteSession(context.Background(), "ses_own"); err != nil {
		t.Fatalf("DeleteSession own-directory: %v", err)
	}
}
