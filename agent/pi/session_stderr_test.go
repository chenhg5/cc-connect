package pi

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestRPCStderrReadIsRaceFree covers #2111: os/exec copies the child's
// stderr into s.stderrBuf from its own goroutine, and that goroutine is
// still draining the pipe when the RPC read loop reads the buffer back.
// Reading a bytes.Buffer that another goroutine may still be writing to
// is a DATA RACE under `go test -race`.
//
// The fake RPC agent below writes a marker to stderr and then floods the
// pipe with more stderr before closing stdout. Flooding the pipe is what
// makes the race deterministic: the pipe holds ~64KB, so the child blocks
// until os/exec's copy goroutine has drained that much — the marker is
// guaranteed to be in the buffer by the time the read happens — and the
// copy goroutine is still running when the read happens.
//
// Before the fix this test reports a DATA RACE; after it, it is clean.
func TestRPCStderrReadIsRaceFree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake Pi RPC agent")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pi-rpc")
	body := `#!/bin/sh
echo '{"type":"session","id":"sess-stderr-race"}'
printf 'pi-stderr-marker\n' >&2
i=0
while [ $i -lt 2000 ]; do
  printf 'filler-0123456789012345678901234567890123456789\n' >&2
  i=$((i+1))
done
exec 1>&-
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	s := newFakeRPCSession(t, "sess-stderr-race", script, dir)
	defer func() { _ = s.Close() }()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case evt, ok := <-s.events:
			if !ok {
				t.Fatal("event channel closed before the stderr error arrived")
			}
			if evt.Type != core.EventError || evt.Error == nil {
				continue
			}
			if !strings.Contains(evt.Error.Error(), "pi-stderr-marker") {
				t.Fatalf("error should carry the agent's stderr, got: %v", evt.Error)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the stderr error event")
		}
	}
}
