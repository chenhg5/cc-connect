//go:build windows

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervisor_BackendStartsWithoutConsoleWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(DefaultDataDir(), 0700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "console.txt")
	t.Setenv("CC_CONSOLE_PROBE", probe)
	source := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(source, []byte(`package main
import("os";"syscall";"strconv";"time")
func main(){w,_,_:=syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call();os.WriteFile(os.Getenv("CC_CONSOLE_PROBE"),[]byte(strconv.FormatUint(uint64(w),10)),0600);time.Sleep(500*time.Millisecond)}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "backend.exe")
	if out, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	script := buildWindowsTaskScript(Config{BinaryPath: binary, WorkDir: dir, LogFile: filepath.Join(dir, "backend.log")})
	scriptPath := filepath.Join(dir, "supervisor.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("supervisor: %v\n%s", err, out)
	}
	data, err := os.ReadFile(probe)
	if err != nil || string(data) != "0" {
		t.Fatalf("backend console window = %q, %v; want no window", data, err)
	}
	if _, err := os.Stat(windowsProcessStatePath()); !os.IsNotExist(err) {
		t.Fatalf("state file was not removed after backend exit: %v", err)
	}
}

func TestSupervisorLog_CoexistsWithOpenBackendLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "backend.log")
	backend, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	script := buildWindowsTaskScript(Config{LogFile: logPath})
	prefix, _, ok := strings.Cut(script, "$process = $null")
	if !ok {
		t.Fatal("supervisor entry not found")
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", prefix+"\nWrite-SupervisorLog 'sharing probe'")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("supervisor cannot log alongside backend: %v\n%s", err, output)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), "windows supervisor: sharing probe") {
		t.Fatalf("supervisor log missing: %q, %v", data, err)
	}
}

func TestStrictPowerShellStopsOnCmdletErrors(t *testing.T) {
	script := strictPowerShell("Write-Output 'ok'")
	if !strings.HasPrefix(script, "$ErrorActionPreference = 'Stop'\n") {
		t.Fatalf("strictPowerShell() missing stop prelude:\n%s", script)
	}
	if !strings.Contains(script, "Write-Output 'ok'") {
		t.Fatalf("strictPowerShell() missing original script:\n%s", script)
	}
}

func TestBuildWindowsTaskScript(t *testing.T) {
	cfg := Config{
		BinaryPath: `C:\Program Files\cc-connect\cc-connect.exe`,
		WorkDir:    `C:\Users\me\.cc-connect`,
		LogFile:    `C:\Users\me\.cc-connect\logs\cc-connect.log`,
		LogMaxSize: 10 * 1024 * 1024,
		EnvPATH:    `C:\Program Files\nodejs;C:\Users\me\AppData\Local\Programs`,
		EnvExtra: map[string]string{
			"HTTPS_PROXY": "http://127.0.0.1:7890",
			"http_proxy":  "http://127.0.0.1:7890",
		},
	}

	script := buildWindowsTaskScript(cfg)
	for _, want := range []string{
		`$env:CC_LOG_FILE = 'C:\Users\me\.cc-connect\logs\cc-connect.log'`,
		`$env:CC_LOG_MAX_SIZE = '10485760'`,
		`$env:PATH = 'C:\Program Files\nodejs;C:\Users\me\AppData\Local\Programs'`,
		`$env:HTTPS_PROXY = 'http://127.0.0.1:7890'`,
		`$env:http_proxy = 'http://127.0.0.1:7890'`,
		`Set-Location -LiteralPath 'C:\Users\me\.cc-connect'`,
		`while ($true) {`,
		`$binary = 'C:\Program Files\cc-connect\cc-connect.exe'`,
		`$start.CreateNoWindow = $true`,
		`$start.UseShellExecute = $false`,
		`[System.Diagnostics.Process]::Start($start)`,
		`Write-SupervisorLog $_.Exception.Message`,
		`if ($exitCode -eq 0) { exit 0 }`,
		`Start-Sleep -Seconds 10`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

func TestWindowsTaskActionRunsHidden(t *testing.T) {
	got := windowsTaskAction(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`, `C:\Program Files\cc-connect\cc-connect-plugin.exe`)
	for _, want := range []string{
		`"C:\Program Files\cc-connect\cc-connect-plugin.exe"`,
		`--supervisor "C:\Users\me\.cc-connect\cc-connect-daemon.ps1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowsTaskAction() missing %q: %q", want, got)
		}
	}
}

func TestWindowsTaskCreateUsesLimitedInteractivePrincipal(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })

	var script string
	runPowerShell = func(s string) (string, error) {
		script = s
		return "", nil
	}

	if err := createWindowsTask(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`, `C:\cc-connect-plugin.exe`, false); err != nil {
		t.Fatalf("createWindowsTask() error = %v", err)
	}
	for _, want := range []string{
		`New-ScheduledTaskAction`,
		`Register-ScheduledTask`,
		`-LogonType Interactive`,
		`-RunLevel Limited`,
		`-Execute 'C:\cc-connect-plugin.exe'`,
		`--supervisor`,
		`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("create script missing %q:\n%s", want, script)
		}
	}
}

func TestWindowsTaskMatchesActionRequiresExactAction(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })

	var script string
	runPowerShell = func(s string) (string, error) {
		script = s
		return "true", nil
	}

	if !windowsTaskMatchesAction(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`, `C:\cc-connect-plugin.exe`) {
		t.Fatal("windowsTaskMatchesAction() = false, want true")
	}
	for _, want := range []string{
		`$expectedArgs = '--supervisor "C:\Users\me\.cc-connect\cc-connect-daemon.ps1"'`,
		`$action.Execute -ieq 'C:\cc-connect-plugin.exe'`,
		`$action.Arguments -eq $expectedArgs`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("reuse check script missing %q:\n%s", want, script)
		}
	}
}

func TestPowerShellLiteralEscapesSingleQuotes(t *testing.T) {
	got := powerShellLiteral(`C:\Users\O'Brien\.cc-connect`)
	want := `'C:\Users\O''Brien\.cc-connect'`
	if got != want {
		t.Fatalf("powerShellLiteral() = %q, want %q", got, want)
	}
}

func TestBuildWindowsTaskScript_DropsInvalidEnvName(t *testing.T) {
	cfg := Config{
		BinaryPath: "x", WorkDir: "y", LogFile: "l", LogMaxSize: 1, EnvPATH: "p",
		EnvExtra: map[string]string{"FOO BAR": "v", "OK": "ok"},
	}
	script := buildWindowsTaskScript(cfg)
	if strings.Contains(script, "FOO BAR") {
		t.Errorf("invalid env name leaked: %s", script)
	}
	if !strings.Contains(script, "$env:OK = 'ok'") {
		t.Errorf("valid env missing: %s", script)
	}
}

func TestBuildWindowsTaskScript_DropsEmptyValue(t *testing.T) {
	cfg := Config{
		BinaryPath: "x", WorkDir: "y", LogFile: "l", LogMaxSize: 1, EnvPATH: "p",
		EnvExtra: map[string]string{"EMPTY": "", "OK": "ok"},
	}
	script := buildWindowsTaskScript(cfg)
	if strings.Contains(script, "$env:EMPTY") {
		t.Errorf("empty value should be skipped: %s", script)
	}
}

// Windows reports writable files as 0666 regardless of the requested Unix mode.
// Check the reinstall's actual result rather than asserting unsupported POSIX bits.
func TestSchtasksInstall_ReplacesExistingLauncher(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	runPowerShell = func(script string) (string, error) { return "", nil }

	if err := os.MkdirAll(DefaultDataDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	scriptPath := windowsTaskScriptPath()
	if err := os.WriteFile(scriptPath, []byte("$env:OLD = 'leftover'\r\n"), 0o644); err != nil {
		t.Fatalf("seed legacy script: %v", err)
	}

	mgr := &schtasksManager{}
	binaryDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binaryDir, "cc-connect-plugin.exe"), []byte("launcher"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		BinaryPath: filepath.Join(binaryDir, "cc-connect.exe"),
		WorkDir:    t.TempDir(),
		LogFile:    "C:\\cc.log",
		LogMaxSize: 1024,
		EnvPATH:    "C:\\bin",
		EnvExtra:   map[string]string{"CUSTOM_TOKEN": "captured"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if string(data) != buildWindowsTaskScript(cfg) {
		t.Errorf("reinstall did not replace the old launcher")
	}
}

func TestWindowsTaskCreate_KeepsSupervisorRunning(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	var script string
	runPowerShell = func(s string) (string, error) { script = s; return "", nil }
	if err := createWindowsTask(`C:\cc-connect-daemon.ps1`, `C:\cc-connect-plugin.exe`, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-ExecutionTimeLimit ([TimeSpan]::Zero)", "-MultipleInstances IgnoreNew", "-AllowStartIfOnBatteries", "-DontStopIfGoingOnBatteries", "-RestartCount 3", "-Settings $settings"} {
		if !strings.Contains(script, want) {
			t.Errorf("task settings missing %q", want)
		}
	}
}

func TestWindowsTaskCreate_CodexPluginHasNoLoginTrigger(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	var script string
	runPowerShell = func(s string) (string, error) { script = s; return "", nil }
	for _, withCodex := range []bool{false, true} {
		if err := createWindowsTask(`C:\cc-connect-daemon.ps1`, `C:\cc-connect-plugin.exe`, withCodex); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(script, "-AtLogOn"); got == withCodex {
			t.Errorf("withCodex=%v: login trigger present=%v", withCodex, got)
		}
		if got := strings.Contains(script, "-Trigger $trigger"); got == withCodex {
			t.Errorf("withCodex=%v: registered login trigger=%v", withCodex, got)
		}
	}
}

func TestStopWindowsTask_KillsManagedChildTree(t *testing.T) {
	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	var script string
	runPowerShell = func(s string) (string, error) { script = s; return "", nil }
	if err := stopWindowsTask(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Stop-ScheduledTask", "taskkill.exe", "/T /F", "$child.StartTime", "$child.Path"} {
		if !strings.Contains(script, want) {
			t.Errorf("stop script missing %q", want)
		}
	}
}
