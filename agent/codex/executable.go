package codex

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
)

// An empty command enables discovery. Explicit commands (including "codex")
// must resolve as configured; a typo must not select a different installation.
func resolveCodexExecutable(command string) (string, error) {
	return findCodexExecutable(command, runtime.GOOS, []string{
		"/Applications/ChatGPT.app",
		"/Applications/Codex.app",
	})
}

func findCodexExecutable(command, goos string, applications []string) (string, error) {
	if command != "" {
		return exec.LookPath(command)
	}
	path, err := exec.LookPath("codex")
	if err == nil {
		return path, nil
	}
	// Preserve Go's current-directory protection and other lookup errors.
	// A missing command or dangling PATH symlink permits desktop discovery.
	if goos != "darwin" || !errors.Is(err, exec.ErrNotFound) {
		return "", err
	}
	for _, application := range applications {
		for _, layout := range []string{
			"Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
			"Contents/Resources/codex",
		} {
			if path, err := exec.LookPath(filepath.Join(application, layout)); err == nil {
				return path, nil
			}
		}
	}
	return "", err
}

// The doctor uses the same executable as turns and metadata queries.
func (a *Agent) CLIBinaryName() string {
	a.mu.RLock()
	command := a.cmd
	a.mu.RUnlock()
	if path, err := resolveCodexExecutable(command); err == nil {
		return path
	}
	if command != "" {
		return command
	}
	return "codex"
}

func (a *Agent) CLIDisplayName() string { return "Codex" }
