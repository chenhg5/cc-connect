package controlstore

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreSetupSessionAndPairingAreOneTime(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := Open(path, "setup-once")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.SetupAdministrator(ctx, "setup-once", "ccshus", "long-enough-password"); err != nil {
		t.Fatalf("SetupAdministrator() error = %v", err)
	}
	if err := store.SetupAdministrator(ctx, "setup-once", "ccshus", "another-long-password"); err == nil {
		t.Fatal("second SetupAdministrator() unexpectedly succeeded")
	}
	if err := store.AuthenticateAdministrator(ctx, "ccshus", "long-enough-password"); err != nil {
		t.Fatalf("AuthenticateAdministrator() error = %v", err)
	}
	session, token, err := store.CreateSession(ctx)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := store.ValidateSession(ctx, token, session.CSRFToken, true); err != nil {
		t.Fatalf("ValidateSession() error = %v", err)
	}
	if _, err := store.ValidateSession(ctx, token, "wrong", true); err == nil {
		t.Fatal("ValidateSession() accepted invalid CSRF token")
	}

	code, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatalf("CreatePairingCode() error = %v", err)
	}
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	device, err := store.PairDevice(ctx, code.Code, "Mac Studio", publicKey)
	if err != nil {
		t.Fatalf("PairDevice() error = %v", err)
	}
	if device.ID == "" || device.Name != "Mac Studio" {
		t.Fatalf("PairDevice() = %#v", device)
	}
	if _, err := store.PairDevice(ctx, code.Code, "Other", publicKey); err == nil {
		t.Fatal("PairDevice() reused one-time code")
	}
}

func TestOpenInitializedAdministratorWithoutSetupToken(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := Open(path, "setup-once")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.SetupAdministrator(ctx, "setup-once", "ccshus", "long-enough-password"); err != nil {
		_ = store.Close()
		t.Fatalf("SetupAdministrator() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, "")
	if err != nil {
		t.Fatalf("Open(initialized) error = %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.AuthenticateAdministrator(ctx, "ccshus", "long-enough-password"); err != nil {
		t.Fatalf("AuthenticateAdministrator() error = %v", err)
	}
}

func TestOpenInitializedAdministratorRemovesConsumedSetupToken(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := Open(path, "setup-once")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.SetupAdministrator(ctx, "setup-once", "ccshus", "long-enough-password"); err != nil {
		_ = store.Close()
		t.Fatalf("SetupAdministrator() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO control_meta(key, value) VALUES('setup_token_hash', randomblob(32))"); err != nil {
		_ = store.Close()
		t.Fatalf("insert consumed setup token: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, "")
	if err != nil {
		t.Fatalf("Open(initialized) error = %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var count int
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM control_meta WHERE key = 'setup_token_hash'").Scan(&count); err != nil {
		t.Fatalf("count setup token: %v", err)
	}
	if count != 0 {
		t.Fatalf("consumed setup token count = %d, want 0", count)
	}
}

func TestChangeAdministratorPasswordInvalidatesAllSessions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SetupAdministrator(ctx, "setup", "ccshus", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	first, firstToken, err := store.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, secondToken, err := store.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeAdministratorPassword(ctx, "long-enough-password", "new-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateSession(ctx, firstToken, first.CSRFToken, true); err == nil {
		t.Fatal("first session remained valid")
	}
	if _, err := store.ValidateSession(ctx, secondToken, second.CSRFToken, true); err == nil {
		t.Fatal("second session remained valid")
	}
	if err := store.AuthenticateAdministrator(ctx, "ccshus", "long-enough-password"); err == nil {
		t.Fatal("old password remained valid")
	}
	if err := store.AuthenticateAdministrator(ctx, "ccshus", "new-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	profile, err := store.AdministratorProfile(ctx)
	if err != nil || profile.Username != "ccshus" {
		t.Fatalf("profile = %#v err=%v", profile, err)
	}
}

func TestRuntimeCheckpointAdvancesMonotonically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	code, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, _ := ed25519.GenerateKey(nil)
	device, err := store.PairDevice(ctx, code.Code, "Mac", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range [][2]uint64{{3, 7}, {3, 6}, {2, 99}, {4, 1}} {
		if err := store.SaveRuntimeCheckpoint(ctx, device.ID, checkpoint[0], checkpoint[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.RuntimeCheckpoint(ctx, device.ID)
	if err != nil || got == nil || got.ConnectionGeneration != 4 || got.ConfirmedSequence != 1 {
		t.Fatalf("RuntimeCheckpoint() = %#v, %v", got, err)
	}
}

func TestStoreRejectsSchemaMismatchWithoutReadingOldAdministrator(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := Open(path, "setup")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.SetupAdministrator(ctx, "setup", "ccshus", "long-enough-password"); err != nil {
		t.Fatalf("SetupAdministrator() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 4"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path, "ignored-token"); err == nil || !strings.Contains(err.Error(), "does not match required version") {
		t.Fatalf("Open(existing) error = %v", err)
	}
}

func TestStoreRejectsCorruptDatabaseWithoutDeletingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, "setup")
	if err == nil || !strings.Contains(err.Error(), "integrity") && !strings.Contains(err.Error(), "database") {
		t.Fatalf("Open(corrupt) error = %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "not sqlite" {
		t.Fatalf("corrupt database was modified: raw=%q error=%v", raw, readErr)
	}
}

func TestStoreBackupIsConsistentAndPreservesAdministrator(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(filepath.Join(directory, "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetupAdministrator(ctx, "setup", "ccshus", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(directory, "backups", "control.db")
	if err := store.Backup(ctx, backup); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup, "ignored")
	if err != nil {
		t.Fatalf("Open(backup) error = %v", err)
	}
	defer func() { _ = restored.Close() }()
	if err := restored.AuthenticateAdministrator(ctx, "ccshus", "long-enough-password"); err != nil {
		t.Fatalf("backup lost administrator: %v", err)
	}
}

func TestRecoverInterruptedRunsPreservesPendingActivationRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	run, err := store.AcquireExecutionSlot(ctx, "update", "v0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverInterruptedRuns(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.DeployRun(ctx, run.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("preserved run = %#v, %v", got, err)
	}
	if _, err := store.AcquireExecutionSlot(ctx, "restart", "", ""); err == nil {
		t.Fatal("execution slot was released while pending activation was preserved")
	}
}

func TestStoreExpiresSessionAndPairingCode(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	clock := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return clock }
	if err := store.SetupAdministrator(ctx, "setup", "ccshus", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	session, token, err := store.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(defaultSessionTTL + time.Second)
	if _, err := store.ValidateSession(ctx, token, session.CSRFToken, true); err == nil {
		t.Fatal("expired session accepted")
	}
	publicKey, _, _ := ed25519.GenerateKey(nil)
	if _, err := store.PairDevice(ctx, code.Code, "Mac", publicKey); err == nil {
		t.Fatal("expired pairing code accepted")
	}
	if _, err := store.Device(ctx, "missing"); err != sql.ErrNoRows {
		t.Fatalf("Device(missing) error = %v", err)
	}
}

func TestStoreListsPersistentAuditEventsByDeviceCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, action := range []string{"device_paired", "runtime_connected", "runtime_disconnected"} {
		if err := store.RecordAudit(ctx, "runtime:device-1", action, "device:device-1", "succeeded", nil); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.AuditEvents(ctx, "device:device-1", 0, 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("AuditEvents() = %#v, err=%v", events, err)
	}
	next, err := store.AuditEvents(ctx, "device:device-1", events[1].ID, 10)
	if err != nil || len(next) != 1 || next[0].Action != "runtime_disconnected" {
		t.Fatalf("cursor AuditEvents() = %#v, err=%v", next, err)
	}
}
