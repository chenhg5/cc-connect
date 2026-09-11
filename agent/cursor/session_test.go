package cursor

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestSendSetsStdinPipe verifies that Send() provides a pipe for the child
// process's stdin instead of inheriting the parent's stdin.  Under launchd
// (daemon mode) the parent stdin is /dev/null; newer Cursor agent CLI
// versions hang when they inherit /dev/null as stdin.
//
// The test uses a tiny helper script that checks whether stdin is a pipe
// (not /dev/null or a terminal) and emits a JSON result event so the
// session's readLoop can consume it normally.
func TestSendSetsStdinPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a bash helper script")
	}

	helper := t.TempDir() + "/fake-agent.sh"
	// The helper script uses "file /dev/stdin" which follows the symlink and
	// reports "fifo (named pipe)" for pipes vs "character special" for /dev/null.
	script := `#!/bin/bash
set -e
ftype=$(file /dev/stdin 2>/dev/null)

if echo "$ftype" | grep -qi "fifo\|pipe"; then
  echo '{"type":"result","result":"ok","session_id":"test-stdin-pipe"}'
  exit 0
else
  echo '{"type":"result","result":"stdin is not a pipe: '"$ftype"'","session_id":"test-stdin-pipe"}' >&2
  exit 1
fi
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := newCursorSession(ctx, helper, t.TempDir(), "", "", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case evt := <-cs.Events():
		if evt.SessionID != "test-stdin-pipe" {
			t.Errorf("unexpected session_id %q, want %q", evt.SessionID, "test-stdin-pipe")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event — agent process likely hung (stdin not a pipe?)")
	}
}

// TestSendStdinNotInherited ensures the child process does NOT inherit the
// parent's stdin even when the parent's stdin is /dev/null (simulating
// launchd).  We temporarily redirect our own stdin to /dev/null and verify
// the child still gets a pipe.
func TestSendStdinNotInherited(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a bash helper script")
	}

	// Verify we have bash available.
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	helper := t.TempDir() + "/fake-agent.sh"
	script := `#!/bin/bash
set -e
ftype=$(file /dev/stdin 2>/dev/null)

if echo "$ftype" | grep -qi "fifo\|pipe"; then
  echo '{"type":"result","result":"ok","session_id":"test-no-inherit"}'
  exit 0
else
  echo '{"type":"result","result":"stdin is not a pipe: '"$ftype"'","session_id":"test-no-inherit"}' >&2
  exit 1
fi
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	// Temporarily replace our own stdin with /dev/null to simulate launchd.
	origStdin := os.Stdin
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	os.Stdin = devnull
	defer func() {
		os.Stdin = origStdin
		devnull.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := newCursorSession(ctx, helper, t.TempDir(), "", "", "", nil)
	if err != nil {
		t.Fatalf("newCursorSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case evt := <-cs.Events():
		if evt.SessionID != "test-no-inherit" {
			t.Errorf("unexpected session_id %q, want %q", evt.SessionID, "test-no-inherit")
		}
	case <-ctx.Done():
		t.Fatal("timed out — child likely inherited /dev/null stdin instead of getting a pipe")
	}
}
