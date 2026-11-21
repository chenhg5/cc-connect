//go:build !windows

package claudecode

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

// startClaudeUsagePTY starts cmd on a new pseudo-terminal of the given size.
// It returns the terminal (closing it releases the PTY) and a function that
// waits for the process to exit.
func startClaudeUsagePTY(cmd *exec.Cmd, rows, cols uint16) (io.ReadWriteCloser, func() error, error) {
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, nil, err
	}
	return ptmx, cmd.Wait, nil
}

// newClaudeUsageProbeTerminal returns the terminal emulator for the probe's
// PTY output. A Unix PTY passes Claude Code's output through unchanged, which
// the default terminal handles.
func newClaudeUsageProbeTerminal(_, _ int) *claudeUsageTerminal {
	return newClaudeUsageTerminal()
}
