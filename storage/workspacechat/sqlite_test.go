package workspacechat

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestRepositoryMigrationAndRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	repository, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	selection := core.WorkspaceChatSelection{
		ClientID: "wecom:user:u1", WorkspaceRef: "ws_one", ThreadID: "thread-1", UpdatedAt: time.Now(),
	}
	if err := repository.PutSelection(ctx, selection); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutMenu(ctx, core.WorkspaceChatMenuSnapshot{
		ClientID: selection.ClientID, Kind: "projects", Revision: "r1", ItemIDs: []string{"ws_one", "ws_two"}, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginTurn(ctx, core.WorkspaceChatTurnRecord{
		RequestID: "request-1", ClientID: selection.ClientID, WorkspaceRef: selection.WorkspaceRef,
		ThreadID: selection.ThreadID, Status: "in_progress", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	gotSelection, err := reopened.GetSelection(ctx, selection.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSelection == nil || gotSelection.WorkspaceRef != selection.WorkspaceRef || gotSelection.ThreadID != selection.ThreadID {
		t.Fatalf("selection after restart = %#v", gotSelection)
	}
	menu, err := reopened.GetMenu(ctx, selection.ClientID, "projects")
	if err != nil {
		t.Fatal(err)
	}
	if menu == nil || len(menu.ItemIDs) != 2 || menu.ItemIDs[1] != "ws_two" {
		t.Fatalf("menu after restart = %#v", menu)
	}
	var status string
	if err := reopened.db.QueryRowContext(ctx, "SELECT status FROM turn_records WHERE request_id = ?", "request-1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" {
		t.Fatalf("recovered turn status = %q, want interrupted", status)
	}
	var version int
	if err := reopened.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestRepositoryCorruptedMenuReturnsError(t *testing.T) {
	repository, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	_, err = repository.db.Exec(`INSERT INTO menu_snapshots(client_id, kind, revision, item_ids_json, updated_at_ms)
		VALUES('client', 'threads', 'bad', '{', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetMenu(context.Background(), "client", "threads"); err == nil {
		t.Fatal("GetMenu accepted corrupted JSON")
	}
}

func TestRepositoryRejectsCorruptedDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workspace_chat.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted corrupted database")
	}
}

func TestRepositoryConcurrentSelectionAndMenuWrites(t *testing.T) {
	repository, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	ctx := context.Background()
	var wg sync.WaitGroup
	for index := 0; index < 24; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			clientID := "client-" + string(rune('a'+index))
			if err := repository.PutSelection(ctx, core.WorkspaceChatSelection{
				ClientID: clientID, WorkspaceRef: "workspace", ThreadID: "thread", UpdatedAt: time.Now(),
			}); err != nil {
				t.Errorf("PutSelection: %v", err)
			}
			if err := repository.PutMenu(ctx, core.WorkspaceChatMenuSnapshot{
				ClientID: clientID, Kind: "projects", Revision: "r", ItemIDs: []string{"workspace"}, UpdatedAt: time.Now(),
			}); err != nil {
				t.Errorf("PutMenu: %v", err)
			}
		}()
	}
	wg.Wait()
	for index := 0; index < 24; index++ {
		clientID := "client-" + string(rune('a'+index))
		selection, err := repository.GetSelection(ctx, clientID)
		if err != nil || selection == nil {
			t.Fatalf("GetSelection(%q) = %#v, %v", clientID, selection, err)
		}
		menu, err := repository.GetMenu(ctx, clientID, "projects")
		if err != nil || menu == nil || len(menu.ItemIDs) != 1 {
			t.Fatalf("GetMenu(%q) = %#v, %v", clientID, menu, err)
		}
	}
}
