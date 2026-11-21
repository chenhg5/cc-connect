//go:build windows

package claudecode

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/chenhg5/cc-connect/internal/taskkill"
)

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION
// (0x1000). The syscall package only exports PROCESS_QUERY_INFORMATION,
// which needs more rights than reading a process' start time does.
const processQueryLimitedInformation = 0x1000

// windowsProcessTreeProbe inspects the Windows process table.
type windowsProcessTreeProbe struct{}

// descendants returns the descendants of the process with the given PID.
//
// The list is built from a Toolhelp32 snapshot of the process table, so it
// has to be taken while the direct child is still alive: once it exits, its
// descendants are re-parented and can no longer be found by walking down
// from it.
func (windowsProcessTreeProbe) descendants(rootPID int) []processRef {
	if rootPID <= 0 {
		return nil
	}
	children := windowsProcessChildren()
	if len(children) == 0 {
		return nil
	}

	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	var refs []processRef
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			queue = append(queue, child)
			refs = append(refs, processRef{
				PID:      child,
				Identity: windowsProcessIdentity(child),
			})
		}
	}
	return refs
}

// newProcessTreeProbe returns the probe for the current platform.
func newProcessTreeProbe() processTreeProbe {
	return windowsProcessTreeProbe{}
}

// alive reports whether a process with the given PID is running.
func (windowsProcessTreeProbe) alive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}

// identity returns the start time of the process with the given PID, which
// identifies it even after its PID has been recycled.
func (windowsProcessTreeProbe) identity(pid int) string {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	identity, err := windowsProcessTimes(handle)
	if err != nil {
		return ""
	}
	return identity
}

// kill terminates the process with the given PID.
func (windowsProcessTreeProbe) kill(pid int) error {
	killCmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if taskkill.IsNotRunning(err) {
		return nil
	}
	return fmt.Errorf("taskkill failed: %w: %s", err, processKillOutput(output))
}

// windowsProcessChildren snapshots the process table and maps every parent
// PID to the PIDs of its children.
func windowsProcessChildren() map[int][]int {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		slog.Debug("claudeSession: process table snapshot failed", "error", err)
		return nil
	}
	defer func() { _ = syscall.CloseHandle(snapshot) }()

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		slog.Debug("claudeSession: process table snapshot is empty", "error", err)
		return nil
	}

	children := make(map[int][]int)
	for {
		parent := int(entry.ParentProcessID)
		children[parent] = append(children[parent], int(entry.ProcessID))
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return children
}

// windowsProcessIdentity returns an opaque token identifying the process with
// the given PID. It is empty when the process cannot be inspected, in which
// case the process must not be acted on later.
func windowsProcessIdentity(pid int) string {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	identity, err := windowsProcessTimes(handle)
	if err != nil {
		return ""
	}
	return identity
}

// windowsProcessTimes returns the process start time as an opaque token.
// The start time is what tells a captured process apart from a later
// process that reuses its PID.
func windowsProcessTimes(handle syscall.Handle) (string, error) {
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10), nil
}
