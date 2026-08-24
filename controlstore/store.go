package controlstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 5

const (
	defaultSessionTTL = 24 * time.Hour
	pairingTTL        = 10 * time.Minute
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Session struct {
	ID        string    `json:"id"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	PublicKey  []byte     `json:"-"`
	PairedAt   time.Time  `json:"paired_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type PairingCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type DeployRun struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	TargetTag string     `json:"target_tag,omitempty"`
	CommitSHA string     `json:"commit_sha,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type RuntimeCheckpoint struct {
	DeviceID             string    `json:"device_id"`
	ConnectionGeneration uint64    `json:"connection_generation"`
	ConfirmedSequence    uint64    `json:"confirmed_sequence"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type ReleaseSlot struct {
	Tag         string    `json:"tag"`
	CommitSHA   string    `json:"commit_sha"`
	Directory   string    `json:"directory"`
	Manifest    []byte    `json:"-"`
	Status      string    `json:"status"`
	ActivatedAt time.Time `json:"activated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type RuntimeUpdate struct {
	DeviceID  string    `json:"device_id"`
	TargetTag string    `json:"target_tag"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AuditEvent struct {
	ID         int64           `json:"id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource"`
	Outcome    string          `json:"outcome"`
	Details    json.RawMessage `json:"details"`
}

type AdministratorProfile struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func Open(path, setupToken string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("control store: database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("control store: create data directory: %w", err)
	}
	dsn := &url.URL{Scheme: "file", Path: path}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("control store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.initialize(context.Background(), setupToken); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, setupToken string) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("control store: enable foreign keys: %w", err)
	}
	if err := s.integrityCheck(ctx); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("control store: read schema version: %w", err)
	}
	if version != 0 && version != SchemaVersion {
		return fmt.Errorf("control store: schema version %d does not match required version %d", version, SchemaVersion)
	}
	if version == 0 {
		if err := s.createSchema(ctx); err != nil {
			return err
		}
	}
	var initialized int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM control_meta WHERE key = 'setup_token_hash'").Scan(&initialized); err != nil {
		return fmt.Errorf("control store: inspect setup token: %w", err)
	}
	if initialized == 0 {
		if strings.TrimSpace(setupToken) == "" {
			return errors.New("control store: first start requires a one-time setup token")
		}
		if _, err := s.db.ExecContext(ctx, "INSERT INTO control_meta(key, value) VALUES('setup_token_hash', ?)", digest(setupToken)); err != nil {
			return fmt.Errorf("control store: save setup token: %w", err)
		}
	}
	var workspaceKeyCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM control_meta WHERE key = 'workspace_ref_key'").Scan(&workspaceKeyCount); err != nil {
		return fmt.Errorf("control store: inspect workspace reference key: %w", err)
	}
	if workspaceKeyCount == 0 {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return fmt.Errorf("control store: generate workspace reference key: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "INSERT INTO control_meta(key, value) VALUES('workspace_ref_key', ?)", key); err != nil {
			return fmt.Errorf("control store: save workspace reference key: %w", err)
		}
	}
	return nil
}

func (s *Store) createSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control store: begin schema creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`CREATE TABLE control_meta (key TEXT PRIMARY KEY, value BLOB NOT NULL)`,
		`CREATE TABLE administrators (id INTEGER PRIMARY KEY CHECK(id = 1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, token_hash BLOB NOT NULL UNIQUE, csrf_hash BLOB NOT NULL, expires_at_ms INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE devices (id TEXT PRIMARY KEY, name TEXT NOT NULL, public_key BLOB NOT NULL UNIQUE, paired_at_ms INTEGER NOT NULL, last_seen_at_ms INTEGER, revoked_at_ms INTEGER)`,
		`CREATE TABLE pairing_codes (code_hash BLOB PRIMARY KEY, expires_at_ms INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, consumed_at_ms INTEGER)`,
		`CREATE TABLE deploy_runs (id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL, target_tag TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '', started_at_ms INTEGER NOT NULL, ended_at_ms INTEGER, error TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE deploy_run_logs (run_id TEXT NOT NULL REFERENCES deploy_runs(id) ON DELETE CASCADE, sequence INTEGER NOT NULL, occurred_at_ms INTEGER NOT NULL, stream TEXT NOT NULL, line TEXT NOT NULL, PRIMARY KEY(run_id, sequence))`,
		`CREATE TABLE execution_slot (id INTEGER PRIMARY KEY CHECK(id = 1), run_id TEXT NOT NULL REFERENCES deploy_runs(id), acquired_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, occurred_at_ms INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, resource TEXT NOT NULL, outcome TEXT NOT NULL, details_json BLOB NOT NULL DEFAULT '{}')`,
		`CREATE TABLE runtime_checkpoints (device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE, connection_generation INTEGER NOT NULL, confirmed_sequence INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE runtime_catalog (device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, local_ref TEXT NOT NULL, global_ref TEXT NOT NULL UNIQUE, payload_json BLOB NOT NULL, available INTEGER NOT NULL, reason TEXT NOT NULL DEFAULT '', updated_at_ms INTEGER NOT NULL, PRIMARY KEY(device_id, local_ref))`,
		`CREATE TABLE control_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE release_slots (tag TEXT PRIMARY KEY, commit_sha TEXT NOT NULL, directory TEXT NOT NULL UNIQUE, manifest_json BLOB NOT NULL, status TEXT NOT NULL, activated_at_ms INTEGER NOT NULL, created_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE runtime_updates (device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE, target_tag TEXT NOT NULL, status TEXT NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("control store: create schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("control store: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("control store: commit schema creation: %w", err)
	}
	return nil
}

func (s *Store) integrityCheck(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("control store: integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("control store: read integrity result: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			return fmt.Errorf("control store: integrity check failed: %s", result)
		}
	}
	return rows.Err()
}

func (s *Store) PutSetting(ctx context.Context, key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("control store: setting key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO control_settings(key, value, updated_at_ms) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at_ms = excluded.updated_at_ms`, key, value, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("control store: save setting %s: %w", key, err)
	}
	return nil
}

func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM control_settings WHERE key = ?", strings.TrimSpace(key)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("control store: read setting %s: %w", key, err)
	}
	return value, true, nil
}

func (s *Store) RecordAudit(ctx context.Context, actor, action, resource, outcome string, details []byte) error {
	if len(details) == 0 {
		details = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(occurred_at_ms, actor, action, resource, outcome, details_json)
		VALUES(?, ?, ?, ?, ?, ?)`, s.now().UnixMilli(), actor, action, resource, outcome, details)
	if err != nil {
		return fmt.Errorf("control store: record audit event: %w", err)
	}
	return nil
}

func (s *Store) AuditEvents(ctx context.Context, resource string, after int64, limit int) ([]AuditEvent, error) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return nil, errors.New("control store: audit resource is required")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("control store: audit event limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, occurred_at_ms, actor, action, resource, outcome, details_json
		FROM audit_events WHERE resource = ? AND id > ? ORDER BY id LIMIT ?`, resource, after, limit)
	if err != nil {
		return nil, fmt.Errorf("control store: list audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var occurredAt int64
		if err := rows.Scan(&event.ID, &occurredAt, &event.Actor, &event.Action, &event.Resource, &event.Outcome, &event.Details); err != nil {
			return nil, fmt.Errorf("control store: scan audit event: %w", err)
		}
		event.OccurredAt = time.UnixMilli(occurredAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) SetupRequired(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM administrators").Scan(&count); err != nil {
		return false, fmt.Errorf("control store: inspect administrator: %w", err)
	}
	return count == 0, nil
}

func validateUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 || strings.IndexFunc(username, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) >= 0 {
		return "", errors.New("control store: administrator username must be 3-64 characters without whitespace")
	}
	return username, nil
}

func validatePassword(password string) error {
	if len(password) < 12 {
		return errors.New("control store: administrator password must contain at least 12 characters")
	}
	return nil
}

func (s *Store) SetupAdministrator(ctx context.Context, setupToken, username, password string) error {
	username, err := validateUsername(username)
	if err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control store: begin administrator setup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var saved []byte
	if err := tx.QueryRowContext(ctx, "SELECT value FROM control_meta WHERE key = 'setup_token_hash'").Scan(&saved); err != nil {
		return fmt.Errorf("control store: setup token unavailable: %w", err)
	}
	presented := digest(setupToken)
	if subtle.ConstantTimeCompare(saved, presented) != 1 {
		return errors.New("control store: invalid one-time setup token")
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "INSERT INTO administrators(id, username, password_hash, created_at_ms, updated_at_ms) VALUES(1, ?, ?, ?, ?)", username, passwordHash, now, now); err != nil {
		return fmt.Errorf("control store: create administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM control_meta WHERE key = 'setup_token_hash'"); err != nil {
		return fmt.Errorf("control store: consume setup token: %w", err)
	}
	return tx.Commit()
}

func (s *Store) AuthenticateAdministrator(ctx context.Context, username, password string) error {
	username, err := validateUsername(username)
	if err != nil {
		return err
	}
	var encoded string
	if err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM administrators WHERE id = 1 AND username = ?", username).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("control store: administrator setup is incomplete")
		}
		return fmt.Errorf("control store: read administrator: %w", err)
	}
	ok, err := verifyPassword(password, encoded)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("control store: invalid administrator credentials")
	}
	return nil
}

func (s *Store) AdministratorProfile(ctx context.Context) (AdministratorProfile, error) {
	var profile AdministratorProfile
	var created, updated int64
	if err := s.db.QueryRowContext(ctx, "SELECT username, created_at_ms, updated_at_ms FROM administrators WHERE id = 1").Scan(&profile.Username, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdministratorProfile{}, errors.New("control store: administrator setup is incomplete")
		}
		return AdministratorProfile{}, fmt.Errorf("control store: read administrator profile: %w", err)
	}
	profile.CreatedAt = time.UnixMilli(created)
	profile.UpdatedAt = time.UnixMilli(updated)
	return profile, nil
}

func (s *Store) ChangeAdministratorPassword(ctx context.Context, currentPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control store: begin password change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var encoded string
	if err := tx.QueryRowContext(ctx, "SELECT password_hash FROM administrators WHERE id = 1").Scan(&encoded); err != nil {
		return fmt.Errorf("control store: read administrator: %w", err)
	}
	ok, err := verifyPassword(currentPassword, encoded)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("control store: current administrator password is invalid")
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE administrators SET password_hash = ?, updated_at_ms = ? WHERE id = 1", hash, s.now().UnixMilli()); err != nil {
		return fmt.Errorf("control store: update administrator password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return fmt.Errorf("control store: invalidate sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("control store: commit password change: %w", err)
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context) (Session, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return Session{}, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return Session{}, "", err
	}
	id, err := randomToken(18)
	if err != nil {
		return Session{}, "", err
	}
	now := s.now()
	expires := now.Add(defaultSessionTTL)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(id, token_hash, csrf_hash, expires_at_ms, created_at_ms, last_seen_at_ms) VALUES(?, ?, ?, ?, ?, ?)`, id, digest(token), digest(csrf), expires.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Session{}, "", fmt.Errorf("control store: create session: %w", err)
	}
	return Session{ID: id, CSRFToken: csrf, ExpiresAt: expires}, token, nil
}

func (s *Store) ValidateSession(ctx context.Context, token, csrf string, requireCSRF bool) (Session, error) {
	var session Session
	var csrfHash []byte
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT id, csrf_hash, expires_at_ms FROM sessions WHERE token_hash = ?", digest(token)).Scan(&session.ID, &csrfHash, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errors.New("control store: invalid session")
	}
	if err != nil {
		return Session{}, fmt.Errorf("control store: read session: %w", err)
	}
	session.ExpiresAt = time.UnixMilli(expires)
	if !session.ExpiresAt.After(s.now()) {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", session.ID)
		return Session{}, errors.New("control store: session expired")
	}
	if requireCSRF && subtle.ConstantTimeCompare(csrfHash, digest(csrf)) != 1 {
		return Session{}, errors.New("control store: invalid csrf token")
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at_ms = ? WHERE id = ?", s.now().UnixMilli(), session.ID)
	session.CSRFToken = csrf
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", digest(token))
	return err
}

func (s *Store) CreatePairingCode(ctx context.Context) (PairingCode, error) {
	code, err := randomToken(9)
	if err != nil {
		return PairingCode{}, err
	}
	now := s.now()
	expires := now.Add(pairingTTL)
	_, err = s.db.ExecContext(ctx, "INSERT INTO pairing_codes(code_hash, expires_at_ms, created_at_ms) VALUES(?, ?, ?)", digest(code), expires.UnixMilli(), now.UnixMilli())
	if err != nil {
		return PairingCode{}, fmt.Errorf("control store: create pairing code: %w", err)
	}
	return PairingCode{Code: code, ExpiresAt: expires}, nil
}

func (s *Store) PairDevice(ctx context.Context, code, name string, publicKey []byte) (Device, error) {
	if len(publicKey) != 32 {
		return Device{}, errors.New("control store: Ed25519 public key must contain 32 bytes")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Device{}, errors.New("control store: device name is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var expires int64
	var consumed sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT expires_at_ms, consumed_at_ms FROM pairing_codes WHERE code_hash = ?", digest(code)).Scan(&expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) || consumed.Valid || !time.UnixMilli(expires).After(s.now()) {
		return Device{}, errors.New("control store: pairing code is invalid, expired, or already consumed")
	}
	if err != nil {
		return Device{}, fmt.Errorf("control store: read pairing code: %w", err)
	}
	id, err := randomToken(18)
	if err != nil {
		return Device{}, err
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, "INSERT INTO devices(id, name, public_key, paired_at_ms) VALUES(?, ?, ?, ?)", id, name, append([]byte(nil), publicKey...), now.UnixMilli()); err != nil {
		return Device{}, fmt.Errorf("control store: create device: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE pairing_codes SET consumed_at_ms = ? WHERE code_hash = ? AND consumed_at_ms IS NULL", now.UnixMilli(), digest(code)); err != nil {
		return Device{}, fmt.Errorf("control store: consume pairing code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("control store: commit device pairing: %w", err)
	}
	return Device{ID: id, Name: name, PublicKey: append([]byte(nil), publicKey...), PairedAt: now}, nil
}

func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, public_key, paired_at_ms, last_seen_at_ms, revoked_at_ms FROM devices ORDER BY paired_at_ms, id")
	if err != nil {
		return nil, fmt.Errorf("control store: list devices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var devices []Device
	for rows.Next() {
		var device Device
		var paired int64
		var seen, revoked sql.NullInt64
		if err := rows.Scan(&device.ID, &device.Name, &device.PublicKey, &paired, &seen, &revoked); err != nil {
			return nil, fmt.Errorf("control store: scan device: %w", err)
		}
		device.PairedAt = time.UnixMilli(paired)
		if seen.Valid {
			value := time.UnixMilli(seen.Int64)
			device.LastSeenAt = &value
		}
		if revoked.Valid {
			value := time.UnixMilli(revoked.Int64)
			device.RevokedAt = &value
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) Device(ctx context.Context, id string) (Device, error) {
	devices, err := s.ListDevices(ctx)
	if err != nil {
		return Device{}, err
	}
	for _, device := range devices {
		if device.ID == id {
			return device, nil
		}
	}
	return Device{}, sql.ErrNoRows
}

func (s *Store) TouchDevice(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE devices SET last_seen_at_ms = ? WHERE id = ? AND revoked_at_ms IS NULL", s.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("control store: touch device: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("control store: active device not found")
	}
	return nil
}

func (s *Store) RenameDevice(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("control store: device name is required")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE devices SET name = ? WHERE id = ? AND revoked_at_ms IS NULL", name, id)
	if err != nil {
		return fmt.Errorf("control store: rename device: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("control store: active device not found")
	}
	return nil
}

func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE devices SET revoked_at_ms = ? WHERE id = ? AND revoked_at_ms IS NULL", s.now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("control store: revoke device: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("control store: active device not found")
	}
	return nil
}

func (s *Store) RecoverInterruptedRuns(ctx context.Context, preserveRunID ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control store: begin run recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UnixMilli()
	preserved := ""
	if len(preserveRunID) > 0 {
		preserved = strings.TrimSpace(preserveRunID[0])
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deploy_runs SET status = 'interrupted', ended_at_ms = ?, error = 'control restarted before operation completed' WHERE status = 'running' AND id <> ?`, now, preserved); err != nil {
		return fmt.Errorf("control store: recover running deployments: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_slot WHERE run_id <> ?", preserved); err != nil {
		return fmt.Errorf("control store: release stale execution slot: %w", err)
	}
	return tx.Commit()
}

func (s *Store) Backup(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("control store: backup path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("control store: create backup directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control store: replace backup: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("control store: backup database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("control store: protect backup: %w", err)
	}
	return nil
}

func (s *Store) SaveRuntimeCheckpoint(ctx context.Context, deviceID string, generation, sequence uint64) error {
	if strings.TrimSpace(deviceID) == "" || generation == 0 || sequence == 0 {
		return errors.New("control store: valid runtime checkpoint is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runtime_checkpoints(device_id, connection_generation, confirmed_sequence, updated_at_ms)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			connection_generation = excluded.connection_generation,
			confirmed_sequence = excluded.confirmed_sequence,
			updated_at_ms = excluded.updated_at_ms
		WHERE excluded.connection_generation > runtime_checkpoints.connection_generation
		   OR (excluded.connection_generation = runtime_checkpoints.connection_generation
		       AND excluded.confirmed_sequence > runtime_checkpoints.confirmed_sequence)`,
		deviceID, generation, sequence, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("control store: save runtime checkpoint: %w", err)
	}
	return nil
}

func (s *Store) RuntimeCheckpoint(ctx context.Context, deviceID string) (*RuntimeCheckpoint, error) {
	var checkpoint RuntimeCheckpoint
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, connection_generation, confirmed_sequence, updated_at_ms
		FROM runtime_checkpoints WHERE device_id = ?`, deviceID).Scan(
		&checkpoint.DeviceID, &checkpoint.ConnectionGeneration, &checkpoint.ConfirmedSequence, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("control store: read runtime checkpoint: %w", err)
	}
	checkpoint.UpdatedAt = time.UnixMilli(updated)
	return &checkpoint, nil
}

func (s *Store) AcquireExecutionSlot(ctx context.Context, kind, targetTag, commitSHA string) (DeployRun, error) {
	kind = strings.TrimSpace(kind)
	if kind != "update" && kind != "rollback" && kind != "restart" {
		return DeployRun{}, errors.New("control store: unsupported execution kind")
	}
	id, err := randomToken(18)
	if err != nil {
		return DeployRun{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeployRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO deploy_runs(id, kind, status, target_tag, commit_sha, started_at_ms) VALUES(?, ?, 'running', ?, ?, ?)`, id, kind, targetTag, commitSHA, now.UnixMilli()); err != nil {
		return DeployRun{}, fmt.Errorf("control store: create deployment run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO execution_slot(id, run_id, acquired_at_ms) VALUES(1, ?, ?)", id, now.UnixMilli()); err != nil {
		return DeployRun{}, errors.New("control store: another update, rollback, or restart is already running")
	}
	if err := tx.Commit(); err != nil {
		return DeployRun{}, err
	}
	return DeployRun{ID: id, Kind: kind, Status: "running", TargetTag: targetTag, CommitSHA: commitSHA, StartedAt: now}, nil
}

func (s *Store) AppendRunLog(ctx context.Context, runID, stream, line string) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var next uint64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) + 1 FROM deploy_run_logs WHERE run_id = ?", runID).Scan(&next); err != nil {
		return 0, fmt.Errorf("control store: allocate deployment log cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deploy_run_logs(run_id, sequence, occurred_at_ms, stream, line) VALUES(?, ?, ?, ?, ?)`, runID, next, s.now().UnixMilli(), stream, line); err != nil {
		return 0, fmt.Errorf("control store: append deployment log: %w", err)
	}
	return next, tx.Commit()
}

func (s *Store) FinishExecution(ctx context.Context, runID, status, errorMessage string) error {
	if status != "succeeded" && status != "failed" && status != "cancelled" {
		return errors.New("control store: invalid terminal deployment status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE deploy_runs SET status = ?, ended_at_ms = ?, error = ? WHERE id = ? AND status = 'running'`, status, s.now().UnixMilli(), errorMessage, runID)
	if err != nil {
		return fmt.Errorf("control store: finish deployment run: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("control store: running deployment not found")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM execution_slot WHERE run_id = ?", runID); err != nil {
		return fmt.Errorf("control store: release execution slot: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListDeployRuns(ctx context.Context, limit int) ([]DeployRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, status, target_tag, commit_sha, started_at_ms, ended_at_ms, error FROM deploy_runs ORDER BY started_at_ms DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("control store: list deployment runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runs []DeployRun
	for rows.Next() {
		var run DeployRun
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&run.ID, &run.Kind, &run.Status, &run.TargetTag, &run.CommitSHA, &started, &ended, &run.Error); err != nil {
			return nil, err
		}
		run.StartedAt = time.UnixMilli(started)
		if ended.Valid {
			value := time.UnixMilli(ended.Int64)
			run.EndedAt = &value
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) DeployRun(ctx context.Context, id string) (DeployRun, error) {
	var run DeployRun
	var started int64
	var ended sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, status, target_tag, commit_sha, started_at_ms, ended_at_ms, error FROM deploy_runs WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&run.ID, &run.Kind, &run.Status, &run.TargetTag, &run.CommitSHA, &started, &ended, &run.Error,
	)
	if err != nil {
		return DeployRun{}, err
	}
	run.StartedAt = time.UnixMilli(started)
	if ended.Valid {
		value := time.UnixMilli(ended.Int64)
		run.EndedAt = &value
	}
	return run, nil
}

func (s *Store) RunLogs(ctx context.Context, runID string, after uint64) ([]struct {
	Sequence   uint64    `json:"sequence"`
	OccurredAt time.Time `json:"occurred_at"`
	Stream     string    `json:"stream"`
	Line       string    `json:"line"`
}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, occurred_at_ms, stream, line FROM deploy_run_logs WHERE run_id = ? AND sequence > ? ORDER BY sequence`, runID, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []struct {
		Sequence   uint64    `json:"sequence"`
		OccurredAt time.Time `json:"occurred_at"`
		Stream     string    `json:"stream"`
		Line       string    `json:"line"`
	}
	for rows.Next() {
		var entry struct {
			Sequence   uint64    `json:"sequence"`
			OccurredAt time.Time `json:"occurred_at"`
			Stream     string    `json:"stream"`
			Line       string    `json:"line"`
		}
		var occurred int64
		if err := rows.Scan(&entry.Sequence, &occurred, &entry.Stream, &entry.Line); err != nil {
			return nil, err
		}
		entry.OccurredAt = time.UnixMilli(occurred)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) SaveReleaseSlot(ctx context.Context, slot ReleaseSlot) error {
	if strings.TrimSpace(slot.Tag) == "" || strings.TrimSpace(slot.CommitSHA) == "" || strings.TrimSpace(slot.Directory) == "" || len(slot.Manifest) == 0 {
		return errors.New("control store: complete release slot metadata is required")
	}
	if slot.Status != "candidate" && slot.Status != "succeeded" && slot.Status != "failed" {
		return errors.New("control store: invalid release slot status")
	}
	now := s.now()
	activated := slot.ActivatedAt
	if activated.IsZero() {
		activated = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO release_slots(tag, commit_sha, directory, manifest_json, status, activated_at_ms, created_at_ms)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tag) DO UPDATE SET commit_sha = excluded.commit_sha, directory = excluded.directory,
		manifest_json = excluded.manifest_json, status = excluded.status, activated_at_ms = excluded.activated_at_ms`,
		slot.Tag, slot.CommitSHA, slot.Directory, append([]byte(nil), slot.Manifest...), slot.Status, activated.UnixMilli(), now.UnixMilli())
	if err != nil {
		return fmt.Errorf("control store: save release slot: %w", err)
	}
	return nil
}

func (s *Store) ReleaseSlots(ctx context.Context, status string) ([]ReleaseSlot, error) {
	query := `SELECT tag, commit_sha, directory, manifest_json, status, activated_at_ms, created_at_ms FROM release_slots`
	args := []any{}
	if strings.TrimSpace(status) != "" {
		query += " WHERE status = ?"
		args = append(args, strings.TrimSpace(status))
	}
	query += " ORDER BY activated_at_ms DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("control store: list release slots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []ReleaseSlot
	for rows.Next() {
		var slot ReleaseSlot
		var activated, created int64
		if err := rows.Scan(&slot.Tag, &slot.CommitSHA, &slot.Directory, &slot.Manifest, &slot.Status, &activated, &created); err != nil {
			return nil, err
		}
		slot.ActivatedAt = time.UnixMilli(activated)
		slot.CreatedAt = time.UnixMilli(created)
		result = append(result, slot)
	}
	return result, rows.Err()
}

func (s *Store) DeleteReleaseSlot(ctx context.Context, tag string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM release_slots WHERE tag = ?", strings.TrimSpace(tag))
	if err != nil {
		return fmt.Errorf("control store: delete release slot: %w", err)
	}
	return nil
}

func (s *Store) SaveRuntimeUpdate(ctx context.Context, update RuntimeUpdate) error {
	if strings.TrimSpace(update.DeviceID) == "" || strings.TrimSpace(update.TargetTag) == "" || strings.TrimSpace(update.Status) == "" {
		return errors.New("control store: complete runtime update state is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runtime_updates(device_id, target_tag, status, updated_at_ms) VALUES(?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET target_tag = excluded.target_tag, status = excluded.status, updated_at_ms = excluded.updated_at_ms`,
		update.DeviceID, update.TargetTag, update.Status, s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("control store: save runtime update state: %w", err)
	}
	return nil
}

func (s *Store) RuntimeUpdates(ctx context.Context) ([]RuntimeUpdate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, target_tag, status, updated_at_ms FROM runtime_updates ORDER BY device_id`)
	if err != nil {
		return nil, fmt.Errorf("control store: list runtime update states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []RuntimeUpdate
	for rows.Next() {
		var update RuntimeUpdate
		var updated int64
		if err := rows.Scan(&update.DeviceID, &update.TargetTag, &update.Status, &updated); err != nil {
			return nil, err
		}
		update.UpdatedAt = time.UnixMilli(updated)
		result = append(result, update)
	}
	return result, rows.Err()
}

type CatalogEntry struct {
	DeviceID  string
	LocalRef  string
	GlobalRef string
	Payload   []byte
	Available bool
	Reason    string
	UpdatedAt time.Time
}

func (s *Store) WorkspaceReferenceKey(ctx context.Context) ([]byte, error) {
	var key []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM control_meta WHERE key = 'workspace_ref_key'").Scan(&key); err != nil {
		return nil, fmt.Errorf("control store: read workspace reference key: %w", err)
	}
	return append([]byte(nil), key...), nil
}

func (s *Store) ReplaceDeviceCatalog(ctx context.Context, deviceID string, entries []CatalogEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("control store: begin catalog replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_catalog WHERE device_id = ?", deviceID); err != nil {
		return fmt.Errorf("control store: clear device catalog: %w", err)
	}
	now := s.now().UnixMilli()
	for _, entry := range entries {
		if strings.TrimSpace(entry.LocalRef) == "" || strings.TrimSpace(entry.GlobalRef) == "" {
			return errors.New("control store: catalog local and global references are required")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_catalog(device_id, local_ref, global_ref, payload_json, available, reason, updated_at_ms) VALUES(?, ?, ?, ?, ?, ?, ?)`, deviceID, entry.LocalRef, entry.GlobalRef, append([]byte(nil), entry.Payload...), boolInt(entry.Available), entry.Reason, now); err != nil {
			return fmt.Errorf("control store: insert catalog entry: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) ListCatalog(ctx context.Context) ([]CatalogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, local_ref, global_ref, payload_json, available, reason, updated_at_ms FROM runtime_catalog ORDER BY device_id, local_ref`)
	if err != nil {
		return nil, fmt.Errorf("control store: list runtime catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []CatalogEntry
	for rows.Next() {
		var entry CatalogEntry
		var available int
		var updated int64
		if err := rows.Scan(&entry.DeviceID, &entry.LocalRef, &entry.GlobalRef, &entry.Payload, &available, &entry.Reason, &updated); err != nil {
			return nil, fmt.Errorf("control store: scan runtime catalog: %w", err)
		}
		entry.Available = available != 0
		entry.UpdatedAt = time.UnixMilli(updated)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) ResolveWorkspaceReference(ctx context.Context, globalRef string) (CatalogEntry, error) {
	var entry CatalogEntry
	var available int
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, local_ref, global_ref, payload_json, available, reason, updated_at_ms FROM runtime_catalog WHERE global_ref = ?`, globalRef).Scan(&entry.DeviceID, &entry.LocalRef, &entry.GlobalRef, &entry.Payload, &available, &entry.Reason, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CatalogEntry{}, errors.New("control store: unknown workspace reference")
		}
		return CatalogEntry{}, fmt.Errorf("control store: resolve workspace reference: %w", err)
	}
	entry.Available = available != 0
	entry.UpdatedAt = time.UnixMilli(updated)
	return entry, nil
}

func (s *Store) ResolveLocalWorkspaceReference(ctx context.Context, deviceID, localRef string) (CatalogEntry, error) {
	var entry CatalogEntry
	var available int
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, local_ref, global_ref, payload_json, available, reason, updated_at_ms FROM runtime_catalog WHERE device_id = ? AND local_ref = ?`, deviceID, localRef).Scan(&entry.DeviceID, &entry.LocalRef, &entry.GlobalRef, &entry.Payload, &available, &entry.Reason, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CatalogEntry{}, errors.New("control store: unknown local workspace reference")
		}
		return CatalogEntry{}, fmt.Errorf("control store: resolve local workspace reference: %w", err)
	}
	entry.Available = available != 0
	entry.UpdatedAt = time.UnixMilli(updated)
	return entry, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) Close() error { return s.db.Close() }

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func randomToken(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", fmt.Errorf("control store: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("control store: generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("control store: invalid password hash format")
	}
	var version int
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("control store: invalid password hash: %w", err)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("control store: invalid password parameters: %w", err)
	}
	if version != argon2.Version || memory != 64*1024 || iterations != 3 || parallelism != 2 {
		return false, errors.New("control store: unsupported password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("control store: decode password salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("control store: decode password hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
