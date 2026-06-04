package codex

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestCodexSession(t *testing.T, codexHome, day, id, cwd, prompt string, modified time.Time) {
	t.Helper()
	writeTestCodexSessionInRoot(t, codexHome, "sessions", day, id, cwd, prompt, modified)
}

func writeTestCodexSessionInRoot(t *testing.T, codexHome, root, day, id, cwd, prompt string, modified time.Time) {
	t.Helper()
	dir := filepath.Join(codexHome, root, "2026", "05", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	path := filepath.Join(dir, "rollout-2026-05-"+day+"T12-00-00-"+id+".jsonl")
	body := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"` + prompt + `"}]}}`,
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("chtimes session: %v", err)
	}
}

func writeTestCodexGlobalState(t *testing.T, codexHome string, projectOrder, savedRoots []string) {
	t.Helper()
	writeTestCodexGlobalStateWithProjectless(t, codexHome, projectOrder, savedRoots, nil)
}

func writeTestCodexGlobalStateWithProjectless(t *testing.T, codexHome string, projectOrder, savedRoots, projectlessIDs []string) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	body := struct {
		ProjectOrder            []string `json:"project-order"`
		ElectronSavedWorkspaces []string `json:"electron-saved-workspace-roots"`
		ProjectlessThreadIDs    []string `json:"projectless-thread-ids"`
	}{
		ProjectOrder:            projectOrder,
		ElectronSavedWorkspaces: savedRoots,
		ProjectlessThreadIDs:    projectlessIDs,
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal global state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"), data, 0o644); err != nil {
		t.Fatalf("write global state: %v", err)
	}
}

func writeTestCodexStateDB(t *testing.T, codexHome string, rows ...struct {
	id          string
	cwd         string
	title       string
	updatedAtMS int64
	archived    bool
}) {
	t.Helper()
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(codexHome, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`create table threads (
		id text primary key,
		cwd text,
		title text,
		updated_at integer,
		updated_at_ms integer,
		archived integer not null default 0
	)`)
	if err != nil {
		t.Fatalf("create threads: %v", err)
	}
	for _, row := range rows {
		archived := 0
		if row.archived {
			archived = 1
		}
		_, err = db.Exec(`insert into threads (id, cwd, title, updated_at, updated_at_ms, archived) values (?, ?, ?, ?, ?, ?)`,
			row.id, row.cwd, row.title, row.updatedAtMS/1000, row.updatedAtMS, archived)
		if err != nil {
			t.Fatalf("insert thread: %v", err)
		}
	}
}

func TestListCodexWorkdirsGroupsByCwd(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	projectA := filepath.Join(t.TempDir(), "project-a")
	projectB := filepath.Join(t.TempDir(), "project-b")
	newer := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)

	writeTestCodexSession(t, codexHome, "24", "a1", projectA, "first task", older)
	writeTestCodexSession(t, codexHome, "24", "a2", projectA, "latest task", newer)
	writeTestCodexSession(t, codexHome, "23", "b1", projectB, "other task", older.Add(-time.Hour))

	got, err := listCodexWorkdirs(codexHome)
	if err != nil {
		t.Fatalf("listCodexWorkdirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Cwd != projectA {
		t.Fatalf("first cwd = %q, want %q", got[0].Cwd, projectA)
	}
	if got[0].SessionCount != 2 {
		t.Fatalf("projectA sessions = %d, want 2", got[0].SessionCount)
	}
	if got[0].MessageCount != 4 {
		t.Fatalf("projectA messages = %d, want 4", got[0].MessageCount)
	}
	if got[0].LatestSummary != "latest task" {
		t.Fatalf("projectA latest summary = %q, want latest task", got[0].LatestSummary)
	}
	if got[1].Cwd != projectB {
		t.Fatalf("second cwd = %q, want %q", got[1].Cwd, projectB)
	}
}

func TestListCodexWorkdirsGroupsSavedProjectsByRoot(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	rootA := filepath.Join(t.TempDir(), "project-a")
	nestedA := filepath.Join(rootA, "service")
	rootB := filepath.Join(t.TempDir(), "project-b")
	loose := filepath.Join(t.TempDir(), "loose")
	newer := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)

	writeTestCodexGlobalState(t, codexHome, []string{rootB, rootA}, []string{rootA, rootB})
	writeTestCodexSession(t, codexHome, "24", "a1", rootA, "root task", older)
	writeTestCodexSession(t, codexHome, "24", "a2", nestedA, "nested task", newer)
	writeTestCodexSession(t, codexHome, "24", "b1", rootB, "project b", older)
	writeTestCodexSession(t, codexHome, "24", "c1", loose, "loose task", newer.Add(-2*time.Hour))

	got, err := listCodexWorkdirs(codexHome)
	if err != nil {
		t.Fatalf("listCodexWorkdirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Cwd != rootB {
		t.Fatalf("first cwd = %q, want project-order rootB %q", got[0].Cwd, rootB)
	}
	if got[1].Cwd != rootA {
		t.Fatalf("second cwd = %q, want project-order rootA %q", got[1].Cwd, rootA)
	}
	if got[1].SessionCount != 2 {
		t.Fatalf("rootA sessions = %d, want 2", got[1].SessionCount)
	}
	if got[1].MessageCount != 4 {
		t.Fatalf("rootA messages = %d, want 4", got[1].MessageCount)
	}
	if got[1].LatestSummary != "nested task" {
		t.Fatalf("rootA latest summary = %q, want nested task", got[1].LatestSummary)
	}
}

func TestListCodexSessionsIncludesNestedSessionsForSavedProjectRoot(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	rootA := filepath.Join(t.TempDir(), "project-a")
	nestedA := filepath.Join(rootA, "service")
	loose := filepath.Join(t.TempDir(), "loose")
	modified := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	writeTestCodexGlobalState(t, codexHome, []string{rootA}, []string{rootA})
	writeTestCodexSession(t, codexHome, "24", "a1", rootA, "root task", modified)
	writeTestCodexSession(t, codexHome, "24", "a2", nestedA, "nested task", modified.Add(time.Minute))
	writeTestCodexSession(t, codexHome, "24", "b1", loose, "loose task", modified.Add(2*time.Minute))

	got, err := listCodexSessions(rootA, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "a2" || got[1].ID != "a1" {
		t.Fatalf("session ids = [%s %s], want [a2 a1]", got[0].ID, got[1].ID)
	}
}

func TestListCodexWorkdirsSkipsProjectlessThreadsFromGlobalState(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	rootA := filepath.Join(t.TempDir(), "project-a")
	projectless := filepath.Join(t.TempDir(), "projectless")
	hidden := filepath.Join(t.TempDir(), "hidden")
	modified := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	writeTestCodexGlobalStateWithProjectless(t, codexHome, []string{rootA}, []string{rootA}, []string{"p1"})
	writeTestCodexSession(t, codexHome, "24", "a1", rootA, "saved task", modified)
	writeTestCodexSession(t, codexHome, "24", "p1", projectless, "projectless task", modified.Add(time.Minute))
	writeTestCodexSession(t, codexHome, "24", "h1", hidden, "hidden task", modified.Add(2*time.Minute))

	got, err := listCodexWorkdirs(codexHome)
	if err != nil {
		t.Fatalf("listCodexWorkdirs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Cwd != rootA {
		t.Fatalf("first cwd = %q, want saved root %q", got[0].Cwd, rootA)
	}
	for _, wd := range got {
		if wd.Cwd == projectless || wd.Cwd == hidden {
			t.Fatalf("unexpected non-project workdir: %#v", wd)
		}
	}
}

func TestListCodexWorkdirsSkipsArchivedSessionFiles(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	projectA := filepath.Join(t.TempDir(), "project-a")
	modified := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	writeTestCodexSessionInRoot(t, codexHome, "archived_sessions", "24", "a1", projectA, "archived task", modified)

	got, err := listCodexWorkdirs(codexHome)
	if err != nil {
		t.Fatalf("listCodexWorkdirs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0: %#v", len(got), got)
	}
}

func TestListCodexSessionsUsesAppIndexTitleAndDatabaseCwd(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	projectA := filepath.Join(t.TempDir(), "project-a")
	updated := time.Date(2026, 5, 24, 13, 0, 0, 0, time.UTC)

	writeTestCodexSession(t, codexHome, "24", "a1", projectA, "jsonl prompt", updated.Add(-time.Hour))
	writeTestCodexStateDB(t, codexHome, struct {
		id          string
		cwd         string
		title       string
		updatedAtMS int64
		archived    bool
	}{id: "a1", cwd: projectA, title: "database title", updatedAtMS: updated.UnixMilli()})
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(`{"id":"a1","thread_name":"app sidebar title","updated_at":"2026-05-24T13:30:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	got, err := listCodexSessions(projectA, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Summary != "app sidebar title" {
		t.Fatalf("summary = %q, want app sidebar title", got[0].Summary)
	}
	if got[0].MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", got[0].MessageCount)
	}
}

func TestListCodexWorkdirsSkipsArchivedDatabaseThreads(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	projectA := filepath.Join(t.TempDir(), "project-a")
	updated := time.Date(2026, 5, 24, 13, 0, 0, 0, time.UTC)

	writeTestCodexSession(t, codexHome, "24", "a1", projectA, "archived jsonl task", updated)
	writeTestCodexStateDB(t, codexHome, struct {
		id          string
		cwd         string
		title       string
		updatedAtMS int64
		archived    bool
	}{id: "a1", cwd: projectA, title: "archived database title", updatedAtMS: updated.UnixMilli(), archived: true})

	got, err := listCodexWorkdirs(codexHome)
	if err != nil {
		t.Fatalf("listCodexWorkdirs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0: %#v", len(got), got)
	}
}

func TestListCodexWorkdirsSkipsArchivedIndexOnlyThread(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), ".codex")
	projectA := filepath.Join(t.TempDir(), "project-a")
	updated := time.Date(2026, 5, 24, 13, 0, 0, 0, time.UTC)

	writeTestCodexStateDB(t, codexHome, struct {
		id          string
		cwd         string
		title       string
		updatedAtMS int64
		archived    bool
	}{id: "a1", cwd: projectA, title: "archived database title", updatedAtMS: updated.UnixMilli(), archived: true})
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(`{"id":"a1","thread_name":"archived app title","updated_at":"2026-05-24T13:30:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	got := listAllCodexSessions(codexHome)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0: %#v", len(got), got)
	}
}
