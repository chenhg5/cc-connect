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
	w, err := NewRotatingWriter(logPath, maxSize)
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
		LogFile:     "/tmp/test.log",
		LogMaxSize:  1024,
		WorkDir:     "/tmp",
		BinaryPath:  "/usr/local/bin/cc-connect",
		InstalledAt: NowISO(),
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
