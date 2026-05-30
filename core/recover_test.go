package core

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafeGo_RecoversPanic(t *testing.T) {
	tmpDir := t.TempDir()
	e := &Engine{
		name:    "test",
		dataDir: tmpDir,
	}

	e.safeGo(func() {
		panic("test panic")
	})

	time.Sleep(100 * time.Millisecond)

	crashLog := filepath.Join(tmpDir, "crashes", "crash.log")
	data, err := os.ReadFile(crashLog)
	if err != nil {
		t.Fatalf("crash log not created: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("crash log is empty")
	}
}

func TestSafeGo_NormalExecution(t *testing.T) {
	tmpDir := t.TempDir()
	e := &Engine{
		name:    "test",
		dataDir: tmpDir,
	}

	var executed atomic.Bool
	e.safeGo(func() {
		executed.Store(true)
	})

	time.Sleep(50 * time.Millisecond)
	if !executed.Load() {
		t.Error("safeGo function was not executed")
	}

	crashLog := filepath.Join(tmpDir, "crashes", "crash.log")
	if _, err := os.Stat(crashLog); !os.IsNotExist(err) {
		t.Error("crash log should not exist for normal execution")
	}
}
