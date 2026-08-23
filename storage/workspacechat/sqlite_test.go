package workspacechat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func openTestRepository(t *testing.T, dir string) *Repository {
	t.Helper()
	repository, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return repository
}

func schemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestOpenCreatesOnlyCurrentSchema(t *testing.T) {
	dir := t.TempDir()
	repository := openTestRepository(t, dir)
	if got := schemaVersion(t, repository.db); got != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", got, currentSchemaVersion)
	}
	if err := validateSchema(context.Background(), repository.db); err != nil {
		t.Fatal(err)
	}
	for _, oldTable := range []string{"schema_migrations", "client_selections", "turn_records"} {
		var count int
		if err := repository.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, oldTable).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy table %q still exists", oldTable)
		}
	}
	info, err := os.Stat(filepath.Join(dir, databaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database permissions = %o, want no group/other access", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".workspace_chat.db.new-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary databases remain: %v", matches)
	}
}

func TestOpenRebuildsHealthyVersionMismatchAndReplacesSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, databaseFileName)
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE legacy_value(value TEXT); INSERT INTO legacy_value VALUES('must disappear'); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 固定旧 inode，避免 Linux 在 unlink 后立即创建临时数据库时复用 inode，
	// 让 os.SameFile 把真正的删除重建误判为保留原文件。
	legacyLink := path + ".legacy-link"
	if err := os.Link(path, legacyLink); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(legacyLink); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove legacy database link: %v", err)
		}
	})
	marker := []byte("obsolete-sidecar")
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, marker, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	repository := openTestRepository(t, dir)
	if got := schemaVersion(t, repository.db); got != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", got, currentSchemaVersion)
	}
	var legacyCount int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'legacy_value'`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatal("legacy table survived destructive rebuild")
	}
	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("version mismatch retained the old database file")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		content, err := os.ReadFile(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if reflect.DeepEqual(content, marker) {
			t.Fatalf("obsolete %s sidecar survived rebuild", suffix)
		}
	}
}

func TestOpenMatchingVersionPreservesData(t *testing.T) {
	dir := t.TempDir()
	repository, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	selection := core.WorkspaceChatSelection{
		ClientID: "web:admin", WorkspaceRef: "workspace-one",
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread-one"},
		UpdatedAt:    time.Now(),
	}
	if err := repository.PutSelection(context.Background(), selection); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestRepository(t, dir)
	got, err := reopened.GetSelection(context.Background(), selection.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.WorkspaceRef != selection.WorkspaceRef || got.Conversation != selection.Conversation {
		t.Fatalf("selection after matching-version reopen = %#v", got)
	}
}

func TestOpenRejectsCorruptedDatabaseWithoutDeletingAnything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, databaseFileName)
	files := map[string][]byte{
		path:          []byte("not sqlite"),
		path + "-wal": []byte("wal-marker"),
		path + "-shm": []byte("shm-marker"),
	}
	for name, content := range files {
		if err := os.WriteFile(name, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted corrupted database")
	}
	for name, want := range files {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read preserved file %s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("corrupted database artifact %s changed: got %q, want %q", name, got, want)
		}
	}
}

func TestOpenRejectsCurrentVersionWithInvalidSchemaWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, databaseFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES('keep'); PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "schema definition is invalid") {
		t.Fatalf("Open invalid current schema error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid current schema was modified or rebuilt")
	}
}

func TestOpenConcurrentInitializationAndWrites(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const count = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for index := 0; index < count; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			repository, err := Open(dir)
			if err != nil {
				errorsCh <- fmt.Errorf("Open(%d): %w", index, err)
				return
			}
			defer func() {
				if err := repository.Close(); err != nil {
					errorsCh <- fmt.Errorf("Close(%d): %w", index, err)
				}
			}()
			clientID := fmt.Sprintf("client-%02d", index)
			if err := repository.PutSelection(ctx, core.WorkspaceChatSelection{
				ClientID: clientID, WorkspaceRef: "workspace",
				Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread"},
			}); err != nil {
				errorsCh <- fmt.Errorf("PutSelection(%d): %w", index, err)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	repository := openTestRepository(t, dir)
	var got int
	if err := repository.db.QueryRow("SELECT COUNT(*) FROM selections").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != count {
		t.Fatalf("selection count = %d, want %d", got, count)
	}
}

const (
	openSubprocessHelperEnv    = "CC_CONNECT_WORKSPACE_CHAT_OPEN_HELPER"
	openSubprocessDataDirEnv   = "CC_CONNECT_WORKSPACE_CHAT_DATA_DIR"
	openSubprocessClientIDEnv  = "CC_CONNECT_WORKSPACE_CHAT_CLIENT_ID"
	openSubprocessEnteredEnv   = "CC_CONNECT_WORKSPACE_CHAT_ENTERED_FILE"
	openSubprocessStartEnv     = "CC_CONNECT_WORKSPACE_CHAT_START_FILE"
	openSubprocessOpenedEnv    = "CC_CONNECT_WORKSPACE_CHAT_OPENED_FILE"
	openSubprocessReleaseEnv   = "CC_CONNECT_WORKSPACE_CHAT_RELEASE_FILE"
	openSubprocessWorkspaceRef = "subprocess-workspace"
	openSubprocessConversation = "subprocess-thread"
)

func TestOpenSubprocessHelper(t *testing.T) {
	if os.Getenv(openSubprocessHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	for name, value := range map[string]string{
		openSubprocessDataDirEnv:  os.Getenv(openSubprocessDataDirEnv),
		openSubprocessClientIDEnv: os.Getenv(openSubprocessClientIDEnv),
		openSubprocessEnteredEnv:  os.Getenv(openSubprocessEnteredEnv),
		openSubprocessStartEnv:    os.Getenv(openSubprocessStartEnv),
		openSubprocessOpenedEnv:   os.Getenv(openSubprocessOpenedEnv),
		openSubprocessReleaseEnv:  os.Getenv(openSubprocessReleaseEnv),
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("missing helper environment %s", name)
		}
	}

	if err := os.WriteFile(os.Getenv(openSubprocessEnteredEnv), []byte("entered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForSubprocessFile(os.Getenv(openSubprocessStartEnv), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	repository, err := Open(os.Getenv(openSubprocessDataDirEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close subprocess repository: %v", err)
		}
	}()
	if err := repository.PutSelection(context.Background(), core.WorkspaceChatSelection{
		ClientID:     os.Getenv(openSubprocessClientIDEnv),
		WorkspaceRef: openSubprocessWorkspaceRef,
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: openSubprocessConversation},
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(openSubprocessOpenedEnv), []byte("opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForSubprocessFile(os.Getenv(openSubprocessReleaseEnv), 30*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestOpenConcurrentProcessesCreateOneDatabase(t *testing.T) {
	runConcurrentOpenSubprocesses(t, nil, nil)
}

func TestOpenConcurrentProcessesRebuildVersionMismatch(t *testing.T) {
	runConcurrentOpenSubprocesses(t, func(t *testing.T, dataDir string) {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(dataDir, databaseFileName))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf(
			"CREATE TABLE legacy_value(value TEXT); INSERT INTO legacy_value VALUES('must disappear'); PRAGMA user_version = %d",
			currentSchemaVersion-1,
		)); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}, func(t *testing.T, repository *Repository) {
		t.Helper()
		var count int
		if err := repository.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'legacy_value'",
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("legacy schema survived concurrent destructive rebuild")
		}
	})
}

type openSubprocess struct {
	clientID string
	command  *exec.Cmd
	output   bytes.Buffer
	done     chan error
	finished bool
	result   error
}

func runConcurrentOpenSubprocesses(
	t *testing.T,
	prepare func(t *testing.T, dataDir string),
	verify func(t *testing.T, repository *Repository),
) {
	t.Helper()
	const processCount = 4

	dataDir := t.TempDir()
	if prepare != nil {
		prepare(t, dataDir)
	}
	controlDir := t.TempDir()
	startPath := filepath.Join(controlDir, "start")
	releasePath := filepath.Join(controlDir, "release")
	processContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	startupLock, err := acquireInitializationLock(filepath.Join(dataDir, initializationLockFileName))
	if err != nil {
		t.Fatalf("acquire parent initialization lock: %v", err)
	}

	children := make([]*openSubprocess, 0, processCount)
	completed := false
	defer func() {
		if completed {
			return
		}
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		cancel()
		if startupLock != nil {
			if err := startupLock.release(); err != nil {
				t.Errorf("cleanup parent initialization lock: %v", err)
			}
			startupLock = nil
		}
		for _, child := range children {
			if !child.finished {
				child.result = <-child.done
				child.finished = true
			}
		}
	}()

	enteredPaths := make([]string, 0, processCount)
	openedPaths := make([]string, 0, processCount)
	for index := 0; index < processCount; index++ {
		clientID := fmt.Sprintf("subprocess-client-%02d", index)
		enteredPath := filepath.Join(controlDir, fmt.Sprintf("entered-%02d", index))
		openedPath := filepath.Join(controlDir, fmt.Sprintf("opened-%02d", index))
		child := &openSubprocess{clientID: clientID, done: make(chan error, 1)}
		child.command = exec.CommandContext(
			processContext,
			os.Args[0],
			"-test.run=^TestOpenSubprocessHelper$",
		)
		child.command.Env = append(os.Environ(),
			openSubprocessHelperEnv+"=1",
			openSubprocessDataDirEnv+"="+dataDir,
			openSubprocessClientIDEnv+"="+clientID,
			openSubprocessEnteredEnv+"="+enteredPath,
			openSubprocessStartEnv+"="+startPath,
			openSubprocessOpenedEnv+"="+openedPath,
			openSubprocessReleaseEnv+"="+releasePath,
		)
		child.command.Stdout = &child.output
		child.command.Stderr = &child.output
		if err := child.command.Start(); err != nil {
			t.Fatalf("start subprocess %s: %v", clientID, err)
		}
		go func(child *openSubprocess) {
			child.done <- child.command.Wait()
		}(child)
		children = append(children, child)
		enteredPaths = append(enteredPaths, enteredPath)
		openedPaths = append(openedPaths, openedPath)
	}

	if err := waitForSubprocessFiles(enteredPaths, children, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(startPath, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertSubprocessFilesRemainAbsent(openedPaths, children, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := startupLock.release(); err != nil {
		t.Fatalf("release parent initialization lock: %v", err)
	}
	startupLock = nil
	if err := waitForSubprocessFiles(openedPaths, children, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	assertFinalDatabaseContainsSubprocessWrites(t, dataDir, children, verify)

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if child.finished {
			continue
		}
		select {
		case child.result = <-child.done:
			child.finished = true
		case <-processContext.Done():
			t.Fatalf("wait for subprocess %s: %v", child.clientID, processContext.Err())
		}
		if child.result != nil {
			t.Errorf("subprocess %s: %v\n%s", child.clientID, child.result, child.output.String())
		}
	}
	if t.Failed() {
		return
	}
	assertFinalDatabaseContainsSubprocessWrites(t, dataDir, children, verify)
	completed = true
}

func assertFinalDatabaseContainsSubprocessWrites(
	t *testing.T,
	dataDir string,
	children []*openSubprocess,
	verify func(t *testing.T, repository *Repository),
) {
	t.Helper()
	repository, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close final repository: %v", err)
		}
	}()
	if got := schemaVersion(t, repository.db); got != currentSchemaVersion {
		t.Fatalf("final schema version = %d, want %d", got, currentSchemaVersion)
	}
	for _, child := range children {
		selection, err := repository.GetSelection(context.Background(), child.clientID)
		if err != nil {
			t.Fatal(err)
		}
		if selection == nil || selection.WorkspaceRef != openSubprocessWorkspaceRef ||
			selection.Conversation != (core.ConversationRef{Kind: core.ConversationKindThread, ID: openSubprocessConversation}) {
			t.Fatalf("selection for %s = %#v; subprocess wrote to a replaced database", child.clientID, selection)
		}
	}
	if verify != nil {
		verify(t, repository)
	}
}

func waitForSubprocessFiles(paths []string, children []*openSubprocess, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		missing := ""
		for _, path := range paths {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				missing = path
				break
			} else if err != nil {
				return fmt.Errorf("inspect subprocess marker %s: %w", path, err)
			}
		}
		if missing == "" {
			return nil
		}
		for _, child := range children {
			if child.finished {
				continue
			}
			select {
			case child.result = <-child.done:
				child.finished = true
				return fmt.Errorf("subprocess %s exited before marker %s: %v\n%s", child.clientID, filepath.Base(missing), child.result, child.output.String())
			default:
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for subprocess marker %s", missing)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForSubprocessFile(path string, timeout time.Duration) error {
	return waitForSubprocessFiles([]string{path}, nil, timeout)
}

func assertSubprocessFilesRemainAbsent(paths []string, children []*openSubprocess, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("subprocess completed Open while parent held initialization lock: %s", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect blocked subprocess marker %s: %w", path, err)
			}
		}
		for _, child := range children {
			if child.finished {
				continue
			}
			select {
			case child.result = <-child.done:
				child.finished = true
				return fmt.Errorf("subprocess %s exited while parent held initialization lock: %v\n%s", child.clientID, child.result, child.output.String())
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func TestDraftMaterializationAtomicallySwitchesReferences(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	now := time.Now()
	draft := core.WorkspaceChatDraft{
		ID: "draft-one", OwnerClientID: "web:admin", WorkspaceRef: "workspace-one", State: "draft",
		SettingsPatch: core.NativeThreadSettingsPatch{Model: stringPointer("gpt-5.6")}, CreatedAt: now, UpdatedAt: now,
	}
	ownerSelection := core.WorkspaceChatSelection{
		ClientID: draft.OwnerClientID, WorkspaceRef: draft.WorkspaceRef,
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: draft.ID}, UpdatedAt: now,
	}
	if err := repository.CreateDraftAndSelect(ctx, draft, ownerSelection); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutSelection(ctx, core.WorkspaceChatSelection{
		ClientID: "wecom:user:u1", WorkspaceRef: draft.WorkspaceRef,
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: draft.ID}, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginSubmission(ctx, core.WorkspaceChatSubmission{
		RequestID: "request-one", ClientID: "web:admin", WorkspaceRef: draft.WorkspaceRef,
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: draft.ID}, Kind: "start",
		InputJSON: json.RawMessage(`[{"type":"text","text":"hello"}]`), Status: "pending", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MaterializeDraft(ctx, draft.ID, "request-one", "thread-one", "turn-one"); err != nil {
		t.Fatal(err)
	}
	if err := repository.MaterializeDraft(ctx, draft.ID, "request-one", "thread-one", "turn-one"); err != nil {
		t.Fatalf("idempotent MaterializeDraft: %v", err)
	}
	if err := repository.MaterializeDraft(ctx, draft.ID, "request-one", "thread-other", "turn-one"); err == nil {
		t.Fatal("MaterializeDraft allowed a second thread")
	}
	gotDraft, err := repository.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotDraft == nil || gotDraft.State != "materialized" || gotDraft.ThreadID != "thread-one" || gotDraft.SettingsPatch.Model == nil || *gotDraft.SettingsPatch.Model != "gpt-5.6" {
		t.Fatalf("materialized draft = %#v", gotDraft)
	}
	for _, clientID := range []string{"web:admin", "wecom:user:u1"} {
		selection, err := repository.GetSelection(ctx, clientID)
		if err != nil {
			t.Fatal(err)
		}
		want := core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread-one"}
		if selection == nil || selection.Conversation != want {
			t.Fatalf("selection %s = %#v, want %#v", clientID, selection, want)
		}
	}
	var kind, ref, threadID, nativeTurnID, status string
	var input any
	if err := repository.db.QueryRow(`SELECT conversation_kind, conversation_ref, thread_id, native_turn_id, status, input_json
		FROM native_submissions WHERE request_id = 'request-one'`).Scan(&kind, &ref, &threadID, &nativeTurnID, &status, &input); err != nil {
		t.Fatal(err)
	}
	if kind != "thread" || ref != "thread-one" || threadID != "thread-one" || nativeTurnID != "turn-one" || status != "accepted" || input != nil {
		t.Fatalf("materialized submission = kind %q ref %q thread %q turn %q status %q input %#v", kind, ref, threadID, nativeTurnID, status, input)
	}
}

func TestDraftMaterializationRollsBackAllReferences(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	draft := core.WorkspaceChatDraft{
		ID: "draft", OwnerClientID: "client", WorkspaceRef: "workspace", State: "draft",
	}
	selection := core.WorkspaceChatSelection{
		ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: "draft"},
	}
	if err := repository.CreateDraftAndSelect(ctx, draft, selection); err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginSubmission(ctx, core.WorkspaceChatSubmission{
		RequestID: "request", ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: "draft"},
		Kind:         "start", InputJSON: json.RawMessage(`[{"type":"text","text":"hello"}]`), Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`CREATE TRIGGER reject_materialization
		BEFORE UPDATE OF conversation_kind ON selections
		BEGIN SELECT RAISE(ABORT, 'forced rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if err := repository.MaterializeDraft(ctx, "draft", "request", "thread", "turn"); err == nil {
		t.Fatal("MaterializeDraft succeeded despite forced selection failure")
	}
	gotDraft, err := repository.GetDraft(ctx, "draft")
	if err != nil {
		t.Fatal(err)
	}
	if gotDraft == nil || gotDraft.State != "draft" || gotDraft.ThreadID != "" {
		t.Fatalf("draft escaped rollback: %#v", gotDraft)
	}
	gotSelection, err := repository.GetSelection(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	want := core.ConversationRef{Kind: core.ConversationKindDraft, ID: "draft"}
	if gotSelection == nil || gotSelection.Conversation != want {
		t.Fatalf("selection escaped rollback: %#v", gotSelection)
	}
	var submissionKind, submissionRef, submissionStatus string
	var submissionInput any
	if err := repository.db.QueryRow(`SELECT conversation_kind, conversation_ref, status, input_json
		FROM native_submissions WHERE request_id = 'request'`).Scan(&submissionKind, &submissionRef, &submissionStatus, &submissionInput); err != nil {
		t.Fatal(err)
	}
	if submissionKind != "draft" || submissionRef != "draft" || submissionStatus != "pending" || submissionInput != nil {
		t.Fatalf("draft bindings escaped rollback or accepted input survived: kind %q ref %q status %q input %#v", submissionKind, submissionRef, submissionStatus, submissionInput)
	}
}

func TestCreateDraftAndSelectRollsBackDraftWhenSelectionFails(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	if _, err := repository.db.Exec(`CREATE TRIGGER reject_draft_selection
		BEFORE INSERT ON selections
		BEGIN SELECT RAISE(ABORT, 'forced rollback'); END`); err != nil {
		t.Fatal(err)
	}
	draft := core.WorkspaceChatDraft{ID: "draft", OwnerClientID: "client", WorkspaceRef: "workspace", State: "draft"}
	selection := core.WorkspaceChatSelection{
		ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: "draft"},
	}
	if err := repository.CreateDraftAndSelect(ctx, draft, selection); err == nil {
		t.Fatal("CreateDraftAndSelect succeeded despite forced selection failure")
	}
	gotDraft, err := repository.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotDraft != nil {
		t.Fatalf("draft escaped failed creation transaction: %#v", gotDraft)
	}
	gotSelection, err := repository.GetSelection(ctx, selection.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSelection != nil {
		t.Fatalf("selection escaped failed creation transaction: %#v", gotSelection)
	}
}

func TestSubmissionLifecycleClearsInputAfterAcceptance(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	if err := repository.BeginSubmission(ctx, core.WorkspaceChatSubmission{
		RequestID: "request", ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread"}, ThreadID: "thread",
		Kind: "start", InputJSON: json.RawMessage(`[{"type":"text","text":"temporary input"}]`), Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	unfinished, err := repository.ListUnfinishedSubmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfinished) != 1 || string(unfinished[0].InputJSON) != `[{"type":"text","text":"temporary input"}]` {
		t.Fatalf("unfinished submissions = %#v", unfinished)
	}
	if err := repository.MarkSubmissionAccepted(ctx, "request", "thread", "turn"); err != nil {
		t.Fatal(err)
	}
	var input any
	var status, nativeTurnID string
	if err := repository.db.QueryRow(`SELECT input_json, status, native_turn_id FROM native_submissions WHERE request_id = 'request'`).Scan(&input, &status, &nativeTurnID); err != nil {
		t.Fatal(err)
	}
	if input != nil || status != "accepted" || nativeTurnID != "turn" {
		t.Fatalf("accepted submission = input %#v status %q turn %q", input, status, nativeTurnID)
	}
	if err := repository.FinishSubmission(ctx, "request", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT status FROM native_submissions WHERE request_id = 'request'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("finished status = %q", status)
	}
	unfinished, err = repository.ListUnfinishedSubmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfinished) != 0 {
		t.Fatalf("completed submission remains unfinished: %#v", unfinished)
	}
}

func TestSubmissionNeedsRetryClearsInputWithoutResending(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	input := json.RawMessage(`[{"type":"text","text":"retry explicitly"}]`)
	if err := repository.BeginSubmission(ctx, core.WorkspaceChatSubmission{
		RequestID: "retry", ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread"}, ThreadID: "thread",
		Kind: "start", InputJSON: input, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishSubmission(ctx, "retry", "needs_retry", "terminal_state_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	unfinished, err := repository.ListUnfinishedSubmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfinished) != 1 || unfinished[0].Status != "needs_retry" || len(unfinished[0].InputJSON) != 0 {
		t.Fatalf("needs-retry submission = %#v", unfinished)
	}
}

func TestSubmissionTerminalStateCannotRegressToAccepted(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	if err := repository.BeginSubmission(ctx, core.WorkspaceChatSubmission{
		RequestID: "race", ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread"}, ThreadID: "thread",
		Kind: "start", InputJSON: json.RawMessage(`[{"type":"text","text":"fast turn"}]`), Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishSubmission(ctx, "race", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkSubmissionAccepted(ctx, "race", "thread", "turn"); err != nil {
		t.Fatal(err)
	}
	var status, nativeTurnID string
	var input any
	if err := repository.db.QueryRow(`SELECT status, native_turn_id, input_json FROM native_submissions WHERE request_id = 'race'`).Scan(&status, &nativeTurnID, &input); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || nativeTurnID != "turn" || input != nil {
		t.Fatalf("raced submission = status %q turn %q input %#v", status, nativeTurnID, input)
	}
	if err := repository.FinishSubmission(ctx, "race", "needs_retry", "terminal_state_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	var errorMessage string
	if err := repository.db.QueryRow(`SELECT status, error_message FROM native_submissions WHERE request_id = 'race'`).Scan(&status, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || errorMessage != "" {
		t.Fatalf("terminal submission regressed = status %q error %q", status, errorMessage)
	}
}

func TestPendingInteractionPersistsRequestButNeverResponse(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	record := core.WorkspaceChatInteractionRecord{
		Interaction: core.NativeInteraction{
			ID: "interaction", Kind: "item/tool/requestUserInput", ThreadID: "thread", TurnID: "turn", ItemID: "item",
			RequestID: json.RawMessage(`17`), AllowedDecisions: []json.RawMessage{json.RawMessage(`"allow"`), json.RawMessage(`{"acceptWithPolicy":{"rule":"git status"}}`)},
			Payload: json.RawMessage(`{"questions":[{"id":"password","secret":true}]}`), OccurredAt: time.Now(),
		},
		ConnectionGeneration: 9, Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := repository.PutInteraction(ctx, record); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.ListPendingInteractions(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Interaction.ID != record.Interaction.ID || pending[0].ConnectionGeneration != 9 ||
		string(pending[0].Interaction.RequestID) != "17" || !reflect.DeepEqual(pending[0].Interaction.AllowedDecisions,
		[]json.RawMessage{json.RawMessage(`"allow"`), json.RawMessage(`{"acceptWithPolicy":{"rule":"git status"}}`)}) {
		t.Fatalf("pending interactions = %#v", pending)
	}
	if err := repository.ResolveInteraction(ctx, "interaction", "resolved"); err != nil {
		t.Fatal(err)
	}
	pending, err = repository.ListPendingInteractions(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("resolved interaction remains pending: %#v", pending)
	}
	rows, err := repository.db.Query(`PRAGMA table_info(pending_interactions)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(name, "response") || strings.Contains(name, "answer") || strings.Contains(name, "secret") {
			t.Fatalf("interaction schema persists response data in column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPutInteractionAcceptsExactReplayAndRejectsEveryConflictingField(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	occurredAt := time.UnixMilli(1_700_000_000_000)
	expiresAt := time.UnixMilli(1_700_000_060_000)
	record := core.WorkspaceChatInteractionRecord{
		Interaction: core.NativeInteraction{
			ID: "replayed-interaction", Kind: "item/commandExecution/requestApproval", ThreadID: "thread", TurnID: "turn", ItemID: "item",
			RequestID: json.RawMessage(`17`), ConnectionGeneration: 9,
			AllowedDecisions: []json.RawMessage{json.RawMessage(`"allow"`), json.RawMessage(`{"reject":{}}`)},
			Payload:          json.RawMessage(`{"command":"go test ./..."}`),
			OccurredAt:       occurredAt,
		},
		ConnectionGeneration: 9,
		Status:               "pending",
		ExpiresAt:            expiresAt,
	}
	if err := repository.PutInteraction(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE pending_interactions SET updated_at_ms = 12345 WHERE interaction_id = ?`, record.Interaction.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutInteraction(ctx, record); err != nil {
		t.Fatalf("exact interaction replay: %v", err)
	}
	var updatedAt int64
	if err := repository.db.QueryRow(`SELECT updated_at_ms FROM pending_interactions WHERE interaction_id = ?`, record.Interaction.ID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != 12345 {
		t.Fatalf("exact replay changed persisted record timestamp to %d", updatedAt)
	}

	mutations := []struct {
		name   string
		mutate func(*core.WorkspaceChatInteractionRecord)
	}{
		{name: "request_id", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.RequestID = json.RawMessage(`18`)
		}},
		{name: "generation", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.ConnectionGeneration = 10
			candidate.Interaction.ConnectionGeneration = 10
		}},
		{name: "thread", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.ThreadID = "other-thread"
		}},
		{name: "turn", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.TurnID = "other-turn"
		}},
		{name: "item", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.ItemID = "other-item"
		}},
		{name: "kind", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.Kind = "item/fileChange/requestApproval"
		}},
		{name: "decisions", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.AllowedDecisions = []json.RawMessage{json.RawMessage(`"allow"`)}
		}},
		{name: "payload", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.Payload = json.RawMessage(`{"command":"go vet ./..."}`)
		}},
		{name: "occurred_at", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.Interaction.OccurredAt = occurredAt.Add(time.Second)
		}},
		{name: "expires_at", mutate: func(candidate *core.WorkspaceChatInteractionRecord) {
			candidate.ExpiresAt = expiresAt.Add(time.Second)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := record
			mutation.mutate(&candidate)
			err := repository.PutInteraction(ctx, candidate)
			if err == nil || !strings.Contains(err.Error(), "conflicts with existing record") {
				t.Fatalf("conflicting interaction error = %v", err)
			}
		})
	}

	inconsistentGeneration := record
	inconsistentGeneration.ConnectionGeneration = 10
	if err := repository.PutInteraction(ctx, inconsistentGeneration); err == nil || !strings.Contains(err.Error(), "generations disagree") {
		t.Fatalf("inconsistent generation error = %v", err)
	}
	pending, err := repository.ListPendingInteractions(ctx, record.Interaction.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Interaction.ID != record.Interaction.ID ||
		pending[0].ConnectionGeneration != record.ConnectionGeneration ||
		string(pending[0].Interaction.RequestID) != string(record.Interaction.RequestID) ||
		string(pending[0].Interaction.Payload) != string(record.Interaction.Payload) ||
		pending[0].Interaction.OccurredAt.UnixMilli() != occurredAt.UnixMilli() ||
		pending[0].ExpiresAt.UnixMilli() != expiresAt.UnixMilli() {
		t.Fatalf("conflicting replay changed pending interaction: %#v", pending)
	}

	if err := repository.ResolveInteraction(ctx, record.Interaction.ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutInteraction(ctx, record); err == nil || !strings.Contains(err.Error(), "conflicts with existing record") {
		t.Fatalf("status-conflicting interaction error = %v", err)
	}
}

func TestExpirePendingInteractionsDoesNotOverwriteTerminalRecords(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"pending", "resolved"} {
		if err := repository.PutInteraction(ctx, core.WorkspaceChatInteractionRecord{
			Interaction: core.NativeInteraction{
				ID: id, Kind: "item/commandExecution/requestApproval", ThreadID: "thread", RequestID: json.RawMessage(`"request-` + id + `"`),
				Payload: json.RawMessage(`{"command":"go test"}`), OccurredAt: time.Now(),
			},
			ConnectionGeneration: 1, Status: "pending",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.ResolveInteraction(ctx, "resolved", "resolved"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ExpirePendingInteractions(ctx, "connection_lost"); err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string)
	rows, err := repository.db.Query(`SELECT interaction_id, status FROM pending_interactions`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		statuses[id] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if statuses["pending"] != "connection_lost" || statuses["resolved"] != "resolved" {
		t.Fatalf("interaction statuses after expiry = %#v", statuses)
	}
}

func TestSettingIntentLifecycle(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	mode := "plan"
	intent := core.WorkspaceChatSettingIntent{
		ID: "intent", WorkspaceRef: "workspace", ThreadID: "thread",
		Patch: core.NativeThreadSettingsPatch{Mode: &mode}, Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repository.PutSettingIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.ListPendingSettingIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != intent.ID || pending[0].Patch.Mode == nil || *pending[0].Patch.Mode != mode {
		t.Fatalf("pending setting intents = %#v", pending)
	}
	if err := repository.ResolveSettingIntent(ctx, intent.ID, "failed", "operation_failed"); err != nil {
		t.Fatal(err)
	}
	var patchJSON, status, errorMessage string
	if err := repository.db.QueryRow(`SELECT patch_json, status, error_message FROM thread_setting_intents WHERE intent_id = ?`, intent.ID).Scan(&patchJSON, &status, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(patchJSON)) || status != "failed" || errorMessage != "operation_failed" {
		t.Fatalf("resolved setting intent = patch %q status %q error %q", patchJSON, status, errorMessage)
	}
	pending, err = repository.ListPendingSettingIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("resolved setting intent remains pending: %#v", pending)
	}
}

func TestDeliveryLifecycleRejectsSensitiveMetadata(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	ctx := context.Background()
	base := core.WorkspaceChatDeliveryRecord{
		ID: "delivery", ClientID: "client", WorkspaceRef: "workspace",
		Conversation: core.ConversationRef{Kind: core.ConversationKindThread, ID: "thread"},
		RequestID:    "request", Transport: "web", Destination: "subscriber", Status: "pending",
	}
	for index, metadata := range []json.RawMessage{
		json.RawMessage(`{"replyCtx":{"message":"opaque"}}`),
		json.RawMessage(`{"nested":{"api_token":"secret"}}`),
		json.RawMessage(`{"credential":"secret"}`),
	} {
		delivery := base
		delivery.ID = fmt.Sprintf("sensitive-%d", index)
		delivery.Metadata = metadata
		if err := repository.PutDelivery(ctx, delivery); err == nil {
			t.Fatalf("PutDelivery accepted sensitive metadata %s", metadata)
		}
	}
	base.Metadata = json.RawMessage(`{"attempt":1,"event":"turn_completed"}`)
	if err := repository.PutDelivery(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishDelivery(ctx, base.ID, "delivered", ""); err != nil {
		t.Fatal(err)
	}
	var status, metadata, errorMessage string
	if err := repository.db.QueryRow(`SELECT status, metadata_json, error_message FROM delivery_records WHERE delivery_id = ?`, base.ID).Scan(&status, &metadata, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || metadata != `{"attempt":1,"event":"turn_completed"}` || errorMessage != "" {
		t.Fatalf("delivery = status %q metadata %q error %q", status, metadata, errorMessage)
	}
}

func TestRepositoryCorruptedMenuReturnsError(t *testing.T) {
	repository := openTestRepository(t, t.TempDir())
	if _, err := repository.db.Exec("PRAGMA ignore_check_constraints = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`INSERT INTO menu_snapshots(client_id, kind, revision, item_ids_json, updated_at_ms)
		VALUES('client', 'threads', 'bad', '{', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetMenu(context.Background(), "client", "threads"); err == nil {
		t.Fatal("GetMenu accepted corrupted JSON")
	}
}

func stringPointer(value string) *string { return &value }
