//go:build windows

package claudecode

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

// startClaudeUsagePTY starts cmd on a new pseudo console (ConPTY) of the given
// size. github.com/creack/pty cannot start processes on Windows, so the
// /usage probe needs ConPTY here.
//
// exec never starts the process, so cmd.Wait cannot be used. The returned wait
// function waits on the process instead. cmd.Process is set so callers can
// still kill it. Stderr is merged into the console output, and cmd.Stdin,
// cmd.Stdout and cmd.Stderr are ignored.
//
// ConPTY keeps the output pipe open after the process exits. Closing the
// returned terminal kills the process tree and closes the pseudo console,
// which ends any blocked Read.
func startClaudeUsagePTY(cmd *exec.Cmd, rows, cols uint16) (io.ReadWriteCloser, func() error, error) {
	cpty, err := conpty.New(int(cols), int(rows), 0)
	if err != nil {
		return nil, nil, fmt.Errorf("create pseudo console: %w", err)
	}

	pid, handle, err := cpty.Spawn(cmd.Path, cmd.Args, &syscall.ProcAttr{
		Dir: cmd.Dir,
		Env: cmd.Env,
		Sys: cmd.SysProcAttr,
	})
	if err != nil {
		_ = cpty.Close()
		return nil, nil, fmt.Errorf("spawn on pseudo console: %w", err)
	}
	// Keep the creation handle open until os.FindProcess has its own handle,
	// so the PID cannot be reused in between.
	proc, findErr := os.FindProcess(pid)
	if findErr != nil {
		_ = windows.TerminateProcess(windows.Handle(handle), 1)
	}
	_ = windows.CloseHandle(windows.Handle(handle))
	if findErr != nil {
		_ = cpty.Close()
		return nil, nil, fmt.Errorf("find spawned process %d: %w", pid, findErr)
	}
	cmd.Process = proc

	wait := func() error {
		state, err := proc.Wait()
		cmd.ProcessState = state
		if err != nil {
			return err
		}
		// os.Process.Wait does not report a non-zero exit code as an error;
		// return the same error type cmd.Wait would.
		if state != nil && !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
		return nil
	}
	return &claudeUsageConPTY{cpty: cpty, cmd: cmd}, wait, nil
}

// newClaudeUsageProbeTerminal returns the terminal emulator for the probe's
// ConPTY output, which needs the real screen size (see
// newClaudeUsageScreenTerminal).
func newClaudeUsageProbeTerminal(rows, cols int) *claudeUsageTerminal {
	return newClaudeUsageScreenTerminal(rows, cols)
}

// claudeUsageConPTY maps ConPTY pipe errors to the errors os.File reports, so
// the probe's reader loop sees io.EOF or os.ErrClosed when the console goes
// away.
type claudeUsageConPTY struct {
	cpty   *conpty.ConPty
	cmd    *exec.Cmd
	closed atomic.Bool
}

func (p *claudeUsageConPTY) Read(b []byte) (int, error) {
	if p.closed.Load() {
		return 0, os.ErrClosed
	}
	n, err := p.cpty.Read(b)
	if err != nil {
		if p.closed.Load() {
			return n, os.ErrClosed
		}
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			return n, io.EOF
		}
	}
	return n, err
}

func (p *claudeUsageConPTY) Write(b []byte) (int, error) {
	if p.closed.Load() {
		return 0, os.ErrClosed
	}
	return p.cpty.Write(b)
}

// Close kills the process tree, like hanging up a Unix PTY, and closes the
// pseudo console. Killing first matters when claude is an npm .cmd shim: the
// shim (cmd.exe) can exit while node.exe is still shutting down in the
// probe's working directory, and Windows cannot remove a directory that a
// running process uses as its working directory.
func (p *claudeUsageConPTY) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	if err := forceKillCmd(p.cmd); err != nil {
		slog.Warn("claudecode: kill usage probe process tree", "error", err)
	}
	if err := p.cpty.Close(); err != nil {
		return fmt.Errorf("close pseudo console: %w", err)
	}
	return nil
}
