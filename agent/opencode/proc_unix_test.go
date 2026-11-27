//go:build unix

package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Reaping a server must take its whole process tree with it: `opencode serve`
// spawns children of its own, and killing only the direct child would leave them
// as orphans. The fake server below forks a long-lived grandchild and the test
// asserts that it is gone once the server is stopped.
func TestStopOpencodeServerKillsTheWholeProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	script := filepath.Join(dir, "opencode")
	body := fmt.Sprintf(`#!/bin/sh
sleep 300 &
echo "$!" > %q
echo "listening on http://127.0.0.1:54321"
while :; do sleep 1; done
`, pidFile)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake server: %v", err)
	}

	srv, err := startOpencodeServer(context.Background(), opencodeServeConfig{
		cmd:     script,
		workDir: dir,
	})
	if err != nil {
		t.Fatalf("startOpencodeServer: %v", err)
	}

	grandchild := waitForPIDFile(t, pidFile)
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d was not running before the stop", grandchild)
	}

	srv.stop()

	if processAlive(srv.cmd.Process.Pid) {
		t.Fatalf("server process %d is still alive after stop", srv.cmd.Process.Pid)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the server stop (orphaned process tree)", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if perr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake server never recorded its grandchild pid: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
