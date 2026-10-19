//go:build unix

package cursor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// prepareCmdForKill puts the child in its own process group so same-group
// children die with the CLI root. Descendants that call setpgid are handled
// separately by forceKillCmd's process snapshot.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return killPID(-cmd.Process.Pid, sig)
}

func killPID(pid int, sig syscall.Signal) error {
	if pid == 0 || pid == 1 || pid == -1 {
		return nil
	}
	err := syscall.Kill(pid, sig)
	if err == nil || errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// forceKillCmd stops cmd and descendants, including tool shells that called
// setpgid and would otherwise survive a kill of the CLI's own group.
// Descendants are recorded before the root is signalled. A process group is
// signalled only when every member is the root or one of those descendants.
func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	root := cmd.Process.Pid
	procs := snapshotProcesses()
	desc := descendantPIDs(procs, root)
	allowed := map[int]struct{}{root: {}}
	for _, pid := range desc {
		if pid > 1 {
			allowed[pid] = struct{}{}
		}
	}
	var errs []error
	signalled := map[int]struct{}{}
	for pg := range exclusiveGroups(procs, allowed) {
		if pg <= 1 {
			continue
		}
		if err := killPID(-pg, syscall.SIGKILL); err != nil {
			errs = append(errs, fmt.Errorf("kill process group %d: %w", pg, err))
			continue
		}
		signalled[pg] = struct{}{}
	}
	for _, pid := range desc {
		if pid <= 1 {
			continue
		}
		if err := killPID(pid, syscall.SIGKILL); err != nil {
			errs = append(errs, fmt.Errorf("kill descendant %d: %w", pid, err))
		}
	}
	rootPGID := procs[root].pgid
	if _, ok := signalled[rootPGID]; !ok {
		if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil {
			errs = append(errs, fmt.Errorf("kill root group: %w", err))
		}
	}
	if err := cmd.Process.Kill(); err != nil &&
		!errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH) {
		errs = append(errs, fmt.Errorf("kill root process: %w", err))
	}
	return errors.Join(errs...)
}

type procInfo struct {
	ppid int
	pgid int
}

func snapshotProcesses() map[int]procInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := runBounded(ctx, "ps", "-ax", "-o", "pid=,ppid=,pgid=")
	if err != nil {
		var fallbackErr error
		out, fallbackErr = runBounded(ctx, "ps", "-ax", "-o", "pid=,ppid=")
		if fallbackErr != nil {
			slog.Warn("cursor: process snapshot failed", "error", err, "fallback", fallbackErr)
			return nil
		}
	}
	return parseProcessSnapshot(out)
}

func runBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	return cmd.Output()
}

func parseProcessSnapshot(out []byte) map[int]procInfo {
	procs := make(map[int]procInfo)
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || pid <= 0 {
			continue
		}
		info := procInfo{ppid: ppid}
		if len(fields) >= 3 {
			info.pgid, _ = strconv.Atoi(fields[2])
		}
		procs[pid] = info
	}
	return procs
}

func descendantPIDs(procs map[int]procInfo, root int) []int {
	if len(procs) == 0 || root <= 1 {
		return nil
	}
	children := make(map[int][]int)
	for pid, info := range procs {
		if pid == root || info.ppid <= 0 {
			continue
		}
		children[info.ppid] = append(children[info.ppid], pid)
	}
	var out []int
	stack := append([]int(nil), children[root]...)
	seen := map[int]struct{}{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid <= 1 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
		stack = append(stack, children[pid]...)
	}
	return out
}

// exclusiveGroups returns process groups whose members are all in allowed.
func exclusiveGroups(procs map[int]procInfo, allowed map[int]struct{}) map[int]struct{} {
	members := make(map[int][]int)
	for pid, info := range procs {
		if info.pgid <= 1 {
			continue
		}
		members[info.pgid] = append(members[info.pgid], pid)
	}
	out := make(map[int]struct{})
	for pid := range allowed {
		pg := procs[pid].pgid
		if pg <= 1 {
			continue
		}
		ok := true
		for _, member := range members[pg] {
			if _, allowedMember := allowed[member]; !allowedMember {
				ok = false
				break
			}
		}
		if ok && len(members[pg]) > 0 {
			out[pg] = struct{}{}
		}
	}
	return out
}
