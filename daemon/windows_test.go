//go:build windows

package daemon

import (
	"os"
	"strings"
	"testing"
)

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
		`Option Explicit`,
		`Set shell = CreateObject("WScript.Shell")`,
		`Set processEnv = shell.Environment("Process")`,
		`processEnv("CC_LOG_FILE") = "C:\Users\me\.cc-connect\logs\cc-connect.log"`,
		`processEnv("CC_LOG_MAX_SIZE") = "10485760"`,
		`processEnv("PATH") = "C:\Program Files\nodejs;C:\Users\me\AppData\Local\Programs"`,
		`processEnv("HTTPS_PROXY") = "http://127.0.0.1:7890"`,
		`processEnv("http_proxy") = "http://127.0.0.1:7890"`,
		`shell.CurrentDirectory = "C:\Users\me\.cc-connect"`,
		`exitCode = shell.Run(Chr(34) & "C:\Program Files\cc-connect\cc-connect.exe" & Chr(34), 0, True)`,
		`If exitCode = 0 Then WScript.Quit 0`,
		`WScript.Sleep 10000`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

func TestWindowsTaskActionUsesWindowlessScriptHost(t *testing.T) {
	got := windowsTaskAction(`C:\Users\me\.cc-connect\cc-connect-daemon.vbs`)
	for _, want := range []string{
		`wscript.exe`,
		`//B`,
		`//NoLogo`,
		`"C:\Users\me\.cc-connect\cc-connect-daemon.vbs"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowsTaskAction() missing %q: %q", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "powershell") {
		t.Fatalf("windowsTaskAction() still launches a console host: %q", got)
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

	if err := createWindowsTask(`C:\Users\me\.cc-connect\cc-connect-daemon.vbs`); err != nil {
		t.Fatalf("createWindowsTask() error = %v", err)
	}
	for _, want := range []string{
		`New-ScheduledTaskAction`,
		`Register-ScheduledTask`,
		`-Execute 'wscript.exe'`,
		`-LogonType Interactive`,
		`-RunLevel Limited`,
		`//B //NoLogo`,
		`C:\Users\me\.cc-connect\cc-connect-daemon.vbs`,
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

	if !windowsTaskMatchesAction(`C:\Users\me\.cc-connect\cc-connect-daemon.vbs`) {
		t.Fatal("windowsTaskMatchesAction() = false, want true")
	}
	for _, want := range []string{
		`$expectedArgs = '//B //NoLogo "C:\Users\me\.cc-connect\cc-connect-daemon.vbs"'`,
		`$action.Execute -ieq 'wscript.exe'`,
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

func TestVBScriptLiteralEscapesDoubleQuotes(t *testing.T) {
	got := vbScriptLiteral(`C:\Users\Jane "quoted"`)
	want := `"C:\Users\Jane ""quoted"""`
	if got != want {
		t.Fatalf("vbScriptLiteral() = %q, want %q", got, want)
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
	if !strings.Contains(script, `processEnv("OK") = "ok"`) {
		t.Errorf("valid env missing: %s", script)
	}
}

func TestBuildWindowsTaskScript_DropsEmptyValue(t *testing.T) {
	cfg := Config{
		BinaryPath: "x", WorkDir: "y", LogFile: "l", LogMaxSize: 1, EnvPATH: "p",
		EnvExtra: map[string]string{"EMPTY": "", "OK": "ok"},
	}
	script := buildWindowsTaskScript(cfg)
	if strings.Contains(script, `processEnv("EMPTY")`) {
		t.Errorf("empty value should be skipped: %s", script)
	}
}

func TestSchtasksInstallRemovesLegacyPowerShellLauncher(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	runPowerShell = func(script string) (string, error) { return "", nil }

	if err := os.MkdirAll(DefaultDataDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacyPath := legacyWindowsTaskScriptPath()
	if err := os.WriteFile(legacyPath, []byte("legacy\r\n"), 0o600); err != nil {
		t.Fatalf("seed legacy launcher: %v", err)
	}

	mgr := &schtasksManager{}
	cfg := Config{BinaryPath: "C:\\cc.exe", WorkDir: t.TempDir(), LogFile: "C:\\cc.log", LogMaxSize: 1024, EnvPATH: "C:\\bin"}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy launcher still exists or cannot be inspected: %v", err)
	}
}

func TestStopWindowsTaskKillsLauncherChildrenAndLockedInstance(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	workDir := t.TempDir()
	if err := os.WriteFile(workDir+`\.config.toml.lock`, []byte("4242\n"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if err := SaveMeta(&Meta{WorkDir: workDir, BinaryPath: `C:\Tools\cc-connect.exe`}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	var script string
	runPowerShell = func(s string) (string, error) {
		script = s
		return "", nil
	}

	if err := stopWindowsTask(); err != nil {
		t.Fatalf("stopWindowsTask: %v", err)
	}
	for _, want := range []string{
		`cc-connect-daemon.vbs`,
		`cc-connect-daemon.ps1`,
		`Get-CimInstance Win32_Process`,
		`$launcherPids -contains $_.ParentProcessId`,
		`$lockedProcessId = 4242`,
		`$expectedBinary = 'C:\Tools\cc-connect.exe'`,
		`Stop-Process -Id $lockedProcessId -Force`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("stop script missing %q:\n%s", want, script)
		}
	}
}

// TestSchtasksInstall_RewritesExistingScript covers the upgrade path: a user
// from an earlier cc-connect version may already have a task script on disk,
// and Install must rewrite it in place instead of leaving the stale one.
//
// The permission half of that check cannot be asserted here. This file is
// windows-only, and Windows ignores permission bits beyond the read-only
// attribute, so the 0644 script seeded below reads back as 0666 and Install's
// 0600 is unobservable. That hardening is covered where modes are real, by
// TestInstallLaunchd_TightensExistingPlistFrom0644 and
// TestSystemdInstall_TightensExistingUnitFrom0644.
func TestSchtasksInstall_RewritesExistingScript(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	orig := runPowerShell
	t.Cleanup(func() { runPowerShell = orig })
	runPowerShell = func(script string) (string, error) { return "", nil }

	if err := os.MkdirAll(DefaultDataDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	scriptPath := windowsTaskScriptPath()
	const legacyMarker = "$env:OLD = 'leftover'"
	if err := os.WriteFile(scriptPath, []byte(legacyMarker+"\r\n"), 0o644); err != nil {
		t.Fatalf("seed legacy script: %v", err)
	}

	mgr := &schtasksManager{}
	cfg := Config{
		BinaryPath: "C:\\cc.exe",
		WorkDir:    t.TempDir(),
		LogFile:    "C:\\cc.log",
		LogMaxSize: 1024,
		EnvPATH:    "C:\\bin",
		EnvExtra:   map[string]string{"CUSTOM_TOKEN": "captured"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}

	written, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script after Install: %v", err)
	}
	if strings.Contains(string(written), legacyMarker) {
		t.Errorf("Install left the legacy script in place:\n%s", written)
	}
	if !strings.Contains(string(written), "captured") {
		t.Errorf("reinstalled script is missing the captured env value:\n%s", written)
	}
}
