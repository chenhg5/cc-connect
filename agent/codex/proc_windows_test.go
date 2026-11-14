//go:build windows

package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startShimTree mirrors how an npm-installed codex runs on Windows:
// cmd.exe (running a .cmd shim) -> long-lived grandchild. It returns the
// started command and the grandchild PID.
func startShimTree(t *testing.T, ctx context.Context) (*exec.Cmd, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	shim := filepath.Join(dir, "fakecodex.cmd")
	script := "@powershell -NoProfile -NonInteractive -Command \"$PID | Out-File -Encoding ascii '" + pidFile + "'; Start-Sleep 60\"\r\n"
	if err := os.WriteFile(shim, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, shim)
	prepareCmdForKill(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	attachCmdToKillJob(cmd)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(pidFile); len(strings.TrimSpace(string(b))) > 0 {
			return cmd, strings.TrimSpace(string(b))
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = forceKillCmd(cmd)
	t.Fatal("grandchild did not start")
	return nil, ""
}

func pidAlive(pid string) bool {
	out, _ := exec.Command("tasklist", "/FI", "PID eq "+pid, "/NH").CombinedOutput()
	return strings.Contains(string(out), " "+pid+" ")
}

func assertPIDExits(t *testing.T, pid, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = exec.Command("taskkill", "/F", "/PID", pid).Run()
	t.Fatalf("grandchild %s survived %s", pid, what)
}

func TestPrepareCmdForKill_LeavesPlainCommandStartable(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	prepareCmdForKill(cmd)
	if cmd.Cancel != nil {
		t.Fatal("Cancel must stay nil for commands not created with CommandContext")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// Regression: codexSession.Close cancels the context before force-killing.
// The default cancel killed only cmd.exe, after which taskkill /T could no
// longer find node.exe/codex.exe, leaving them holding the thread lock.
func TestContextCancel_KillsWholeTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, grandchild := startShimTree(t, ctx)

	cancel()
	_ = cmd.Wait()
	_ = forceKillCmd(cmd) // same follow-up as codexSession.Close

	assertPIDExits(t, grandchild, "context cancel")
}

func TestKillJob_KillsTreeWhenParentDies(t *testing.T) {
	if out := os.Getenv("CODEX_KILLJOB_HELPER_OUT"); out != "" {
		_, grandchild := startShimTree(t, context.Background())
		if err := os.WriteFile(out, []byte(grandchild), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute) // killed by the parent test
		return
	}

	out := filepath.Join(t.TempDir(), "grandchild.pid")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(self, "-test.run", "^TestKillJob_KillsTreeWhenParentDies$")
	helper.Env = append(os.Environ(), "CODEX_KILLJOB_HELPER_OUT="+out)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}

	var grandchild string
	deadline := time.Now().Add(20 * time.Second)
	for grandchild == "" && time.Now().Before(deadline) {
		b, _ := os.ReadFile(out)
		grandchild = strings.TrimSpace(string(b))
		time.Sleep(100 * time.Millisecond)
	}
	if grandchild == "" {
		_ = helper.Process.Kill()
		t.Fatal("helper did not report grandchild PID")
	}

	// Simulate cc-connect crashing: no Close, no taskkill.
	_ = helper.Process.Kill()
	_ = helper.Wait()

	assertPIDExits(t, grandchild, "parent exit")
}
