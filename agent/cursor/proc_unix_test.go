//go:build unix

package cursor

import (
	"context"
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

func TestCursorKillHelper(t *testing.T) {
	switch os.Getenv("CURSOR_KILL_HELPER") {
	case "root":
		runKillHelperRoot()
	case "child":
		runKillHelperChild()
	default:
		return
	}
}

func helperEnv(role string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CURSOR_KILL_HELPER=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "CURSOR_KILL_HELPER="+role)
}

func helperCommand(role string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCursorKillHelper$", "-test.timeout=2m")
	cmd.Env = helperEnv(role)
	return cmd
}

func appendPID(path string, pid int) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(f, "%d\n", pid)
	_ = f.Close()
}

func runKillHelperRoot() {
	pidfile := os.Getenv("CURSOR_KILL_PIDFILE")
	appendPID(pidfile, os.Getpid())
	child := helperCommand("child")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		os.Exit(1)
	}
	_ = child.Wait()
	select {}
}

func runKillHelperChild() {
	appendPID(os.Getenv("CURSOR_KILL_PIDFILE"), os.Getpid())
	sleep := exec.Command("sleep", "120")
	if err := sleep.Start(); err != nil {
		os.Exit(1)
	}
	appendPID(os.Getenv("CURSOR_KILL_PIDFILE"), sleep.Process.Pid)
	_ = sleep.Wait()
	select {}
}

func readPIDs(path string) []int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(b), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 1 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitPIDs(t *testing.T, path string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pids := readPIDs(path)
		if len(pids) >= n {
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pids in %s, got %v", n, path, readPIDs(path))
	return nil
}

func startSeparateGroupTree(t *testing.T) (*exec.Cmd, []int) {
	t.Helper()
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pids")
	cmd := helperCommand("root")
	cmd.Env = append(cmd.Env, "CURSOR_KILL_PIDFILE="+pidfile)
	prepareCmdForKill(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = forceKillCmd(cmd) })
	go func() { _ = cmd.Wait() }()
	pids := waitPIDs(t, pidfile, 3)
	return cmd, pids
}

func TestForceKillCmd_RootGroupLeavesSeparatePGID(t *testing.T) {
	cmd, pids := startSeparateGroupTree(t)
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !processAlive(pids[1]) || !processAlive(pids[2]) {
		t.Fatal("root-group kill unexpectedly reaped the separate-group child or grandchild")
	}
}

func TestForceKillCmd_SeparatePGIDTree(t *testing.T) {
	cmd, pids := startSeparateGroupTree(t)
	start := time.Now()
	if err := forceKillCmd(cmd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if processAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("processes still alive after %s: %v", time.Since(start), pids)
}

func TestRunBoundedTimeoutAndMissingCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runBounded(ctx, "sleep", "30")
	if err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("bounded command waited %s", time.Since(start))
	}
	_, err = runBounded(context.Background(), "/no/such/cursor-ps")
	if err == nil {
		t.Fatal("expected missing command error")
	}
}

func TestExclusiveGroupsSkipsSharedGroup(t *testing.T) {
	procs := map[int]procInfo{
		10: {pgid: 10},
		11: {ppid: 10, pgid: 99},
		12: {ppid: 11, pgid: 99},
		50: {pgid: 99},
	}
	allowed := map[int]struct{}{10: {}, 11: {}, 12: {}}
	groups := exclusiveGroups(procs, allowed)
	if _, ok := groups[99]; ok {
		t.Fatal("shared process group was selected")
	}
	if _, ok := groups[10]; !ok {
		t.Fatal("exclusive root group was not selected")
	}
}

func TestPrepareCmdForKill_SetsPgid(t *testing.T) {
	cmd := exec.Command("true")
	prepareCmdForKill(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("expected Setpgid")
	}
}

func TestPrepareCmdForKill_PreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	prepareCmdForKill(cmd)
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("Setpgid not set on existing SysProcAttr")
	}
}
