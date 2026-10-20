package cursor

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCursorInterfacesAndMemory(t *testing.T) {
	work := t.TempDir()
	a := &Agent{workDir: work}
	if !strings.HasSuffix(a.ProjectMemoryFile(), string(filepath.Separator)+"AGENTS.md") {
		t.Fatalf("project memory = %q", a.ProjectMemoryFile())
	}
	if !strings.Contains(a.ProjectMemoryFile(), filepath.Clean(work)) && a.ProjectMemoryFile() != filepath.Join(work, "AGENTS.md") {
		// EvalSymlinks may differ only by /private prefix on macOS.
		if filepath.Base(a.ProjectMemoryFile()) != "AGENTS.md" {
			t.Fatalf("project memory = %q", a.ProjectMemoryFile())
		}
	}
	if a.GlobalMemoryFile() != "" {
		t.Fatalf("global memory = %q, want empty", a.GlobalMemoryFile())
	}
	if a.ValidateSessionID(context.Background(), "") || a.ValidateSessionID(context.Background(), "../x") {
		t.Fatal("expected invalid ids to fail validation")
	}
}

func TestRejectUnsafeSessionID(t *testing.T) {
	bad := []string{"", " ", "..", "../etc", "/tmp/x", "foo/bar", `foo\bar`, "foo/../../etc", ".", "a/../b"}
	for _, id := range bad {
		if err := rejectUnsafeSessionID(id); err == nil {
			t.Fatalf("rejectUnsafeSessionID(%q) = nil", id)
		}
	}
	if err := rejectUnsafeSessionID("7d7ff250-ae71-4ced-beab-503a75c8d58f"); err != nil {
		t.Fatal(err)
	}
}

func withCursorHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	prev := cursorUserHomeDir
	cursorUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { cursorUserHomeDir = prev })
}

func TestGetSessionHistory_OrderedMultilineFiltered(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "11111111-2222-3333-4444-555555555555"
	db := writeFixtureDB(t, home, work, sessionID, false)

	user := "line one\nline two"
	blobs := []fixtureMsg{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "<user_info>\nsecret context\n</user_info>"},
		{Role: "user", Content: "<timestamp>t</timestamp>\n<user_query>\n" + user + "\n</user_query>"},
		{Role: "assistant", Content: []any{
			map[string]any{"type": "reasoning", "text": "hidden"},
			map[string]any{"type": "text", "text": "ok\nstill ok"},
		}},
		{Role: "user", Content: "<system_reminder>\nstay read only\n</system_reminder>"},
	}
	// Interleave a non-child field so a prefix-only scanner would stop early.
	writeRootAndBlobs(t, db, blobs, true)

	a := &Agent{workDir: work}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("len = %d, entries = %#v", len(entries), entries)
	}
	if entries[0].Role != "user" || entries[0].Content != user {
		t.Fatalf("user = %#v", entries[0])
	}
	if entries[1].Role != "assistant" || entries[1].Content != "ok\nstill ok" {
		t.Fatalf("assistant = %#v", entries[1])
	}
	limited, err := a.GetSessionHistory(context.Background(), sessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Role != "assistant" {
		t.Fatalf("limit = %#v", limited)
	}
	if !a.ValidateSessionID(context.Background(), sessionID) {
		t.Fatal("expected valid session")
	}
	if a.ValidateSessionID(context.Background(), "99999999-9999-9999-9999-999999999999") {
		t.Fatal("missing session validated")
	}
}

func TestGetSessionHistory_NoEightyCap(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	db := writeFixtureDB(t, home, work, sessionID, false)
	var blobs []fixtureMsg
	for i := 0; i < 85; i++ {
		blobs = append(blobs, fixtureMsg{Role: "user", Content: "<user_query>\n" + strings.Repeat("m", 1) + string(rune('a'+i%26)) + "\n</user_query>"})
	}
	writeRootAndBlobs(t, db, blobs, false)
	a := &Agent{workDir: work}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 85 {
		t.Fatalf("len = %d, want 85", len(entries))
	}
}

func TestGetSessionTitle_NewAgentFallback(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	db := writeFixtureDB(t, home, work, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{
		{Role: "user", Content: "<user_query>\nnamed from query\n</user_query>"},
	}, false)
	a := &Agent{workDir: work}
	if got := a.GetSessionTitle(sessionID); got != "named from query" {
		t.Fatalf("title = %q", got)
	}
}

func TestSafeSessionDir_SymlinkTraversalAndWorkspace(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	db := writeFixtureDB(t, home, work, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{{Role: "assistant", Content: "hi"}}, false)

	a := &Agent{workDir: work}
	if _, err := a.GetSessionHistory(context.Background(), "../"+sessionID, 0); err == nil {
		t.Fatal("traversal was accepted")
	}
	if _, err := a.GetSessionHistory(context.Background(), filepath.Join(work, sessionID), 0); err == nil {
		t.Fatal("absolute session id was accepted")
	}

	// Session dir symlink pointing outside the workspace hash.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "store.db"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	link := filepath.Join(filepath.Dir(filepath.Dir(db)), linkID)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if a.ValidateSessionID(context.Background(), linkID) {
		t.Fatal("symlink session validated")
	}
	if _, err := a.safeSessionDir(linkID); err == nil {
		t.Fatal("symlink session dir accepted")
	}

	otherWork := t.TempDir()
	other := &Agent{workDir: otherWork}
	if other.ValidateSessionID(context.Background(), sessionID) {
		t.Fatal("cross-workspace session validated")
	}
}

func TestHistory_SymlinkWorkspaceAndLegacyHash(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)

	realWork := t.TempDir()
	linkParent := t.TempDir()
	linkWork := filepath.Join(linkParent, "link")
	if err := os.Symlink(realWork, linkWork); err != nil {
		t.Fatal(err)
	}
	sessionID := "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	// Store under the canonical (EvalSymlinks) hash, which is realWork.
	db := writeFixtureDB(t, home, realWork, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{
		{Role: "user", Content: "<user_query>\nvia symlink\n</user_query>"},
	}, false)
	a := &Agent{workDir: linkWork}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "via symlink" {
		t.Fatalf("entries = %#v", entries)
	}

	// Legacy fallback: hash of the non-resolved absolute path only.
	legacyWork := filepath.Join(linkParent, "legacy-target")
	if err := os.MkdirAll(legacyWork, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyLink := filepath.Join(linkParent, "legacy-link")
	if err := os.Symlink(legacyWork, legacyLink); err != nil {
		t.Fatal(err)
	}
	absLink, err := filepath.Abs(legacyLink)
	if err != nil {
		t.Fatal(err)
	}
	session2 := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	sum := md5.Sum([]byte(absLink))
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(home, ".cursor", "chats", hash, session2)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db2 := filepath.Join(dir, "store.db")
	createEmptyDB(t, db2)
	writeMeta(t, db2, "Legacy")
	writeRootAndBlobs(t, db2, []fixtureMsg{
		{Role: "user", Content: "<user_query>\nlegacy hash\n</user_query>"},
	}, false)
	b := &Agent{workDir: legacyLink}
	entries, err = b.GetSessionHistory(context.Background(), session2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "legacy hash" {
		t.Fatalf("legacy entries = %#v", entries)
	}
}

func TestHistoryUserText_KeepsAngleAndMultiline(t *testing.T) {
	got := historyUserText("<not-a-wrapper>\nkeep me")
	if got != "<not-a-wrapper>\nkeep me" {
		t.Fatalf("literal = %q", got)
	}
	if historyUserText("<user_info>\nsecret\n</user_info>") != "" {
		t.Fatal("user_info was kept")
	}
	q := historyUserText("<system_reminder>\nno\n</system_reminder>\n<user_query>\nline1\nline2\n</user_query>")
	if q != "line1\nline2" {
		t.Fatalf("query = %q", q)
	}
}

func TestCursorRootChildIDs_MalformedVarintNoPanic(t *testing.T) {
	cases := [][]byte{
		{0x0a, 0x80},
		{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01},
	}
	for _, buf := range cases {
		func() {
			defer func() {
				if recover() != nil {
					t.Fatalf("panic on %x", buf)
				}
			}()
			if _, err := cursorRootChildIDs(buf); err == nil {
				t.Fatalf("expected error for %x", buf)
			}
		}()
	}
}

func TestWorkspaceHashSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	realWork := t.TempDir()
	otherWork := t.TempDir()
	sessionID := "abababab-abab-abab-abab-abababababab"
	db := writeFixtureDB(t, home, otherWork, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{
		{Role: "user", Content: "<user_query>\nother workspace\n</user_query>"},
	}, false)

	abs, err := filepath.Abs(realWork)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	sum := md5.Sum([]byte(resolved))
	hash := hex.EncodeToString(sum[:])
	link := filepath.Join(home, ".cursor", "chats", hash)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(filepath.Dir(db)), link); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: realWork}
	if a.ValidateSessionID(context.Background(), sessionID) {
		t.Fatal("hash symlink validated another workspace")
	}
	if _, err := a.GetSessionHistory(context.Background(), sessionID, 0); err == nil {
		t.Fatal("hash symlink returned history")
	}
}

func TestAncestorSymlinkHomeStillReads(t *testing.T) {
	realHome := t.TempDir()
	linkParent := t.TempDir()
	homeLink := filepath.Join(linkParent, "home")
	if err := os.Symlink(realHome, homeLink); err != nil {
		t.Fatal(err)
	}
	withCursorHome(t, homeLink)
	work := t.TempDir()
	sessionID := "cdcdcdcd-cdcd-cdcd-cdcd-cdcdcdcdcdcd"
	db := writeFixtureDB(t, realHome, work, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{
		{Role: "user", Content: "<user_query>\nthrough ancestor symlink\n</user_query>"},
	}, false)
	a := &Agent{workDir: work}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "through ancestor symlink" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestListSessions_ActivityIgnoresEmptyWAL(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	olderID := "11111111-1111-1111-1111-111111111111"
	newerID := "22222222-2222-2222-2222-222222222222"
	olderDB := writeFixtureDB(t, home, work, olderID, false)
	newerDB := writeFixtureDB(t, home, work, newerID, false)
	writeRootAndBlobs(t, olderDB, []fixtureMsg{{Role: "user", Content: "<user_query>\nold\n</user_query>"}}, false)
	writeRootAndBlobs(t, newerDB, []fixtureMsg{{Role: "user", Content: "<user_query>\nnew\n</user_query>"}}, false)

	olderAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	newerAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(olderDB, olderAt, olderAt); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerDB, newerAt, newerAt); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Dir(olderDB), now, now); err != nil {
		t.Fatal(err)
	}

	sessions, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != newerID || sessions[1].ID != olderID {
		t.Fatalf("order = %#v", sessions)
	}
	if !sessions[1].ModifiedAt.Equal(olderAt) || sessions[1].MessageCount != 1 {
		t.Fatalf("older = %#v", sessions[1])
	}
	again, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].ID != newerID || !again[1].ModifiedAt.Equal(olderAt) {
		t.Fatalf("after read order = %#v", again)
	}

	wal := olderDB + "-wal"
	if err := os.WriteFile(wal, []byte("wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	walAt := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.Chtimes(wal, walAt, walAt); err != nil {
		t.Fatal(err)
	}
	bumped, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if bumped[0].ID != olderID || !bumped[0].ModifiedAt.Equal(walAt) || bumped[0].MessageCount != 1 {
		t.Fatalf("nonempty wal = %#v", bumped)
	}

	if err := os.Remove(wal); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newerDB, wal); err != nil {
		t.Fatal(err)
	}
	linked, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if linked[0].ID != newerID || !linked[1].ModifiedAt.Equal(olderAt) {
		t.Fatalf("symlink wal sorted = %#v", linked)
	}
	if err := os.Remove(wal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(wal, walAt, walAt); err != nil {
		t.Fatal(err)
	}
	ignored, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if ignored[0].ID != newerID || !ignored[1].ModifiedAt.Equal(olderAt) {
		t.Fatalf("empty wal still sorted = %#v", ignored)
	}
}

func TestSQLiteFileURI_DriveLetterAndEscape(t *testing.T) {
	got, err := sqliteFileURI("C:/a b/test.db", "ro")
	if err != nil {
		t.Fatal(err)
	}
	if got != "file:///C:/a%20b/test.db?mode=ro" {
		t.Fatalf("drive uri = %q", got)
	}
	if strings.Contains(got, "file://C:") {
		t.Fatalf("drive letter parsed as authority: %s", got)
	}
	unc, err := sqliteFileURI("//server/share/a b.db", "rw")
	if err != nil {
		t.Fatal(err)
	}
	if unc != "file://server/share/a%20b.db?mode=rw" {
		t.Fatalf("unc uri = %q", unc)
	}
}

func TestGetSessionHistory_WALStore(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "b8eb0211-128e-452f-ba2e-ace28253d454"
	db := writeFixtureDB(t, home, work, sessionID, false)
	writeRootAndBlobs(t, db, []fixtureMsg{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "<user_info>\nsecret\n</user_info>"},
		{Role: "user", Content: "<timestamp>t</timestamp>\n<user_query>\nvisible line\n</user_query>"},
		{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "visible reply"}}},
	}, false)
	sqlDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	// Parent repro: a closed WAL database has no -wal/-shm. mode=ro and
	// sqlite3 -readonly both fail until a later connection creates them.
	_ = os.Remove(db + "-wal")
	_ = os.Remove(db + "-shm")
	a := &Agent{workDir: work}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Content != "visible line" || entries[1].Content != "visible reply" {
		t.Fatalf("entries = %#v", entries)
	}
	sessions, err := listCursorSessions(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].MessageCount != 2 {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestRunSQLite_QueryOnlyWALAndPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "store.db")
	sqlDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE t(x); INSERT INTO t VALUES ('main')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO t VALUES ('wal-only')`); err != nil {
		t.Fatal(err)
	}
	walInfo, err := os.Stat(db + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("wal sidecar = %v size ok=%v", err, walInfo != nil && walInfo.Size() > 0)
	}
	out, err := runSQLite(context.Background(), db, "SELECT x FROM t ORDER BY x;")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, "wal-only") || !strings.Contains(got, "main") {
		t.Fatalf("visible rows = %q", got)
	}
	if _, err := runSQLite(context.Background(), db, "INSERT INTO t VALUES ('nope');"); err == nil {
		t.Fatal("query_only insert succeeded")
	}
	check, err := runSQLite(context.Background(), db, "SELECT count(*) FROM t;")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(check)) != "2" {
		t.Fatalf("count after rejected write = %q", strings.TrimSpace(string(check)))
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.db")
	if _, err := runSQLite(context.Background(), missing, "SELECT 1;"); err == nil {
		t.Fatal("missing db opened")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing db created: %v", err)
	}
}

func TestRunSQLite_ReadonlyAndBounded(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := runSQLite(context.Background(), missing, "SELECT 1;"); err == nil {
		t.Fatal("expected readonly open of missing db to fail")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing db stat = %v", err)
	}

	dir := t.TempDir()
	db := filepath.Join(dir, "store.db")
	createEmptyDB(t, db)
	prev := cursorMaxHexResult
	cursorMaxHexResult = 4
	t.Cleanup(func() { cursorMaxHexResult = prev })
	_, err := runSQLite(context.Background(), db, "SELECT hex(randomblob(64));")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded err = %v", err)
	}
}

func TestRunSQLite_KillsOversizedWriterBeforeWait(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-sqlite3")
	body := "#!/bin/sh\n" +
		"python3 -c 'import sys,time\n" +
		"sys.stdout.buffer.write(b\"x\"*65536)\n" +
		"sys.stdout.flush()\n" +
		"while True:\n" +
		"    sys.stdout.buffer.write(b\"y\"*65536)\n" +
		"    sys.stdout.flush()\n" +
		"    time.sleep(0.05)\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	prevBin, prevMax := cursorSQLiteBin, cursorMaxHexResult
	cursorSQLiteBin = script
	cursorMaxHexResult = 1024
	t.Cleanup(func() {
		cursorSQLiteBin = prevBin
		cursorMaxHexResult = prevMax
	})
	start := time.Now()
	_, err := runSQLite(context.Background(), "ignored.db", "SELECT 1;")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("oversized sqlite took %s", elapsed)
	}
}

func TestListAndDelete_CanonicalCountAndScope(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "work-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	sessionID := "90909090-9090-9090-9090-909090909090"
	db := writeFixtureDB(t, home, real, sessionID, false)
	var msgs []fixtureMsg
	for i := 0; i < 85; i++ {
		msgs = append(msgs, fixtureMsg{Role: "user", Content: "<user_query>\n" + strings.Repeat("q", 3) + "\n</user_query>"})
	}
	writeRootAndBlobs(t, db, msgs, false)

	sessions, err := listCursorSessions(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != sessionID || sessions[0].MessageCount != 85 {
		t.Fatalf("sessions = %#v", sessions)
	}
	a := &Agent{workDir: link}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != sessions[0].MessageCount {
		t.Fatalf("history %d != list %d", len(entries), sessions[0].MessageCount)
	}

	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteSession(context.Background(), "../"+sessionID); err == nil {
		t.Fatal("traversal delete accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("real store removed during traversal")
	}

	target := t.TempDir()
	secret := filepath.Join(target, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkID := "80808080-8080-8080-8080-808080808080"
	hashDir := filepath.Dir(filepath.Dir(db))
	if err := os.Symlink(target, filepath.Join(hashDir, linkID)); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteSession(context.Background(), linkID); err == nil {
		t.Fatal("symlink session delete accepted")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("symlink target was deleted")
	}

	if err := a.DeleteSession(context.Background(), "70707070-7070-7070-7070-707070707070"); err == nil {
		t.Fatal("missing session delete succeeded")
	}
	if sessions, err = listCursorSessions(t.TempDir()); err != nil || len(sessions) != 0 {
		t.Fatalf("missing workspace list = %#v, %v", sessions, err)
	}

	if err := a.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("deleted store stat = %v", err)
	}
}

func TestGetSessionHistory_ClearSQLiteError(t *testing.T) {
	home := t.TempDir()
	withCursorHome(t, home)
	work := t.TempDir()
	sessionID := "12121212-3434-5656-7878-909090909090"
	db := writeFixtureDB(t, home, work, sessionID, false)
	if err := os.WriteFile(db, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: work}
	_, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err == nil || !strings.Contains(err.Error(), "cursor:") {
		t.Fatalf("err = %v", err)
	}
}

type fixtureMsg struct {
	Role    string
	Content any
}

func writeFixtureDB(t *testing.T, home, workDir, sessionID string, useConfig bool) string {
	t.Helper()
	abs, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	sum := md5.Sum([]byte(resolved))
	hash := hex.EncodeToString(sum[:])
	base := filepath.Join(home, ".cursor", "chats")
	if useConfig {
		base = filepath.Join(home, ".config", "Cursor", "chats")
	}
	dir := filepath.Join(base, hash, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "store.db")
	createEmptyDB(t, db)
	writeMeta(t, db, "New Agent")
	return db
}

func createEmptyDB(t *testing.T, db string) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close fixture database: %v", err)
		}
	}()
	if _, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, data BLOB);`); err != nil {
		t.Fatal(err)
	}
}

func writeMeta(t *testing.T, db, name string) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close fixture database: %v", err)
		}
	}()
	payload, _ := json.Marshal(map[string]string{
		"name":             name,
		"latestRootBlobId": strings.Repeat("ab", 32),
	})
	hexVal := hex.EncodeToString(payload)
	if _, err := sqlDB.Exec(`INSERT INTO meta(key, value) VALUES('0', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, hexVal); err != nil {
		t.Fatal(err)
	}
}

func writeRootAndBlobs(t *testing.T, db string, msgs []fixtureMsg, interleave bool) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close fixture database: %v", err)
		}
	}()
	var root []byte
	for i, msg := range msgs {
		body, err := json.Marshal(map[string]any{"role": msg.Role, "content": msg.Content})
		if err != nil {
			t.Fatal(err)
		}
		sum := md5.Sum(body)
		id := hex.EncodeToString(sum[:]) + hex.EncodeToString(sum[:])
		if _, err := sqlDB.Exec(`INSERT OR REPLACE INTO blobs(id, data) VALUES(?, ?)`, id, body); err != nil {
			t.Fatal(err)
		}
		raw, err := hex.DecodeString(id)
		if err != nil {
			t.Fatal(err)
		}
		root = appendProtoBytes(root, 1, raw)
		if interleave && i == 0 {
			root = appendProtoBytes(root, 9, []byte("file:///tmp/cursor-parity"))
		}
	}
	rootID := strings.Repeat("ab", 32)
	if _, err := sqlDB.Exec(`INSERT OR REPLACE INTO blobs(id, data) VALUES(?, ?)`, rootID, root); err != nil {
		t.Fatal(err)
	}
}

func appendProtoBytes(buf []byte, field int, payload []byte) []byte {
	buf = append(buf, byte((field<<3)|2))
	buf = appendVarint(buf, len(payload))
	return append(buf, payload...)
}

func appendVarint(buf []byte, n int) []byte {
	for n >= 0x80 {
		buf = append(buf, byte(n)|0x80)
		n >>= 7
	}
	return append(buf, byte(n))
}
