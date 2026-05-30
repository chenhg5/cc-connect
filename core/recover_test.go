package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSafeGo_RecoversPanic(t *testing.T) {
	e := &Engine{
		name:    "test-project",
		dataDir: t.TempDir(),
	}

	panicked := make(chan struct{})
	e.safeGo(func() {
		panic("test panic")
	})

	// The goroutine should recover and not crash the process.
	select {
	case <-panicked:
		t.Fatal("should not reach here")
	default:
	}

	// Give the goroutine time to write the crash log.
	crashLog := filepath.Join(e.dataDir, "crashes", "crash.log")
	for i := 0; i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		if data, err := os.ReadFile(crashLog); err == nil {
			content := string(data)
			if !strings.Contains(content, "test panic") {
				t.Fatalf("crash log should contain panic value, got:\n%s", content)
			}
			if !strings.Contains(content, "test-project") {
				t.Fatalf("crash log should contain project name, got:\n%s", content)
			}
			return
		}
	}
	t.Fatal("crash log was not written")
}

func TestSafeGo_NormalExecution(t *testing.T) {
	e := &Engine{
		name:    "test-project",
		dataDir: t.TempDir(),
	}

	done := make(chan struct{})
	e.safeGo(func() {
		close(done)
	})

	select {
	case <-done:
		// Normal execution completed.
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo should complete normally")
	}

	// No crash log should be written.
	crashLog := filepath.Join(e.dataDir, "crashes", "crash.log")
	if _, err := os.Stat(crashLog); !os.IsNotExist(err) {
		t.Fatal("crash log should not exist for normal execution")
	}
}
