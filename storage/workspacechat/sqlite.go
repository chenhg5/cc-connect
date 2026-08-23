package workspacechat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 1

type Repository struct{ db *sql.DB }

func Open(dataDir string) (*Repository, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, fmt.Errorf("workspace chat sqlite: data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "workspace_chat.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	repo := &Repository{db: db}
	if err := repo.configureAndMigrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := repo.RecoverInterruptedTurns(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

func (r *Repository) configureAndMigrate(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := r.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("workspace chat sqlite: %s: %w", pragma, err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("workspace chat sqlite: create migration table: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("workspace chat sqlite: read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("workspace chat sqlite: schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	if version < 1 {
		if err := migrateV1(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at_ms) VALUES(1, ?)", time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("workspace chat sqlite: record migration 1: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("workspace chat sqlite: commit migration: %w", err)
	}
	return nil
}

func migrateV1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE client_selections (
			client_id TEXT PRIMARY KEY,
			workspace_ref TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE menu_snapshots (
			client_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			revision TEXT NOT NULL,
			item_ids_json TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			PRIMARY KEY(client_id, kind)
		)`,
		`CREATE TABLE turn_records (
			request_id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			workspace_ref TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued','in_progress','completed','failed','cancelled','interrupted')),
			error_message TEXT NOT NULL DEFAULT '',
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX turn_records_thread_updated_idx ON turn_records(thread_id, updated_at_ms DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("workspace chat sqlite: migration 1: %w", err)
		}
	}
	return nil
}

func (r *Repository) GetSelection(ctx context.Context, clientID string) (*core.WorkspaceChatSelection, error) {
	var selection core.WorkspaceChatSelection
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT client_id, workspace_ref, thread_id, updated_at_ms
		FROM client_selections WHERE client_id = ?`, clientID).Scan(
		&selection.ClientID, &selection.WorkspaceRef, &selection.ThreadID, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: get selection: %w", err)
	}
	selection.UpdatedAt = time.UnixMilli(updatedAt)
	return &selection, nil
}

func (r *Repository) PutSelection(ctx context.Context, selection core.WorkspaceChatSelection) error {
	if strings.TrimSpace(selection.ClientID) == "" || strings.TrimSpace(selection.WorkspaceRef) == "" || strings.TrimSpace(selection.ThreadID) == "" {
		return fmt.Errorf("workspace chat sqlite: selection client, workspace and thread are required")
	}
	if selection.UpdatedAt.IsZero() {
		selection.UpdatedAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO client_selections(client_id, workspace_ref, thread_id, updated_at_ms)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(client_id) DO UPDATE SET workspace_ref=excluded.workspace_ref,
		thread_id=excluded.thread_id, updated_at_ms=excluded.updated_at_ms`,
		selection.ClientID, selection.WorkspaceRef, selection.ThreadID, selection.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put selection: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSelection(ctx context.Context, clientID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM client_selections WHERE client_id = ?", clientID); err != nil {
		return fmt.Errorf("workspace chat sqlite: delete selection: %w", err)
	}
	return nil
}

func (r *Repository) GetMenu(ctx context.Context, clientID, kind string) (*core.WorkspaceChatMenuSnapshot, error) {
	var snapshot core.WorkspaceChatMenuSnapshot
	var itemIDsJSON string
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT client_id, kind, revision, item_ids_json, updated_at_ms
		FROM menu_snapshots WHERE client_id = ? AND kind = ?`, clientID, kind).Scan(
		&snapshot.ClientID, &snapshot.Kind, &snapshot.Revision, &itemIDsJSON, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: get menu: %w", err)
	}
	if err := json.Unmarshal([]byte(itemIDsJSON), &snapshot.ItemIDs); err != nil {
		return nil, fmt.Errorf("workspace chat sqlite: corrupt menu snapshot for %q/%q: %w", clientID, kind, err)
	}
	for i, itemID := range snapshot.ItemIDs {
		if strings.TrimSpace(itemID) == "" {
			return nil, fmt.Errorf("workspace chat sqlite: corrupt menu snapshot for %q/%q: empty item at %d", clientID, kind, i)
		}
	}
	snapshot.UpdatedAt = time.UnixMilli(updatedAt)
	return &snapshot, nil
}

func (r *Repository) PutMenu(ctx context.Context, snapshot core.WorkspaceChatMenuSnapshot) error {
	if strings.TrimSpace(snapshot.ClientID) == "" || strings.TrimSpace(snapshot.Kind) == "" {
		return fmt.Errorf("workspace chat sqlite: menu client and kind are required")
	}
	itemsJSON, err := json.Marshal(snapshot.ItemIDs)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: encode menu: %w", err)
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now()
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO menu_snapshots(client_id, kind, revision, item_ids_json, updated_at_ms)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(client_id, kind) DO UPDATE SET revision=excluded.revision,
		item_ids_json=excluded.item_ids_json, updated_at_ms=excluded.updated_at_ms`,
		snapshot.ClientID, snapshot.Kind, snapshot.Revision, string(itemsJSON), snapshot.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: put menu: %w", err)
	}
	return nil
}

func (r *Repository) BeginTurn(ctx context.Context, record core.WorkspaceChatTurnRecord) error {
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO turn_records(
		request_id, client_id, workspace_ref, thread_id, status, error_message, created_at_ms, updated_at_ms
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, record.RequestID, record.ClientID, record.WorkspaceRef,
		record.ThreadID, record.Status, record.Error, record.CreatedAt.UnixMilli(), record.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: begin turn: %w", err)
	}
	return nil
}

func (r *Repository) FinishTurn(ctx context.Context, requestID, status, errorMessage string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE turn_records SET status = ?, error_message = ?, updated_at_ms = ?
		WHERE request_id = ?`, status, errorMessage, time.Now().UnixMilli(), requestID)
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: finish turn: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace chat sqlite: finish turn rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("workspace chat sqlite: turn %q not found", requestID)
	}
	return nil
}

func (r *Repository) RecoverInterruptedTurns(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE turn_records
		SET status = 'interrupted', error_message = 'cc-connect restarted before the turn completed', updated_at_ms = ?
		WHERE status IN ('queued', 'in_progress')`, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("workspace chat sqlite: recover interrupted turns: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("workspace chat sqlite: recover interrupted rows: %w", err)
	}
	return rows, nil
}

func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
