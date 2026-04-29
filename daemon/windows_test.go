//go:build windows

package daemon

import (
	"strings"
	"testing"
)

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
		`& 'C:\Program Files\cc-connect\cc-connect.exe'`,
		`if ($exitCode -eq 0) { exit 0 }`,
		`Start-Sleep -Seconds 10`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

func TestWindowsTaskActionRunsHidden(t *testing.T) {
	got := windowsTaskAction(`C:\Users\me\.cc-connect\cc-connect-daemon.ps1`)
	for _, want := range []string{
		`powershell.exe`,
		`-WindowStyle Hidden`,
		`-NoProfile`,
		`-NonInteractive`,
		`-ExecutionPolicy Bypass`,
		`-File "C:\Users\me\.cc-connect\cc-connect-daemon.ps1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowsTaskAction() missing %q: %q", want, got)
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

func TestParseWindowsTaskStatus(t *testing.T) {
	out := `"HostName","TaskName","Next Run Time","Status"` + "\r\n" +
		`"DESKTOP","\cc-connect","N/A","Running"` + "\r\n"

	if got := parseWindowsTaskStatus(out); got != "Running" {
		t.Fatalf("parseWindowsTaskStatus() = %q, want Running", got)
	}
}

func TestWindowsTaskStatePredicates(t *testing.T) {
	if !windowsTaskAlreadyRunning("ERROR: The task is already running.") {
		t.Fatal("expected already-running schtasks output to be accepted")
	}
	if !windowsTaskAlreadyStopped("ERROR: The scheduled task is not currently running.") {
		t.Fatal("expected already-stopped schtasks output to be accepted")
	}
	if !windowsTaskNotFound("ERROR: The system cannot find the file specified.") {
		t.Fatal("expected not-found schtasks output to be accepted")
	}
}
