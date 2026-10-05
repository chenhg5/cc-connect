package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func main() {
	os.Exit(runShell(os.Args[1:]))
}

func runShell(args []string) int {
	shell, err := findPowerShell()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Set the console code page before PowerShell initializes its output
	// encoding. This also works when its sandbox uses ConstrainedLanguage.
	kernel := syscall.NewLazyDLL("kernel32.dll")
	if cp, _, _ := kernel.NewProc("GetConsoleOutputCP").Call(); cp == 0 {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// Detached Codex tools have no console. CREATE_NO_WINDOW supplies a
		// console code page without creating a window or Windows Terminal tab.
		cmd := exec.Command(self, os.Args[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		return runChild(cmd)
	}
	for _, name := range []string{"SetConsoleCP", "SetConsoleOutputCP"} {
		if ok, _, err := kernel.NewProc(name).Call(65001); ok == 0 {
			fmt.Fprintln(os.Stderr, name, err)
			return 1
		}
	}
	cmd := exec.Command(shell, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return runChild(cmd)
}

func runChild(cmd *exec.Cmd) int {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func findPowerShell() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return "", err
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, "pwsh.exe")
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && !os.SameFile(selfInfo, info) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("pwsh.exe: no other PowerShell executable found in PATH")
}
