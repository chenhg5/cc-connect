package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunClaudeUsageProbe_StderrReadIsRaceFree covers #2111: the probe's
// stderr goes into a buffer that os/exec fills from its own copy goroutine
// (creack/pty only takes over the std streams that are still nil, so the
// stderr buffer survives the PTY setup). The ticker and the timeout branch
// read that buffer while the probe is still running, which raced with the
// copy goroutine's writes — a DATA RACE under `go test -race`.
//
// The fake claude below keeps writing to stderr and never renders the
// /usage panel, so the ticker keeps reading the buffer while the copy
// goroutine keeps writing to it.
//
// Before the fix this test reports a DATA RACE; after it, it is clean.
func TestRunClaudeUsageProbe_StderrReadIsRaceFree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake claude binary")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf 'unknown command: /usage\\n' >&2\n" +
		"while : ; do\n" +
		"  printf 'claude-usage-stderr-filler-01234567890123456789\\n' >&2\n" +
		"  sleep 0.05\n" +
		"done\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := (&Agent{}).runClaudeUsageProbe(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a probe that never rendered /usage")
		}
		if !strings.Contains(err.Error(), "does not support /usage") {
			t.Fatalf("expected the probe to report the agent's stderr, got: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runClaudeUsageProbe did not return")
	}
}
