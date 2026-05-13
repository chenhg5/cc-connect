package ymsagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJSONL writes the given pre-built JSONL lines to a file.
func writeJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestFindSessionFile_MatchByUUID(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"20260101_aaa.jsonl", "20260102_bbb.jsonl", "not-a-session.txt"} {
		writeJSONL(t, filepath.Join(dir, name), `{"type":"session","id":"x"}`)
	}
	if got := findSessionFile(dir, "bbb"); got != filepath.Join(dir, "20260102_bbb.jsonl") {
		t.Errorf("findSessionFile bbb = %q", got)
	}
	if got := findSessionFile(dir, "missing"); got != "" {
		t.Errorf("missing should return empty, got %q", got)
	}
}

func TestFindSessionFile_EmptyArgs(t *testing.T) {
	if findSessionFile("", "x") != "" {
		t.Error("empty dir should return empty")
	}
	if findSessionFile("/tmp", "") != "" {
		t.Error("empty id should return empty")
	}
}

func TestScanSession_ExtractsIDSummaryCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101_s1.jsonl")
	writeJSONL(t, path,
		`{"type":"session","id":"s1"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"hello there"}]}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"text":"hi"}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolName":"bash"}}`,
	)
	sid, summary, count := scanSession(path)
	if sid != "s1" {
		t.Errorf("sid = %q", sid)
	}
	if summary != "hello there" {
		t.Errorf("summary = %q", summary)
	}
	if count != 2 {
		t.Errorf("msg count = %d (user + assistant only)", count)
	}
}

func TestScanSession_SummaryTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101_s2.jsonl")
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	writeJSONL(t, path,
		`{"type":"session","id":"s2"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"`+long+`"}]}}`,
	)
	_, summary, _ := scanSession(path)
	if !endsWith(summary, "...") {
		t.Errorf("summary should end with ...; got %q", summary)
	}
	if len([]rune(summary)) > 83 {
		t.Errorf("summary too long: %d runes", len([]rune(summary)))
	}
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func TestListSessions_SortedByMtimeDesc(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "20260101_old.jsonl")
	newer := filepath.Join(dir, "20260102_new.jsonl")
	writeJSONL(t, older, `{"type":"session","id":"old"}`)
	writeJSONL(t, newer, `{"type":"session","id":"new"}`)
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	a := &Agent{sessionDir: dir}
	got, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(got), got)
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Errorf("sort order wrong: %+v", got)
	}
}

func TestListSessions_MissingDir(t *testing.T) {
	a := &Agent{sessionDir: filepath.Join(t.TempDir(), "does-not-exist")}
	got, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on missing dir, got %+v", got)
	}
}

func TestListSessions_SkipsNonJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(t, filepath.Join(dir, "20260101_real.jsonl"),
		`{"type":"session","id":"real"}`)
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{sessionDir: dir}
	got, _ := a.ListSessions(context.Background())
	if len(got) != 1 || got[0].ID != "real" {
		t.Errorf("expected only the jsonl session, got %+v", got)
	}
}

func TestDeleteSession_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101_xxx.jsonl")
	writeJSONL(t, path, `{"type":"session","id":"xxx"}`)

	a := &Agent{sessionDir: dir}
	if err := a.DeleteSession(context.Background(), "xxx"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	a := &Agent{sessionDir: t.TempDir()}
	err := a.DeleteSession(context.Background(), "no-such-id")
	if err == nil {
		t.Fatal("expected error for missing session id")
	}
}

func TestGetSessionHistory_FiltersCustomType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101_hist.jsonl")
	writeJSONL(t, path,
		`{"type":"session","id":"hist"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"q1"}]}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"text":"a1"}]}}`,
		`{"type":"message","message":{"role":"assistant","customType":"yms-command","content":[{"text":"cmd-output"}]}}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"q2"}]}}`,
	)
	a := &Agent{sessionDir: dir}
	hist, err := a.GetSessionHistory(context.Background(), "hist", 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("expected 3 entries (customType filtered), got %d: %+v", len(hist), hist)
	}
	for _, h := range hist {
		if h.Content == "cmd-output" {
			t.Error("customType entry leaked through")
		}
	}
}

func TestGetSessionHistory_LimitTrimsFromHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101_lim.jsonl")
	writeJSONL(t, path,
		`{"type":"session","id":"lim"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"q1"}]}}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"q2"}]}}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"q3"}]}}`,
	)
	a := &Agent{sessionDir: dir}
	hist, _ := a.GetSessionHistory(context.Background(), "lim", 2)
	if len(hist) != 2 || hist[0].Content != "q2" || hist[1].Content != "q3" {
		t.Errorf("limit failed: %+v", hist)
	}
}

func TestGetSessionHistory_MissingSession(t *testing.T) {
	a := &Agent{sessionDir: t.TempDir()}
	got, err := a.GetSessionHistory(context.Background(), "no-such", 0)
	if err != nil || got != nil {
		t.Errorf("got (%+v, %v), want (nil, nil)", got, err)
	}
}
