package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUTF8ShellProcess(t *testing.T) {
	if os.Getenv("CC_UTF8_SHELL_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(runShell(os.Args[i+1:]))
		}
	}
	os.Exit(1)
}

func TestUTF8Shell_ChineseOutputAndExitCode(t *testing.T) {
	if _, err := findPowerShell(); err != nil {
		t.Skip(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestUTF8ShellProcess$", "--", "-NoProfile", "-Command",
		`[Console]::OutputEncoding.WebName; Write-Output '中文目录'; [Console]::Error.WriteLine('中文错误'); exit 7`)
	cmd.Env = append(os.Environ(), "CC_UTF8_SHELL_TEST=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "utf-8") || !strings.Contains(stdout.String(), "中文目录") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "中文错误") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestUTF8Shell_DetachedProcessHasNoConsoleWindow(t *testing.T) {
	if _, err := findPowerShell(); err != nil {
		t.Skip(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestUTF8ShellProcess$", "--", "-NoProfile", "-Command",
		`Add-Type 'using System; using System.Runtime.InteropServices; public class ConsoleProbe { [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow(); }'; [ConsoleProbe]::GetConsoleWindow().ToInt64(); [Console]::OutputEncoding.WebName; [Console]::In.ReadLine(); '中文目录'; [Console]::Error.WriteLine('中文错误'); exit 7`)
	cmd.Env = append(os.Environ(), "CC_UTF8_SHELL_TEST=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 8} // DETACHED_PROCESS, as used by Codex.
	cmd.Stdin = strings.NewReader("stdin中文\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7; stderr: %s", err, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "0\r\nutf-8\r\n") || !strings.Contains(stdout.String(), "stdin中文") || !strings.Contains(stdout.String(), "中文目录") || !strings.Contains(stderr.String(), "中文错误") {
		t.Fatalf("detached shell created a console window or lost UTF-8: out=%q err=%q", stdout.String(), stderr.String())
	}
}

// Reproduce the real install -> daemon environment -> per-session PATH
// injection journey. A project-only PATH override was overwritten by this
// injection, so testing the wrapper alone missed the user-visible bug.
func TestInstaller_UTF8SurvivesSessionPATHInjection(t *testing.T) {
	powershell, err := findPowerShell()
	if err != nil {
		t.Skip(err)
	}
	dataDir := filepath.Join(t.TempDir(), "中文目录")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dataDir, "cc-connect-daemon.ps1")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	if err := os.WriteFile(scriptPath, []byte("$env:PATH = "+quote(os.Getenv("PATH"))+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	installer, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installer, "-DataDir", dataDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install %d: %v\n%s", i, err, out)
		}
	}
	wrapper := filepath.Join(dataDir, "utf8-shell", "pwsh.exe")
	// Match the engine prepending its binary directory to the daemon's PATH.
	check := ". " + quote(scriptPath) + "; $env:PATH = " + quote(t.TempDir()) +
		" + ';' + $env:PATH; @{path=$env:PATH; shell=(Get-Command pwsh).Source} | ConvertTo-Json -EscapeHandling EscapeNonAscii -Compress"
	cmd := exec.Command(powershell, "-NoProfile", "-Command", check)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon environment: %v\n%s", err, out)
	}
	var environment struct{ Path, Shell string }
	if err := json.Unmarshal(out, &environment); err != nil {
		t.Fatalf("decode daemon environment: %v\n%s", err, out)
	}
	selectedShell, err := os.Stat(environment.Shell)
	if err != nil {
		t.Fatal(err)
	}
	expectedShell, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(selectedShell, expectedShell) {
		t.Fatalf("selected shell = %q, want %q", environment.Shell, wrapper)
	}
	// Launch directly as Codex does, without an outer PowerShell recoding the
	// native process output through its own CP936 pipeline.
	cmd = exec.Command(environment.Shell, "-NoProfile", "-Command",
		`[Console]::OutputEncoding.WebName; (Get-Location).Path; Write-Output '中文文件'; exit 7`)
	cmd.Env = append(os.Environ(), "PATH="+environment.Path)
	cmd.Dir = dataDir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	out, err = cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("probe: %v\n%s", err, out)
	}
	for _, want := range []string{"utf-8", "中文目录", "中文文件"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
	if strings.ContainsRune(string(out), '\uFFFD') {
		t.Errorf("output contains replacement characters: %s", out)
	}
	wrapperDir, err := os.Stat(filepath.Dir(wrapper))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range filepath.SplitList(environment.Path) {
		if info, err := os.Stat(entry); err == nil && os.SameFile(info, wrapperDir) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("wrapper directory appears %d times after reinstall, want 1", n)
	}
	backups, err := filepath.Glob(filepath.Join(dataDir, "backups", "utf8-shell-*", "cc-connect-daemon.ps1"))
	if err != nil || len(backups) != 2 {
		t.Errorf("backups = %v, error = %v", backups, err)
	}
}

func TestInstaller_BundledWrapperDoesNotRequireGo(t *testing.T) {
	dataDir, bundleDir := t.TempDir(), t.TempDir()
	installer, err := os.ReadFile("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		filepath.Join(bundleDir, "install.ps1"):         installer,
		filepath.Join(bundleDir, "pwsh.exe"):            []byte("bundled release wrapper"),
		filepath.Join(dataDir, "cc-connect-daemon.ps1"): []byte("$env:PATH = 'C:\\Windows\\System32'\r\n"),
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(powershell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(bundleDir, "install.ps1"), "-DataDir", dataDir)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(os.Getenv("SystemRoot"), "System32"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install without Go: %v\n%s", err, out)
	}
	installed, err := os.ReadFile(filepath.Join(dataDir, "utf8-shell", "pwsh.exe"))
	if err != nil || string(installed) != "bundled release wrapper" {
		t.Fatalf("installed wrapper = %q, error = %v", installed, err)
	}
}

func TestInstaller_MultipleGoCommandsUsesFirst(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip(err)
	}
	dataDir, otherGoDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "cc-connect-daemon.ps1"), []byte("$env:PATH = 'C:\\Windows\\System32'\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherGoDir, "go.cmd"), []byte("@echo off\r\nexit /b 99\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	installer, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(powershell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installer, "-DataDir", dataDir)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(goPath)+";"+otherGoDir+";"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install with multiple Go commands: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "utf8-shell", "pwsh.exe")); err != nil {
		t.Fatal(err)
	}
}
