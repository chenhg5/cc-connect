package workspacechat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

const (
	currentSchemaVersion       = 3
	databaseFileName           = "workspace_chat.db"
	initializationLockFileName = "workspace_chat.db.lock"
)

var openMu sync.Mutex

type Repository struct{ db *sql.DB }

var _ core.WorkspaceChatRepository = (*Repository)(nil)

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Open 只接受当前 schema。健康的旧版本会被精确重建，损坏文件会原样保留并报错。
func Open(dataDir string) (repository *Repository, returnErr error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, fmt.Errorf("workspace chat sqlite: data directory is required")
	}
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: resolve data directory: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: create data directory: %w", err)
	}
	path := filepath.Join(absDir, databaseFileName)

	// 进程内 mutex 与跨进程文件锁共同覆盖检查、重建、安装和最终连接校验。
	openMu.Lock()
	defer openMu.Unlock()
	initializationLock, err := acquireInitializationLock(filepath.Join(absDir, initializationLockFileName))
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: acquire initialization lock: %w", err)
	}
	defer func() {
		if releaseErr := initializationLock.release(); releaseErr != nil {
			if repository != nil {
				closeErr := repository.Close()
				repository = nil
				if closeErr != nil {
					returnErr = errors.Join(returnErr, fmt.Errorf("workspace chat sqlite: close after initialization lock failure: %w", closeErr))
				}
			}
			returnErr = errors.Join(returnErr, fmt.Errorf("workspace chat sqlite: release initialization lock: %w", releaseErr))
		}
	}()

	exists, err := regularDatabaseExists(path)
	if err != nil {
		return nil, err
	}
	if exists {
		version, inspectErr := inspectDatabase(path)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if version != currentSchemaVersion {
			if err := removeDatabaseFiles(path); err != nil {
				return nil, err
			}
			exists = false
		}
	}
	if !exists {
		if err := removeOrphanSidecars(path); err != nil {
			return nil, err
		}
		if err := createDatabaseAtomically(path); err != nil {
			return nil, err
		}
	}

	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	if err := configureDatabase(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := validateCurrentDatabase(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Repository{db: db}, nil
}

func regularDatabaseExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("workspace chat sqlite: inspect database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("workspace chat sqlite: database path is not a regular file: %s", path)
	}
	return true, nil
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("workspace chat sqlite: connect: %w", err)
	}
	return db, nil
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: open read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("workspace chat sqlite: connect read-only: %w", err)
	}
	return db, nil
}

func sqliteDSN(path string, readOnly bool) string {
	dsn := &url.URL{Scheme: "file", Path: path}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		query.Set("mode", "ro")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func inspectDatabase(path string) (int, error) {
	if err := validateSQLiteHeader(path); err != nil {
		return 0, err
	}
	db, err := openSQLiteReadOnly(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("workspace chat sqlite: read schema version: %w", err)
	}
	if err := checkIntegrity(ctx, db, "quick_check"); err != nil {
		return 0, err
	}
	if err := checkIntegrity(ctx, db, "integrity_check"); err != nil {
		return 0, err
	}
	if version == currentSchemaVersion {
		if err := validateSchema(ctx, db); err != nil {
			return 0, err
		}
	}
	return version, nil
}

func validateSQLiteHeader(path string) (resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: open database for validation: %w", err)
	}
	defer func() {
		if err := file.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("workspace chat sqlite: close database after validation: %w", err)
		}
	}()

	const sqliteHeader = "SQLite format 3\x00"
	header := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("workspace chat sqlite: invalid database header: %w", err)
	}
	if string(header) != sqliteHeader {
		return fmt.Errorf("workspace chat sqlite: invalid database header")
	}
	return nil
}

func checkIntegrity(ctx context.Context, db *sql.DB, pragma string) (resultErr error) {
	rows, err := db.QueryContext(ctx, "PRAGMA "+pragma)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %s: %w", pragma, err)
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("workspace chat sqlite: close %s rows: %w", pragma, err)
		}
	}()
	var problems []string
	seen := false
	for rows.Next() {
		seen = true
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("workspace chat sqlite: scan %s: %w", pragma, err)
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			problems = append(problems, result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("workspace chat sqlite: read %s: %w", pragma, err)
	}
	if !seen {
		return fmt.Errorf("workspace chat sqlite: %s returned no result", pragma)
	}
	if len(problems) > 0 {
		return fmt.Errorf("workspace chat sqlite: %s failed: %s", pragma, strings.Join(problems, "; "))
	}
	return nil
}

func removeDatabaseFiles(path string) error {
	candidates := []string{path + "-wal", path + "-shm", path}
	for _, candidate := range candidates {
		if err := validateRemovableFile(candidate); err != nil {
			return fmt.Errorf("workspace chat sqlite: validate obsolete database file %s: %w", filepath.Base(candidate), err)
		}
	}
	for _, candidate := range candidates {
		if err := removeExactFile(candidate); err != nil {
			return fmt.Errorf("workspace chat sqlite: remove obsolete database file %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func removeOrphanSidecars(path string) error {
	for _, candidate := range []string{path + "-wal", path + "-shm"} {
		if err := removeExactFile(candidate); err != nil {
			return fmt.Errorf("workspace chat sqlite: remove orphan database file %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func removeExactFile(path string) error {
	if err := validateRemovableFile(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validateRemovableFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to remove directory")
	}
	return nil
}

func createDatabaseAtomically(path string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".workspace_chat.db.new-*")
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: create temporary database: %w", err)
	}
	temporaryPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("workspace chat sqlite: close temporary database: %w", err)
	}
	defer func() {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			_ = os.Remove(temporaryPath + suffix)
		}
	}()

	db, err := openSQLite(temporaryPath)
	if err != nil {
		return err
	}
	if err := createCurrentSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return err
	}
	if err := validateCurrentDatabase(context.Background(), db); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("workspace chat sqlite: close initialized database: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return fmt.Errorf("workspace chat sqlite: secure initialized database: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("workspace chat sqlite: install initialized database: %w", err)
	}
	return nil
}

func createCurrentSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin schema creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`CREATE TABLE selections (
			client_id TEXT PRIMARY KEY,
			workspace_ref TEXT NOT NULL,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('draft', 'thread')),
			conversation_ref TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE menu_snapshots (
			client_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			revision TEXT NOT NULL,
			item_ids_json TEXT NOT NULL CHECK(json_valid(item_ids_json)),
			updated_at_ms INTEGER NOT NULL,
			PRIMARY KEY(client_id, kind)
		)`,
		`CREATE TABLE conversation_drafts (
			draft_id TEXT PRIMARY KEY,
			owner_client_id TEXT NOT NULL,
			workspace_ref TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('draft', 'materialization_uncertain', 'materialized')),
			thread_id TEXT NOT NULL DEFAULT '',
			settings_patch_json TEXT NOT NULL CHECK(json_valid(settings_patch_json)),
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX conversation_drafts_owner_updated_idx ON conversation_drafts(owner_client_id, updated_at_ms DESC)`,
		`CREATE TABLE native_submissions (
			request_id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			workspace_ref TEXT NOT NULL,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('draft', 'thread')),
			conversation_ref TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			native_turn_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			expected_turn_id TEXT NOT NULL DEFAULT '',
			input_json TEXT CHECK(input_json IS NULL OR json_valid(input_json)),
			status TEXT NOT NULL CHECK(status IN ('pending', 'accepted', 'completed', 'failed', 'cancelled', 'interrupted', 'needs_retry')),
			error_message TEXT NOT NULL DEFAULT '',
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX native_submissions_conversation_updated_idx ON native_submissions(conversation_kind, conversation_ref, updated_at_ms DESC)`,
		`CREATE INDEX native_submissions_status_updated_idx ON native_submissions(status, updated_at_ms)`,
		`CREATE TABLE pending_interactions (
			interaction_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			turn_id TEXT NOT NULL DEFAULT '',
			item_id TEXT NOT NULL DEFAULT '',
			request_id_json TEXT NOT NULL CHECK(json_valid(request_id_json)),
			connection_generation INTEGER NOT NULL,
			allowed_decisions_json TEXT NOT NULL CHECK(json_valid(allowed_decisions_json)),
			payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
			status TEXT NOT NULL CHECK(status IN ('pending', 'resolved', 'connection_lost', 'expired', 'failed', 'cancelled')),
			occurred_at_ms INTEGER NOT NULL,
			expires_at_ms INTEGER NOT NULL DEFAULT 0,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX pending_interactions_thread_status_idx ON pending_interactions(thread_id, status, occurred_at_ms)`,
		`CREATE TABLE delivery_records (
			delivery_id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			workspace_ref TEXT NOT NULL,
			conversation_kind TEXT NOT NULL CHECK(conversation_kind IN ('draft', 'thread')),
			conversation_ref TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			transport TEXT NOT NULL,
			destination_ref TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pending', 'delivered', 'failed')),
			error_message TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL CHECK(json_valid(metadata_json)),
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX delivery_records_request_idx ON delivery_records(request_id, updated_at_ms DESC)`,
		`CREATE TABLE thread_setting_intents (
			intent_id TEXT PRIMARY KEY,
			workspace_ref TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			patch_json TEXT NOT NULL CHECK(json_valid(patch_json)),
			status TEXT NOT NULL CHECK(status IN ('pending', 'applied', 'failed', 'needs_retry')),
			error_message TEXT NOT NULL DEFAULT '',
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX thread_setting_intents_thread_status_idx ON thread_setting_intents(thread_id, status, updated_at_ms)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("workspace chat sqlite: create current schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("workspace chat sqlite: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workspace chat sqlite: commit schema creation: %w", err)
	}
	return nil
}

func configureDatabase(ctx context.Context, db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("workspace chat sqlite: %s: %w", pragma, err)
		}
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("workspace chat sqlite: enable WAL: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("workspace chat sqlite: enable WAL returned %q", journalMode)
	}
	return nil
}

func validateCurrentDatabase(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("workspace chat sqlite: read current schema version: %w", err)
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("workspace chat sqlite: schema version %d does not match current version %d", version, currentSchemaVersion)
	}
	return validateSchema(ctx, db)
}

func validateSchema(ctx context.Context, db *sql.DB) (resultErr error) {
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: open schema reference: %w", err)
	}
	reference.SetMaxOpenConns(1)
	defer func() {
		if err := reference.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("workspace chat sqlite: close schema reference: %w", err)
		}
	}()
	if err := createCurrentSchema(ctx, reference); err != nil {
		return fmt.Errorf("workspace chat sqlite: create schema reference: %w", err)
	}
	expected, err := schemaDefinition(ctx, reference)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: read schema reference: %w", err)
	}
	actual, err := schemaDefinition(ctx, db)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: read current schema: %w", err)
	}
	if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
		return fmt.Errorf("workspace chat sqlite: schema definition is invalid: got %v, want %v", actual, expected)
	}
	return nil
}

func schemaDefinition(ctx context.Context, db *sql.DB) (definitions []string, resultErr error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%' AND type IN ('table', 'index') ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			definitions = nil
			resultErr = fmt.Errorf("close schema definition rows: %w", err)
		}
	}()
	for rows.Next() {
		var objectType, name, tableName, statement string
		if err := rows.Scan(&objectType, &name, &tableName, &statement); err != nil {
			return nil, err
		}
		definitions = append(definitions, strings.Join([]string{
			objectType, name, tableName, strings.Join(strings.Fields(statement), " "),
		}, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return definitions, nil
}

func validateConversation(ref core.ConversationRef) error {
	if ref.Kind != core.ConversationKindDraft && ref.Kind != core.ConversationKindThread {
		return fmt.Errorf("conversation kind must be draft or thread")
	}
	if strings.TrimSpace(ref.ID) == "" {
		return fmt.Errorf("conversation reference is required")
	}
	return nil
}

func validateJSON(raw json.RawMessage, field string, emptyValue string) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(emptyValue)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%s must be valid JSON", field)
	}
	return raw, nil
}

func validateDeliveryMetadata(raw json.RawMessage) ([]byte, error) {
	validated, err := validateJSON(raw, "delivery metadata", "{}")
	if err != nil {
		return nil, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(validated, &metadata); err != nil {
		return nil, fmt.Errorf("delivery metadata must be a JSON object: %w", err)
	}
	if metadata == nil {
		return nil, fmt.Errorf("delivery metadata must be a JSON object")
	}
	if key := forbiddenMetadataKey(metadata); key != "" {
		return nil, fmt.Errorf("delivery metadata contains forbidden field %q", key)
	}
	return validated, nil
}

func forbiddenMetadataKey(value any) string {
	forbidden := map[string]struct{}{
		"apikey": {}, "authorization": {}, "cookie": {}, "credential": {}, "password": {},
		"replyctx": {}, "secret": {}, "token": {},
	}
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
				for blocked := range forbidden {
					if strings.Contains(normalized, blocked) {
						return key
					}
				}
				if found := visit(nested); found != "" {
					return found
				}
			}
		case []any:
			for _, nested := range typed {
				if found := visit(nested); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return visit(value)
}

func unixMilliOrNow(value time.Time) int64 {
	if value.IsZero() {
		return time.Now().UnixMilli()
	}
	return value.UnixMilli()
}

func rowsAffectedExactlyOne(result sql.Result, operation string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %s rows: %w", operation, err)
	}
	if rows != 1 {
		return fmt.Errorf("workspace chat sqlite: %s target not found", operation)
	}
	return nil
}

func (r *Repository) GetSelection(ctx context.Context, clientID string) (*core.WorkspaceChatSelection, error) {
	var selection core.WorkspaceChatSelection
	var kind string
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT client_id, workspace_ref, conversation_kind, conversation_ref, updated_at_ms
		FROM selections WHERE client_id = ?`, clientID).Scan(&selection.ClientID, &selection.WorkspaceRef, &kind, &selection.Conversation.ID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: get selection: %w", err)
	}
	selection.Conversation.Kind = core.ConversationKind(kind)
	selection.UpdatedAt = time.UnixMilli(updatedAt)
	return &selection, nil
}

func (r *Repository) PutSelection(ctx context.Context, selection core.WorkspaceChatSelection) error {
	return putSelection(ctx, r.db, selection)
}

func putSelection(ctx context.Context, executor sqlExecutor, selection core.WorkspaceChatSelection) error {
	if strings.TrimSpace(selection.ClientID) == "" || strings.TrimSpace(selection.WorkspaceRef) == "" {
		return fmt.Errorf("workspace chat sqlite: selection client and workspace are required")
	}
	if err := validateConversation(selection.Conversation); err != nil {
		return fmt.Errorf("workspace chat sqlite: selection: %w", err)
	}
	_, err := executor.ExecContext(ctx, `INSERT INTO selections(client_id, workspace_ref, conversation_kind, conversation_ref, updated_at_ms)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(client_id) DO UPDATE SET workspace_ref=excluded.workspace_ref,
		conversation_kind=excluded.conversation_kind, conversation_ref=excluded.conversation_ref,
		updated_at_ms=excluded.updated_at_ms`, selection.ClientID, selection.WorkspaceRef, selection.Conversation.Kind,
		selection.Conversation.ID, unixMilliOrNow(selection.UpdatedAt))
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put selection: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSelection(ctx context.Context, clientID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM selections WHERE client_id = ?", clientID); err != nil {
		return fmt.Errorf("workspace chat sqlite: delete selection: %w", err)
	}
	return nil
}

func (r *Repository) GetMenu(ctx context.Context, clientID, kind string) (*core.WorkspaceChatMenuSnapshot, error) {
	var snapshot core.WorkspaceChatMenuSnapshot
	var itemIDsJSON string
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT client_id, kind, revision, item_ids_json, updated_at_ms
		FROM menu_snapshots WHERE client_id = ? AND kind = ?`, clientID, kind).Scan(&snapshot.ClientID, &snapshot.Kind, &snapshot.Revision, &itemIDsJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: get menu: %w", err)
	}
	if err := json.Unmarshal([]byte(itemIDsJSON), &snapshot.ItemIDs); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: corrupt menu snapshot for %q/%q: %w", clientID, kind, err)
	}
	for index, itemID := range snapshot.ItemIDs {
		if strings.TrimSpace(itemID) == "" {
			return nil, fmt.Errorf("workspace chat sqlite: corrupt menu snapshot for %q/%q: empty item at %d", clientID, kind, index)
		}
	}
	snapshot.UpdatedAt = time.UnixMilli(updatedAt)
	return &snapshot, nil
}

func (r *Repository) PutMenu(ctx context.Context, snapshot core.WorkspaceChatMenuSnapshot) error {
	if strings.TrimSpace(snapshot.ClientID) == "" || strings.TrimSpace(snapshot.Kind) == "" || strings.TrimSpace(snapshot.Revision) == "" {
		return fmt.Errorf("workspace chat sqlite: menu client, kind and revision are required")
	}
	for index, itemID := range snapshot.ItemIDs {
		if strings.TrimSpace(itemID) == "" {
			return fmt.Errorf("workspace chat sqlite: menu item %d is empty", index)
		}
	}
	itemsJSON, err := json.Marshal(snapshot.ItemIDs)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode menu: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO menu_snapshots(client_id, kind, revision, item_ids_json, updated_at_ms)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(client_id, kind) DO UPDATE SET revision=excluded.revision,
		item_ids_json=excluded.item_ids_json, updated_at_ms=excluded.updated_at_ms`, snapshot.ClientID, snapshot.Kind,
		snapshot.Revision, string(itemsJSON), unixMilliOrNow(snapshot.UpdatedAt))
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put menu: %w", err)
	}
	return nil
}

func createDraft(ctx context.Context, executor sqlExecutor, draft core.WorkspaceChatDraft) error {
	if strings.TrimSpace(draft.ID) == "" || strings.TrimSpace(draft.OwnerClientID) == "" || strings.TrimSpace(draft.WorkspaceRef) == "" || strings.TrimSpace(draft.State) == "" {
		return fmt.Errorf("workspace chat sqlite: draft id, owner, workspace and state are required")
	}
	if strings.TrimSpace(draft.ThreadID) != "" {
		return fmt.Errorf("workspace chat sqlite: new draft cannot already reference a thread")
	}
	if draft.State != "draft" {
		return fmt.Errorf("workspace chat sqlite: new draft state must be draft")
	}
	settingsJSON, err := json.Marshal(draft.SettingsPatch)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode draft settings: %w", err)
	}
	createdAt := unixMilliOrNow(draft.CreatedAt)
	updatedAt := draft.UpdatedAt.UnixMilli()
	if draft.UpdatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO conversation_drafts(draft_id, owner_client_id, workspace_ref, state,
		thread_id, settings_patch_json, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, '', ?, ?, ?)`,
		draft.ID, draft.OwnerClientID, draft.WorkspaceRef, draft.State, string(settingsJSON), createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: create draft: %w", err)
	}
	return nil
}

// CreateDraftAndSelect 是新建工作区会话的唯一事务提交点。
func (r *Repository) CreateDraftAndSelect(ctx context.Context, draft core.WorkspaceChatDraft, selection core.WorkspaceChatSelection) error {
	if selection.ClientID != draft.OwnerClientID || selection.WorkspaceRef != draft.WorkspaceRef ||
		selection.Conversation.Kind != core.ConversationKindDraft || selection.Conversation.ID != draft.ID {
		return fmt.Errorf("workspace chat sqlite: draft selection must reference the same owner, workspace and draft")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin draft creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := createDraft(ctx, tx, draft); err != nil {
		return err
	}
	if err := putSelection(ctx, tx, selection); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workspace chat sqlite: commit draft creation: %w", err)
	}
	return nil
}

func (r *Repository) GetDraft(ctx context.Context, draftID string) (*core.WorkspaceChatDraft, error) {
	var draft core.WorkspaceChatDraft
	var settingsJSON string
	var createdAt, updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT draft_id, owner_client_id, workspace_ref, state, thread_id,
		settings_patch_json, created_at_ms, updated_at_ms FROM conversation_drafts WHERE draft_id = ?`, draftID).Scan(
		&draft.ID, &draft.OwnerClientID, &draft.WorkspaceRef, &draft.State, &draft.ThreadID, &settingsJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: get draft: %w", err)
	}
	if err := json.Unmarshal([]byte(settingsJSON), &draft.SettingsPatch); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: corrupt draft settings for %q: %w", draftID, err)
	}
	draft.CreatedAt = time.UnixMilli(createdAt)
	draft.UpdatedAt = time.UnixMilli(updatedAt)
	return &draft, nil
}

func (r *Repository) MarkDraftMaterializationUncertain(ctx context.Context, draftID string) error {
	if strings.TrimSpace(draftID) == "" {
		return fmt.Errorf("workspace chat sqlite: uncertain draft id is required")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE conversation_drafts
		SET state = 'materialization_uncertain',
		updated_at_ms = CASE WHEN state = 'draft' THEN ? ELSE updated_at_ms END
		WHERE draft_id = ? AND state IN ('draft', 'materialization_uncertain')`, time.Now().UnixMilli(), draftID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: mark draft materialization uncertain: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: mark draft materialization uncertain rows: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var state string
	if err := r.db.QueryRowContext(ctx, "SELECT state FROM conversation_drafts WHERE draft_id = ?", draftID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workspace chat sqlite: mark draft materialization uncertain target not found")
		}
		return fmt.Errorf("workspace chat sqlite: read draft materialization state: %w", err)
	}
	if state == "materialization_uncertain" {
		return nil
	}
	return fmt.Errorf("workspace chat sqlite: cannot mark draft %q materialization uncertain from state %q", draftID, state)
}

func (r *Repository) UpdateDraftSettings(ctx context.Context, draftID, ownerClientID, workspaceRef string, patch core.NativeThreadSettingsPatch, updatedAt time.Time) error {
	if strings.TrimSpace(draftID) == "" || strings.TrimSpace(ownerClientID) == "" || strings.TrimSpace(workspaceRef) == "" {
		return fmt.Errorf("workspace chat sqlite: draft settings target is required")
	}
	settingsJSON, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode draft settings: %w", err)
	}
	updatedAtMS := updatedAt.UnixMilli()
	if updatedAt.IsZero() {
		updatedAtMS = time.Now().UnixMilli()
	}
	result, err := r.db.ExecContext(ctx, `UPDATE conversation_drafts SET settings_patch_json = ?, updated_at_ms = ?
		WHERE draft_id = ? AND owner_client_id = ? AND workspace_ref = ? AND state = 'draft'`,
		string(settingsJSON), updatedAtMS, draftID, ownerClientID, workspaceRef)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: update draft settings: %w", err)
	}
	return rowsAffectedExactlyOne(result, "update draft settings")
}

// MaterializeDraft 原子提交首个 Turn、绑定原生 thread，并切换所有草稿引用。
func (r *Repository) MaterializeDraft(ctx context.Context, draftID, requestID, threadID, nativeTurnID string) error {
	if strings.TrimSpace(draftID) == "" || strings.TrimSpace(requestID) == "" || strings.TrimSpace(threadID) == "" || strings.TrimSpace(nativeTurnID) == "" {
		return fmt.Errorf("workspace chat sqlite: draft, request, thread and native turn are required")
	}
	// 调用方只有在 App Server 接受首个 Turn 后才会物化。正文先独立清除，
	// 即使后续绑定草稿的事务失败或进程退出，也不会把已接受正文留在 SQLite。
	redacted, err := r.db.ExecContext(ctx, `UPDATE native_submissions SET input_json = NULL, updated_at_ms = ? WHERE request_id = ?`, time.Now().UnixMilli(), requestID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: clear accepted draft submission input: %w", err)
	}
	if err := rowsAffectedExactlyOne(redacted, "clear accepted draft submission input"); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin draft materialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var draftState, existingThread string
	if err := tx.QueryRowContext(ctx, "SELECT state, thread_id FROM conversation_drafts WHERE draft_id = ?", draftID).Scan(&draftState, &existingThread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workspace chat sqlite: materialize draft target not found")
		}
		return fmt.Errorf("workspace chat sqlite: read draft for materialization: %w", err)
	}
	if existingThread != "" && existingThread != threadID {
		return fmt.Errorf("workspace chat sqlite: draft %q is already materialized as thread %q", draftID, existingThread)
	}
	if draftState != "draft" && (draftState != "materialized" || existingThread != threadID) {
		return fmt.Errorf("workspace chat sqlite: draft %q has invalid materialization state %q", draftID, draftState)
	}
	var submissionKind, submissionRef, submissionThread, submissionTurn string
	if err := tx.QueryRowContext(ctx, `SELECT conversation_kind, conversation_ref, thread_id, native_turn_id
		FROM native_submissions WHERE request_id = ?`, requestID).Scan(
		&submissionKind, &submissionRef, &submissionThread, &submissionTurn,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workspace chat sqlite: materialize submission target not found")
		}
		return fmt.Errorf("workspace chat sqlite: read submission for materialization: %w", err)
	}
	if submissionKind != string(core.ConversationKindDraft) || submissionRef != draftID {
		if submissionKind != string(core.ConversationKindThread) || submissionRef != threadID {
			return fmt.Errorf("workspace chat sqlite: submission %q does not reference draft %q", requestID, draftID)
		}
	}
	if submissionThread != "" && submissionThread != threadID {
		return fmt.Errorf("workspace chat sqlite: submission %q already references thread %q", requestID, submissionThread)
	}
	if submissionTurn != "" && submissionTurn != nativeTurnID {
		return fmt.Errorf("workspace chat sqlite: submission %q already references native turn %q", requestID, submissionTurn)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_drafts SET state = 'materialized', thread_id = ?, updated_at_ms = ? WHERE draft_id = ?`, threadID, now, draftID); err != nil {
		return fmt.Errorf("workspace chat sqlite: materialize draft: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE selections SET conversation_kind = 'thread', conversation_ref = ?, updated_at_ms = ?
		WHERE conversation_kind = 'draft' AND conversation_ref = ?`, threadID, now, draftID); err != nil {
		return fmt.Errorf("workspace chat sqlite: switch materialized selections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE native_submissions SET conversation_kind = 'thread', conversation_ref = ?, thread_id = ?,
		updated_at_ms = ? WHERE conversation_kind = 'draft' AND conversation_ref = ?`, threadID, threadID, now, draftID); err != nil {
		return fmt.Errorf("workspace chat sqlite: switch materialized submissions: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_submissions SET thread_id = ?, native_turn_id = ?, input_json = NULL,
		status = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN status ELSE 'accepted' END,
		error_message = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN error_message ELSE '' END,
		updated_at_ms = ? WHERE request_id = ?`, threadID, nativeTurnID, now, requestID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: accept materialized submission: %w", err)
	}
	if err := rowsAffectedExactlyOne(result, "accept materialized submission"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workspace chat sqlite: commit draft materialization: %w", err)
	}
	return nil
}

func (r *Repository) BeginSubmission(ctx context.Context, submission core.WorkspaceChatSubmission) error {
	if strings.TrimSpace(submission.RequestID) == "" || strings.TrimSpace(submission.ClientID) == "" || strings.TrimSpace(submission.WorkspaceRef) == "" || strings.TrimSpace(submission.Kind) == "" || strings.TrimSpace(submission.Status) == "" {
		return fmt.Errorf("workspace chat sqlite: submission request, client, workspace, kind and status are required")
	}
	if err := validateConversation(submission.Conversation); err != nil {
		return fmt.Errorf("workspace chat sqlite: submission: %w", err)
	}
	if submission.Status != "pending" || submission.NativeTurnID != "" || submission.Error != "" {
		return fmt.Errorf("workspace chat sqlite: new submission must be pending and unaccepted")
	}
	switch submission.Kind {
	case "start":
		if submission.ExpectedTurnID != "" {
			return fmt.Errorf("workspace chat sqlite: start submission cannot have an expected turn")
		}
	case "steer":
		if strings.TrimSpace(submission.ExpectedTurnID) == "" {
			return fmt.Errorf("workspace chat sqlite: steer submission requires an expected turn")
		}
	default:
		return fmt.Errorf("workspace chat sqlite: submission kind must be start or steer")
	}
	if submission.Conversation.Kind == core.ConversationKindDraft && submission.ThreadID != "" {
		return fmt.Errorf("workspace chat sqlite: draft submission cannot reference a thread")
	}
	if submission.Conversation.Kind == core.ConversationKindThread && submission.ThreadID != submission.Conversation.ID {
		return fmt.Errorf("workspace chat sqlite: thread submission must reference its conversation thread")
	}
	inputJSON, err := validateJSON(submission.InputJSON, "submission input", "null")
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %w", err)
	}
	createdAt := unixMilliOrNow(submission.CreatedAt)
	updatedAt := submission.UpdatedAt.UnixMilli()
	if submission.UpdatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO native_submissions(request_id, client_id, workspace_ref, conversation_kind,
		conversation_ref, thread_id, native_turn_id, kind, expected_turn_id, input_json, status, error_message,
		created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, submission.RequestID,
		submission.ClientID, submission.WorkspaceRef, submission.Conversation.Kind, submission.Conversation.ID,
		submission.ThreadID, submission.NativeTurnID, submission.Kind, submission.ExpectedTurnID, string(inputJSON),
		submission.Status, submission.Error, createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin submission: %w", err)
	}
	return nil
}

func (r *Repository) MarkSubmissionAccepted(ctx context.Context, requestID, threadID, nativeTurnID string) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(threadID) == "" || strings.TrimSpace(nativeTurnID) == "" {
		return fmt.Errorf("workspace chat sqlite: accepted submission request, thread and native turn are required")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE native_submissions SET thread_id = ?, native_turn_id = ?, input_json = NULL,
		status = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN status ELSE 'accepted' END,
		error_message = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN error_message ELSE '' END,
		updated_at_ms = ? WHERE request_id = ? AND (thread_id = '' OR thread_id = ?) AND (native_turn_id = '' OR native_turn_id = ?)`,
		threadID, nativeTurnID, time.Now().UnixMilli(), requestID, threadID, nativeTurnID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: mark submission accepted: %w", err)
	}
	return rowsAffectedExactlyOne(result, "mark submission accepted")
}

func (r *Repository) FinishSubmission(ctx context.Context, requestID, status, errorMessage string) error {
	if strings.TrimSpace(requestID) == "" || !containsValue([]string{"completed", "failed", "cancelled", "interrupted", "needs_retry"}, status) {
		return fmt.Errorf("workspace chat sqlite: finished submission request and status are required")
	}
	if err := validatePersistentErrorCode(errorMessage); err != nil {
		return fmt.Errorf("workspace chat sqlite: finish submission: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE native_submissions
		SET input_json = NULL,
		status = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN status ELSE ? END,
		error_message = CASE WHEN status IN ('completed', 'failed', 'cancelled', 'interrupted') THEN error_message ELSE ? END,
		updated_at_ms = ? WHERE request_id = ?`,
		status, errorMessage, time.Now().UnixMilli(), requestID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: finish submission: %w", err)
	}
	return rowsAffectedExactlyOne(result, "finish submission")
}

func (r *Repository) ListUnfinishedSubmissions(ctx context.Context) (result []core.WorkspaceChatSubmission, resultErr error) {
	rows, err := r.db.QueryContext(ctx, `SELECT request_id, client_id, workspace_ref, conversation_kind,
		conversation_ref, thread_id, native_turn_id, kind, expected_turn_id, input_json, status,
		error_message, created_at_ms, updated_at_ms FROM native_submissions
		WHERE status NOT IN ('completed', 'failed', 'cancelled', 'interrupted') ORDER BY created_at_ms, request_id`)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: list unfinished submissions: %w", err)
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			result = nil
			resultErr = fmt.Errorf("workspace chat sqlite: close unfinished submissions rows: %w", err)
		}
	}()
	result = make([]core.WorkspaceChatSubmission, 0)
	for rows.Next() {
		submission, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, submission)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: read unfinished submissions: %w", err)
	}
	return result, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSubmission(row rowScanner) (core.WorkspaceChatSubmission, error) {
	var submission core.WorkspaceChatSubmission
	var kind string
	var inputJSON sql.NullString
	var createdAt, updatedAt int64
	if err := row.Scan(&submission.RequestID, &submission.ClientID, &submission.WorkspaceRef, &kind,
		&submission.Conversation.ID, &submission.ThreadID, &submission.NativeTurnID, &submission.Kind,
		&submission.ExpectedTurnID, &inputJSON, &submission.Status, &submission.Error, &createdAt, &updatedAt); err != nil {
		return core.WorkspaceChatSubmission{}, fmt.Errorf("workspace chat sqlite: scan submission: %w", err)
	}
	submission.Conversation.Kind = core.ConversationKind(kind)
	if inputJSON.Valid {
		submission.InputJSON = json.RawMessage(inputJSON.String)
	}
	submission.CreatedAt = time.UnixMilli(createdAt)
	submission.UpdatedAt = time.UnixMilli(updatedAt)
	return submission, nil
}

func (r *Repository) PutInteraction(ctx context.Context, record core.WorkspaceChatInteractionRecord) error {
	interaction := record.Interaction
	if strings.TrimSpace(interaction.ID) == "" || strings.TrimSpace(interaction.Kind) == "" || strings.TrimSpace(interaction.ThreadID) == "" || strings.TrimSpace(record.Status) == "" {
		return fmt.Errorf("workspace chat sqlite: interaction id, kind, thread and status are required")
	}
	if record.Status != "pending" {
		return fmt.Errorf("workspace chat sqlite: new interaction status must be pending")
	}
	if len(interaction.RequestID) == 0 || strings.TrimSpace(string(interaction.RequestID)) == "null" {
		return fmt.Errorf("workspace chat sqlite: interaction request id is required")
	}
	requestID, err := validateJSON(interaction.RequestID, "interaction request id", "null")
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %w", err)
	}
	payload, err := validateJSON(interaction.Payload, "interaction payload", "null")
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %w", err)
	}
	generation := record.ConnectionGeneration
	if generation != 0 && interaction.ConnectionGeneration != 0 && generation != interaction.ConnectionGeneration {
		return fmt.Errorf("workspace chat sqlite: interaction connection generations disagree")
	}
	if generation == 0 {
		generation = interaction.ConnectionGeneration
	}
	if generation == 0 {
		return fmt.Errorf("workspace chat sqlite: interaction connection generation is required")
	}
	if interaction.OccurredAt.IsZero() {
		return fmt.Errorf("workspace chat sqlite: interaction occurrence time is required")
	}
	decisionsJSON, err := json.Marshal(interaction.AllowedDecisions)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode interaction decisions: %w", err)
	}
	occurredAt := interaction.OccurredAt.UnixMilli()
	expiresAt := int64(0)
	if !record.ExpiresAt.IsZero() {
		expiresAt = record.ExpiresAt.UnixMilli()
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO pending_interactions(interaction_id, kind, thread_id, turn_id, item_id,
		request_id_json, connection_generation, allowed_decisions_json, payload_json, status, occurred_at_ms, expires_at_ms, updated_at_ms)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(interaction_id) DO UPDATE SET interaction_id = pending_interactions.interaction_id
		WHERE pending_interactions.kind = excluded.kind
		AND pending_interactions.thread_id = excluded.thread_id
		AND pending_interactions.turn_id = excluded.turn_id
		AND pending_interactions.item_id = excluded.item_id
		AND pending_interactions.request_id_json = excluded.request_id_json
		AND pending_interactions.connection_generation = excluded.connection_generation
		AND pending_interactions.allowed_decisions_json = excluded.allowed_decisions_json
		AND pending_interactions.payload_json = excluded.payload_json
		AND pending_interactions.status = excluded.status
		AND pending_interactions.occurred_at_ms = excluded.occurred_at_ms
		AND pending_interactions.expires_at_ms = excluded.expires_at_ms`, interaction.ID, interaction.Kind, interaction.ThreadID,
		interaction.TurnID, interaction.ItemID, string(requestID), generation, string(decisionsJSON), string(payload), record.Status,
		occurredAt, expiresAt, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put interaction: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put interaction rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("workspace chat sqlite: interaction %q conflicts with existing record", interaction.ID)
	}
	return nil
}

func (r *Repository) ResolveInteraction(ctx context.Context, interactionID, status string) error {
	if strings.TrimSpace(interactionID) == "" || !containsValue([]string{"resolved", "connection_lost", "expired", "failed", "cancelled"}, status) {
		return fmt.Errorf("workspace chat sqlite: resolved interaction id and terminal status are required")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE pending_interactions SET status = ?, updated_at_ms = ?
		WHERE interaction_id = ? AND (status = 'pending' OR status = ?)`,
		status, time.Now().UnixMilli(), interactionID, status)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: resolve interaction: %w", err)
	}
	return rowsAffectedExactlyOne(result, "resolve interaction")
}

func (r *Repository) ExpirePendingInteractions(ctx context.Context, status string) error {
	if !containsValue([]string{"connection_lost", "expired", "failed", "cancelled"}, status) {
		return fmt.Errorf("workspace chat sqlite: expired interaction terminal status is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin pending interaction expiry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE pending_interactions SET status = ?, updated_at_ms = ?
		WHERE status = 'pending'`, status, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("workspace chat sqlite: expire pending interactions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workspace chat sqlite: commit pending interaction expiry: %w", err)
	}
	return nil
}

func (r *Repository) ListPendingInteractions(ctx context.Context, threadID string) (result []core.WorkspaceChatInteractionRecord, resultErr error) {
	rows, err := r.db.QueryContext(ctx, `SELECT interaction_id, kind, thread_id, turn_id, item_id, request_id_json,
		connection_generation, allowed_decisions_json, payload_json, status, occurred_at_ms, expires_at_ms FROM pending_interactions
		WHERE thread_id = ? AND status = 'pending' ORDER BY occurred_at_ms, interaction_id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: list pending interactions: %w", err)
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			result = nil
			resultErr = fmt.Errorf("workspace chat sqlite: close pending interactions rows: %w", err)
		}
	}()
	result = make([]core.WorkspaceChatInteractionRecord, 0)
	for rows.Next() {
		var record core.WorkspaceChatInteractionRecord
		var requestIDJSON, decisionsJSON, payloadJSON string
		var occurredAt, expiresAt int64
		if err := rows.Scan(&record.Interaction.ID, &record.Interaction.Kind, &record.Interaction.ThreadID,
			&record.Interaction.TurnID, &record.Interaction.ItemID, &requestIDJSON, &record.ConnectionGeneration,
			&decisionsJSON, &payloadJSON, &record.Status, &occurredAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("workspace chat sqlite: scan pending interaction: %w", err)
		}
		record.Interaction.RequestID = json.RawMessage(requestIDJSON)
		record.Interaction.Payload = json.RawMessage(payloadJSON)
		if err := json.Unmarshal([]byte(decisionsJSON), &record.Interaction.AllowedDecisions); err != nil {
			return nil, fmt.Errorf("workspace chat sqlite: corrupt interaction decisions for %q: %w", record.Interaction.ID, err)
		}
		record.Interaction.ConnectionGeneration = record.ConnectionGeneration
		record.Interaction.OccurredAt = time.UnixMilli(occurredAt)
		if expiresAt > 0 {
			record.ExpiresAt = time.UnixMilli(expiresAt)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: read pending interactions: %w", err)
	}
	return result, nil
}

func (r *Repository) PutSettingIntent(ctx context.Context, intent core.WorkspaceChatSettingIntent) error {
	if strings.TrimSpace(intent.ID) == "" || strings.TrimSpace(intent.WorkspaceRef) == "" || strings.TrimSpace(intent.ThreadID) == "" || strings.TrimSpace(intent.Status) == "" {
		return fmt.Errorf("workspace chat sqlite: setting intent id, workspace, thread and status are required")
	}
	if intent.Status != "pending" || intent.Error != "" {
		return fmt.Errorf("workspace chat sqlite: new setting intent must be pending")
	}
	patchJSON, err := json.Marshal(intent.Patch)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode setting intent: %w", err)
	}
	createdAt := unixMilliOrNow(intent.CreatedAt)
	updatedAt := intent.UpdatedAt.UnixMilli()
	if intent.UpdatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO thread_setting_intents(intent_id, workspace_ref, thread_id, patch_json,
		status, error_message, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, intent.ID,
		intent.WorkspaceRef, intent.ThreadID, string(patchJSON), intent.Status, intent.Error, createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put setting intent: %w", err)
	}
	return nil
}

func (r *Repository) ResolveSettingIntent(ctx context.Context, intentID, status, errorMessage string) error {
	if strings.TrimSpace(intentID) == "" || !containsValue([]string{"applied", "failed", "needs_retry"}, status) {
		return fmt.Errorf("workspace chat sqlite: resolved setting intent id and terminal status are required")
	}
	if err := validatePersistentErrorCode(errorMessage); err != nil {
		return fmt.Errorf("workspace chat sqlite: resolve setting intent: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE thread_setting_intents SET status = ?, error_message = ?, updated_at_ms = ?
		WHERE intent_id = ? AND (status = 'pending' OR status = ?)`, status, errorMessage, time.Now().UnixMilli(), intentID, status)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: resolve setting intent: %w", err)
	}
	return rowsAffectedExactlyOne(result, "resolve setting intent")
}

func (r *Repository) ListPendingSettingIntents(ctx context.Context) (result []core.WorkspaceChatSettingIntent, resultErr error) {
	rows, err := r.db.QueryContext(ctx, `SELECT intent_id, workspace_ref, thread_id, patch_json, status,
		error_message, created_at_ms, updated_at_ms FROM thread_setting_intents
		WHERE status = 'pending' ORDER BY created_at_ms, intent_id`)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: list pending setting intents: %w", err)
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			result = nil
			resultErr = fmt.Errorf("workspace chat sqlite: close pending setting intents rows: %w", err)
		}
	}()
	result = make([]core.WorkspaceChatSettingIntent, 0)
	for rows.Next() {
		var intent core.WorkspaceChatSettingIntent
		var patchJSON string
		var createdAt, updatedAt int64
		if err := rows.Scan(&intent.ID, &intent.WorkspaceRef, &intent.ThreadID, &patchJSON, &intent.Status,
			&intent.Error, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("workspace chat sqlite: scan pending setting intent: %w", err)
		}
		if err := json.Unmarshal([]byte(patchJSON), &intent.Patch); err != nil {
			return nil, fmt.Errorf("workspace chat sqlite: corrupt setting intent patch for %q: %w", intent.ID, err)
		}
		intent.CreatedAt = time.UnixMilli(createdAt)
		intent.UpdatedAt = time.UnixMilli(updatedAt)
		result = append(result, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: read pending setting intents: %w", err)
	}
	return result, nil
}

func (r *Repository) PutDelivery(ctx context.Context, delivery core.WorkspaceChatDeliveryRecord) error {
	if strings.TrimSpace(delivery.ID) == "" || strings.TrimSpace(delivery.ClientID) == "" ||
		strings.TrimSpace(delivery.WorkspaceRef) == "" || strings.TrimSpace(delivery.Transport) == "" ||
		strings.TrimSpace(delivery.Destination) == "" || strings.TrimSpace(delivery.Status) == "" {
		return fmt.Errorf("workspace chat sqlite: delivery id, client, workspace, transport, destination and status are required")
	}
	if err := validateConversation(delivery.Conversation); err != nil {
		return fmt.Errorf("workspace chat sqlite: delivery: %w", err)
	}
	if delivery.Status != "pending" {
		return fmt.Errorf("workspace chat sqlite: new delivery status must be pending")
	}
	metadata, err := validateDeliveryMetadata(delivery.Metadata)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: %w", err)
	}
	createdAt := unixMilliOrNow(delivery.CreatedAt)
	updatedAt := delivery.UpdatedAt.UnixMilli()
	if delivery.UpdatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO delivery_records(delivery_id, client_id, workspace_ref,
		conversation_kind, conversation_ref, request_id, transport, destination_ref, status, error_message,
		metadata_json, created_at_ms, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		delivery.ID, delivery.ClientID, delivery.WorkspaceRef, delivery.Conversation.Kind, delivery.Conversation.ID,
		delivery.RequestID, delivery.Transport, delivery.Destination, delivery.Status, string(metadata), createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put delivery: %w", err)
	}
	return nil
}

func (r *Repository) FinishDelivery(ctx context.Context, deliveryID, status, errorMessage string) error {
	if strings.TrimSpace(deliveryID) == "" || !containsValue([]string{"delivered", "failed"}, status) {
		return fmt.Errorf("workspace chat sqlite: finished delivery id and terminal status are required")
	}
	if err := validatePersistentErrorCode(errorMessage); err != nil {
		return fmt.Errorf("workspace chat sqlite: finish delivery: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE delivery_records SET status = ?, error_message = ?, updated_at_ms = ?
		WHERE delivery_id = ? AND (status = 'pending' OR status = ?)`, status, errorMessage, time.Now().UnixMilli(), deliveryID, status)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: finish delivery: %w", err)
	}
	return rowsAffectedExactlyOne(result, "finish delivery")
}

func (r *Repository) ListPendingDeliveries(ctx context.Context) (result []core.WorkspaceChatDeliveryRecord, resultErr error) {
	rows, err := r.db.QueryContext(ctx, `SELECT delivery_id, client_id, workspace_ref, conversation_kind,
		conversation_ref, request_id, transport, destination_ref, status, error_message, metadata_json,
		created_at_ms, updated_at_ms FROM delivery_records WHERE status = 'pending' ORDER BY created_at_ms, delivery_id`)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: list pending deliveries: %w", err)
	}
	defer func() {
		if err := rows.Close(); resultErr == nil && err != nil {
			result = nil
			resultErr = fmt.Errorf("workspace chat sqlite: close pending deliveries rows: %w", err)
		}
	}()
	for rows.Next() {
		var delivery core.WorkspaceChatDeliveryRecord
		var kind, metadata string
		var createdAt, updatedAt int64
		if err := rows.Scan(&delivery.ID, &delivery.ClientID, &delivery.WorkspaceRef, &kind,
			&delivery.Conversation.ID, &delivery.RequestID, &delivery.Transport, &delivery.Destination,
			&delivery.Status, &delivery.Error, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("workspace chat sqlite: scan pending delivery: %w", err)
		}
		delivery.Conversation.Kind = core.ConversationKind(kind)
		delivery.Metadata = json.RawMessage(metadata)
		delivery.CreatedAt = time.UnixMilli(createdAt)
		delivery.UpdatedAt = time.UnixMilli(updatedAt)
		result = append(result, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: read pending deliveries: %w", err)
	}
	return result, nil
}

func validatePersistentErrorCode(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 64 {
		return fmt.Errorf("persistent error code is too long")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return fmt.Errorf("persistent error must be a lowercase structured code")
	}
	return nil
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if target == value {
			return true
		}
	}
	return false
}

func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
