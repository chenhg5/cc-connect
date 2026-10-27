package acp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestListSessions_StderrReadAfterWait covers #2111: the probe's stderr
// buffer is filled by os/exec's own copy goroutine, and cmd.Wait() is the
// only thing that joins it. Reading the buffer before Wait() returned was an
// unsynchronised read of a bytes.Buffer another goroutine may still be
// writing to — a DATA RACE under `go test -race`.
//
// The fake agent below never answers the initialize request (so the probe
// fails and the error path reads stderr) and keeps writing to stderr, so the
// copy goroutine is still active while the buffer is read.
func TestListSessions_StderrReadAfterWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake ACP agent")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-acp-agent")
	body := "#!/bin/sh\n" +
		"while : ; do\n" +
		"  printf 'fake-acp-stderr-marker\\n' >&2\n" +
		"  sleep 0.02\n" +
		"done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	// The fake never answers initialize, so the probe has to time out.
	// Keep it short: the test only needs the probe to fail while the child
	// is still writing to stderr.
	prevTimeout := listSessionsProbeTimeout
	listSessionsProbeTimeout = 300 * time.Millisecond
	defer func() { listSessionsProbeTimeout = prevTimeout }()

	agent, err := New(map[string]any{"cmd": script, "work_dir": dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessions, err := agent.ListSessions(ctx)
	if err == nil {
		t.Fatalf("expected the probe to fail, got sessions=%v", sessions)
	}
	if !strings.Contains(err.Error(), "fake-acp-stderr-marker") {
		t.Fatalf("error should carry the agent's stderr, got: %v", err)
	}
}
