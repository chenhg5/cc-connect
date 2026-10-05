//go:build windows

package main

import (
	"bytes"
	"context"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLauncher_GUIStdioEOFAndExitCode(t *testing.T) {
	dir := t.TempDir()
	helper := `package main
import("encoding/json";"os";"fmt")
func main(){if len(os.Args)!=2 || os.Args[1]!="codex-plugin" {os.Exit(99)};var request any;if json.NewDecoder(os.Stdin).Decode(&request)!=nil{os.Exit(98)};fmt.Println("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}");var last any;json.NewDecoder(os.Stdin).Decode(&last);fmt.Fprintln(os.Stderr,"EOF received");os.Exit(7)}`
	path := filepath.Join(dir, "helper.go")
	if err := os.WriteFile(path, []byte(helper), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "cc-connect.exe"), path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	launcher := filepath.Join(dir, "cc-connect-plugin.exe")
	build = exec.Command("go", "build", "-ldflags", "-H windowsgui", "-o", launcher, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v %s", err, out)
	}
	f, err := pe.Open(launcher)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if h, ok := f.OptionalHeader.(*pe.OptionalHeader64); !ok || h.Subsystem != 2 {
		t.Fatal("launcher does not have Windows GUI subsystem")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, launcher)
	cmd.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("exit forwarding: %v", err)
	}
	if !strings.Contains(stdout.String(), `"jsonrpc":"2.0"`) || !strings.Contains(stderr.String(), "EOF received") {
		t.Fatalf("stdio: out=%q err=%q", stdout.String(), stderr.String())
	}

	// The task scheduler must enter through the GUI launcher too. Starting
	// powershell.exe with WindowStyle Hidden still opens Windows Terminal.
	script := filepath.Join(dir, "supervisor probe.ps1")
	if err := os.WriteFile(script, []byte(`Add-Type 'using System; using System.Runtime.InteropServices; public class ConsoleProbe { [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow(); }'; [ConsoleProbe]::GetConsoleWindow().ToInt64(); [Console]::Error.WriteLine('supervisor stderr'); exit 7`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, launcher, "--supervisor", script)
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err == nil || cmd.ProcessState.ExitCode() != 7 || strings.TrimSpace(stdout.String()) != "0" || !strings.Contains(stderr.String(), "supervisor stderr") {
		t.Fatalf("supervisor must have no console and forward exit/stdio: %v out=%q err=%q", err, stdout.String(), stderr.String())
	}
}
