//go:build windows

package daemon

import (
	"context"
	"encoding/json"
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

func TestMain(m *testing.M) {
	if mode := os.Getenv("CC_CONNECT_TEST_PROCESS"); mode != "" {
		if mode == "parent" {
			child := exec.Command(os.Args[0])
			child.Env = append(os.Environ(), "CC_CONNECT_TEST_PROCESS=child")
			child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if err := child.Start(); err != nil {
				os.Exit(1)
			}
			os.WriteFile(os.Getenv("CC_CONNECT_TEST_CHILD_PID"), []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0600)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func TestBuildWindowsTaskScript_CodexGateIsOptional(t *testing.T) {
	cfg := Config{BinaryPath: "cc.exe", WorkDir: "work", LogFile: "log"}
	if strings.Contains(buildWindowsTaskScript(cfg), "Find-CodexDesktop") {
		t.Fatal("default daemon must start independently of Codex")
	}
	cfg.StartWithCodex = true
	script := buildWindowsTaskScript(cfg)
	for _, want := range []string{"Find-CodexDesktop", "MainWindowHandle -ne 0", "ProductName -eq 'Codex'", "SessionId -eq $sessionId", `OpenAI\Codex\bin\*\codex.exe`, "$process.WaitForExit(1000)"} {
		if !strings.Contains(script, want) {
			t.Errorf("Codex gate missing %q", want)
		}
	}
	if strings.Count(script, "if ($null -eq (Find-CodexDesktop))") != 2 {
		t.Fatal("Codex presence must be checked before startup and while the child runs")
	}
}

func TestWindowsSupervisor_ExitsWithoutCodex(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CC_CONNECT_TEST_PROCESS", "child")
	if err := os.MkdirAll(DefaultDataDir(), 0700); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	cfg := Config{BinaryPath: exe, WorkDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "supervisor.log"), StartWithCodex: true}
	// Hide desktop processes at the OS boundary without closing the user's Codex.
	prefix := `function Get-Process { param($Id, $Name)
    if ($Name) { return }
    Microsoft.PowerShell.Management\Get-Process -Id $Id
}
`
	scriptPath := filepath.Join(t.TempDir(), "supervisor.ps1")
	os.WriteFile(scriptPath, []byte(prefix+buildWindowsTaskScript(cfg)), 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		log, _ := os.ReadFile(cfg.LogFile)
		t.Fatalf("supervisor must exit without waiting for Windows/Codex startup: %v (context: %v) %s %s", err, ctx.Err(), out, log)
	}
	if _, err := os.Stat(windowsProcessStatePath()); !os.IsNotExist(err) {
		t.Fatal("started a child without Codex")
	}
}

func TestWindowsSupervisor_CodexCloseStopsManagedTree(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CC_CONNECT_TEST_PROCESS", "parent")
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("CC_CONNECT_TEST_CHILD_PID", childPIDFile)
	if err := os.MkdirAll(DefaultDataDir(), 0700); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	cfg := Config{BinaryPath: exe, WorkDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "supervisor.log"), StartWithCodex: true}
	presence := filepath.Join(t.TempDir(), "codex-open")
	if err := os.WriteFile(presence, []byte("open"), 0600); err != nil {
		t.Fatal(err)
	}
	// Replace only desktop discovery; exercise the real launcher and process tree.
	override := fmt.Sprintf("function Find-CodexDesktop { if (Test-Path -LiteralPath %s) { return 'open' } }\r\n", powerShellLiteral(presence))
	script := strings.Replace(buildWindowsTaskScript(cfg), "$basePath = $env:PATH", override+"$basePath = $env:PATH", 1)
	scriptPath := filepath.Join(t.TempDir(), "supervisor.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		runPowerShell(windowsManagedChildScript() + `if ($null -ne $child) { & taskkill.exe /PID $child.Id /T /F | Out-Null }`)
	})
	var parentPID, childPID int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(childPIDFile)
		if n, _ := fmt.Sscanf(string(data), "%d %d", &parentPID, &childPID); n == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("supervisor did not start the bot process tree")
	}
	if err := os.Remove(presence); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("closing Codex should stop cleanly: %v", err)
	}
	out, err := runPowerShell(fmt.Sprintf(`Get-Process -Id %d,%d -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id; exit 0`, parentPID, childPID))
	if err != nil || out != "" {
		t.Fatalf("bot processes survived Codex close: %q %v", out, err)
	}
	if _, err := os.Stat(windowsProcessStatePath()); !os.IsNotExist(err) {
		t.Fatal("stale process state after Codex close")
	}
}

func TestWindowsSupervisor_RestartsAndStopsManagedTree(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CC_CONNECT_TEST_PROCESS", "parent")
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("CC_CONNECT_TEST_CHILD_PID", childPIDFile)
	if err := os.MkdirAll(DefaultDataDir(), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{BinaryPath: exe, WorkDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "supervisor.log")}
	scriptPath := filepath.Join(t.TempDir(), "supervisor.ps1")
	if err := os.WriteFile(scriptPath, []byte(buildWindowsTaskScript(cfg)), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	orig := runPowerShell
	var managedPIDs []int
	t.Cleanup(func() {
		runPowerShell = orig
		cmd.Process.Kill()
		cmd.Wait()
		orig(windowsManagedChildScript() + `if ($null -ne $child) { & taskkill.exe /PID $child.Id /T /F | Out-Null }`)
		for _, pid := range managedPIDs {
			orig(fmt.Sprintf(`$p = Get-Process -Id %d -ErrorAction SilentlyContinue; if ($p -and $p.Path -ieq %s) { & taskkill.exe /PID $p.Id /T /F | Out-Null }`, pid, powerShellLiteral(exe)))
		}
	})
	readPID := func() int {
		data, _ := os.ReadFile(windowsProcessStatePath())
		var state struct {
			PID int `json:"pid"`
		}
		json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\ufeff")), &state)
		return state.PID
	}
	waitPID := func(previous int) int {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if pid := readPID(); pid > 0 && pid != previous {
				return pid
			}
			time.Sleep(100 * time.Millisecond)
		}
		log, _ := os.ReadFile(cfg.LogFile)
		t.Fatalf("supervisor did not start a replacement; log: %s", log)
		return 0
	}
	first := waitPID(0)
	managedPIDs = append(managedPIDs, first)
	if _, err := orig(windowsManagedChildScript() + `& taskkill.exe /PID $child.Id /T /F | Out-Null`); err != nil {
		t.Fatal(err)
	}
	second := waitPID(first)
	managedPIDs = append(managedPIDs, second)
	// A stale PID must not select another process, even if the image is identical.
	stateData, _ := os.ReadFile(windowsProcessStatePath())
	var state map[string]any
	json.Unmarshal([]byte(strings.TrimPrefix(string(stateData), "\ufeff")), &state)
	state["start_ticks"] = 0
	stale, _ := json.Marshal(state)
	os.WriteFile(windowsProcessStatePath(), stale, 0600)
	out, err := orig(windowsManagedChildScript() + `Write-Output ($null -eq $child)`)
	if err != nil || out != "True" {
		t.Fatalf("stale PID selected: %q, %v", out, err)
	}
	os.WriteFile(windowsProcessStatePath(), stateData, 0600)
	// Simulate Task Scheduler terminating the launcher. The child must survive it,
	// then Stop must clean up that exact child and its descendants.
	cmd.Process.Kill()
	cmd.Wait()
	runPowerShell = func(script string) (string, error) {
		return orig("function Get-ScheduledTask { [pscustomobject]@{State='Ready'} }\n" + script)
	}
	if err := stopWindowsTask(); err != nil {
		t.Fatal(err)
	}
	var childPID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(childPIDFile)
		fields := strings.Fields(string(data))
		if len(fields) == 2 && fields[0] == strconv.Itoa(second) {
			childPID = fields[1]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range []string{strconv.Itoa(second), childPID} {
		if pid == "" {
			t.Fatal("helper descendant was not created")
		}
		out, err := orig(fmt.Sprintf("Write-Output ($null -eq (Get-Process -Id %s -ErrorAction SilentlyContinue))", pid))
		if err != nil || out != "True" {
			t.Errorf("managed PID %s survived stop: %q, %v", pid, out, err)
		}
	}
}
