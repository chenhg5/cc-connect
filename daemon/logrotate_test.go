package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingWriter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	maxSize := int64(500) // 500 bytes
	w, err := NewRotatingWriter(logPath, maxSize, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	line := strings.Repeat("A", 100) + "\n" // 101 bytes

	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	// After 10 writes of 101 bytes = 1010 bytes, rotation should have occurred.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("Stat main: %v", err)
	}
	if info.Size() > maxSize+200 {
		t.Errorf("main log too large: %d bytes (max %d)", info.Size(), maxSize)
	}

	backupPath := logPath + ".1"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file should exist: %v", err)
	}

	t.Logf("main: %d bytes, backup exists", info.Size())
}

func TestMetaSaveLoad(t *testing.T) {
	origHome := os.Getenv("HOME")
	dir := t.TempDir()
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	m := &Meta{
		LogFile:       "/tmp/test.log",
		LogMaxSize:    1024,
		LogMaxBackups: 3,
		WorkDir:       "/tmp",
		BinaryPath:    "/usr/local/bin/cc-connect",
		InstalledAt:  NowISO(),
	}

	if err := SaveMeta(m); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	loaded, err := LoadMeta()
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}

	if loaded.LogFile != m.LogFile {
		t.Errorf("LogFile mismatch: %s != %s", loaded.LogFile, m.LogFile)
	}
	if loaded.WorkDir != m.WorkDir {
		t.Errorf("WorkDir mismatch: %s != %s", loaded.WorkDir, m.WorkDir)
	}
}

// TestRotatingWriter_BackupRotation exercises the chain: writing past
// maxSize N times should produce .log.1 .. .log.N, and the oldest
// (N+1th write) should be discarded.
func TestRotatingWriter_BackupRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "chain.log")

	maxSize := int64(200)
	maxBackups := 3
	w, err := NewRotatingWriter(logPath, maxSize, maxBackups)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	chunk := strings.Repeat("x", 250) // each write forces a rotation
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	// After 5 rotations with maxBackups=3, the active log is fresh and
	// .log.1 / .log.2 / .log.3 must exist; .log.4 must NOT exist (it
	// was the oldest and was discarded).
	for i := 1; i <= maxBackups; i++ {
		p := logPath + "." + itoa(i)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("backup %s missing: %v", p, err)
		}
	}
	if _, err := os.Stat(logPath + ".4"); !os.IsNotExist(err) {
		t.Errorf(".log.4 should have been discarded, err=%v", err)
	}
}

// TestRotatingWriter_BackupDisabled pins the legacy single-backup
// behaviour: with maxBackups=1 only .log.1 is ever produced, and the
// active log is always fresh.
func TestRotatingWriter_BackupDisabled(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "single.log")

	maxSize := int64(100)
	w, err := NewRotatingWriter(logPath, maxSize, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	chunk := strings.Repeat("y", 200)
	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Errorf(".log.1 should exist: %v", err)
	}
	if _, err := os.Stat(logPath + ".2"); !os.IsNotExist(err) {
		t.Errorf(".log.2 should NOT exist with maxBackups=1, err=%v", err)
	}
}

// TestRotatingWriter_FallbackForInvalidMaxBackups verifies that passing
// a non-positive maxBackups falls back to DefaultLogMaxBackups (3)
// rather than silently disabling the post-mortem trail. This is the
// safety net for an unparsed env var or a typo'd flag.
func TestRotatingWriter_FallbackForInvalidMaxBackups(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fallback.log")

	w, err := NewRotatingWriter(logPath, 100, 0)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()
	if w.MaxBackups() != DefaultLogMaxBackups {
		t.Fatalf("MaxBackups() = %d, want %d", w.MaxBackups(), DefaultLogMaxBackups)
	}
}

// TestIssue1222_BackupRetention is the regression test pinning the
// follow-up to issue #1222: PR #1243 added CC_LOG_MAX_SIZE but only
// kept a single backup, which still loses any post-mortem context
// older than one rotation. With the new env var (and its flag
// counterpart) we retain the configured number of backups.
func TestIssue1222_BackupRetention(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue1222.log")

	maxSize := int64(150)
	w, err := NewRotatingWriter(logPath, maxSize, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write 6 chunks, each > maxSize so each forces a rotation. With
	// maxBackups=3 the disk should hold active + .1 + .2 + .3 and no
	// more — the 4 oldest chunks get shifted off.
	chunk := []byte(strings.Repeat("z", 200))
	for i := 0; i < 6; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}

	for i := 1; i <= 3; i++ {
		p := logPath + "." + itoa(i)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("backup %s missing: %v", p, err)
		}
		if info.Size() == 0 {
			t.Errorf("backup %s is empty", p)
		}
	}
	if _, err := os.Stat(logPath + ".4"); !os.IsNotExist(err) {
		t.Errorf(".log.4 should have been discarded, err=%v", err)
	}
}

// itoa avoids importing strconv just for these tests; small and local.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
