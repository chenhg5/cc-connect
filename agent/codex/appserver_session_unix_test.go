//go:build unix

package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAppServerSession_CloseKillsProcessGroup(t *testing.T) {
	if os.Getenv("CC_CONNECT_CODEX_HELPER") == "1" {
		runFakeCodexAppServer(t)
		return
	}

	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	childPIDFile := filepath.Join(workDir, "child.pid")
	script := `#!/bin/sh
exec "$CC_CONNECT_CODEX_HELPER_BIN" -test.run TestAppServerSession_CloseKillsProcessGroup --
`
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CHILD_PID_FILE", childPIDFile)
	t.Setenv("CC_CONNECT_CODEX_HELPER", "1")
	t.Setenv("CC_CONNECT_CODEX_HELPER_BIN", os.Args[0])
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s, err := newAppServerSession(context.Background(), "stdio://", workDir, "", "", "", "", "", "", nil, "")
	if err != nil {
		t.Fatalf("newAppServerSession: %v", err)
	}

	childPID := waitForPIDFile(t, childPIDFile)
	t.Cleanup(func() {
		if processRunning(childPID) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	parentPID := appServerProcessPID(t, s)
	assertSameProcessGroup(t, parentPID, childPID)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(childPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d still alive after app-server Close", childPID)
}

func runFakeCodexAppServer(t *testing.T) {
	t.Helper()

	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start child sleep: %v", err)
	}
	if err := os.WriteFile(os.Getenv("CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)+"\n"), 0o644); err != nil {
		_ = child.Process.Kill()
		t.Fatalf("write child pid file: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, `"method":"initialize"`):
			fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"test"}}`)
		case strings.Contains(line, `"method":"thread/start"`):
			fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"cwd":"/tmp","model":"test-model","reasoningEffort":"xhigh","thread":{"id":"thread-close"}}}`)
		case strings.Contains(line, `"method":"account/rateLimits/read"`):
			fmt.Println(`{"jsonrpc":"2.0","id":3,"result":{"rateLimits":{}}}`)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	_, _ = child.Process.Wait()
}

func appServerProcessPID(t *testing.T, s *appServerSession) int {
	t.Helper()

	s.procMu.Lock()
	defer s.procMu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		t.Fatal("app-server process is not available")
	}
	return s.cmd.Process.Pid
}

func assertSameProcessGroup(t *testing.T, parentPID, childPID int) {
	t.Helper()

	parentPGID, err := syscall.Getpgid(parentPID)
	if err != nil {
		t.Fatalf("Getpgid(parent %d): %v", parentPID, err)
	}
	childPGID, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("Getpgid(child %d): %v", childPID, err)
	}
	if childPGID != parentPGID {
		t.Fatalf("test child process group = %d, app-server process group = %d; fixture child escaped the process group", childPGID, parentPGID)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse pid file: %v", parseErr)
			}
			return pid
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s: %v", path, lastErr)
	return 0
}

func processRunning(pid int) bool {
	// Linux, including WSL2, can leave a killed child visible as a zombie until
	// its parent is reaped. A zombie is no longer running, so it should not fail
	// a process-group kill test.
	if state, ok := linuxProcessState(pid); ok && state == 'Z' {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func linuxProcessState(pid int) (byte, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	idx := bytes.LastIndex(data, []byte(") "))
	if idx < 0 || idx+2 >= len(data) {
		return 0, false
	}
	return data[idx+2], true
}
