package claudecode

import (
	"log/slog"
)

// processRef identifies one process in the OS process table.
//
// Identity is an opaque token produced by the platform probe. It lets the
// probe tell "the process we captured" apart from "a different process that
// happens to reuse the same PID": Windows recycles PIDs, so a bare PID is
// not a safe identity for a process we are willing to kill.
type processRef struct {
	PID      int
	Identity string
}

// processTreeProbe inspects the OS process table.
//
// It is an interface so the orphan-verification logic can be unit tested on
// any platform, while the actual process table access stays behind the
// platform-specific implementation.
type processTreeProbe interface {
	// descendants returns the descendants of the process with the given PID.
	// It must be called while that process is still alive: once it exits,
	// its descendants are re-parented and can no longer be found by walking
	// down from it.
	descendants(rootPID int) []processRef
	// alive reports whether a process with the given PID is running.
	alive(pid int) bool
	// identity returns an opaque token identifying the process with the
	// given PID (its start time on Windows), or "" when the process cannot
	// be inspected.
	identity(pid int) string
	// kill terminates the process with the given PID.
	kill(pid int) error
}

// treeCleanupResult reports what happened to the descendants that were still
// running after the agent process was closed.
type treeCleanupResult struct {
	// killed are the orphans that were found and are gone now.
	killed []processRef
	// survived are the orphans that are still running.
	survived []processRef
}

// killSurvivingDescendants kills the captured descendants that are still
// running and reports which of them survived the attempt.
//
// Killing the agent process is not enough on Windows: taskkill /T only
// terminates the tree as it exists at kill time, so a descendant that was
// re-parented before the kill (its parent died first) or that was spawned
// while the kill was in flight survives the escalation and keeps running
// with the session's credentials — the orphan tree of issue #2209.
func killSurvivingDescendants(probe processTreeProbe, refs []processRef) treeCleanupResult {
	var result treeCleanupResult
	for _, ref := range refs {
		if !processStillAlive(probe, ref) {
			continue
		}
		result.killed = append(result.killed, ref)
		if err := probe.kill(ref.PID); err != nil {
			slog.Warn("claudeSession: killing orphaned descendant failed",
				"pid", ref.PID, "error", err)
		}
		if processStillAlive(probe, ref) {
			result.survived = append(result.survived, ref)
		}
	}
	return result
}

// processStillAlive reports whether the captured process is still the same
// process that was captured.
//
// The identity check is what keeps a PID that has been recycled in the
// meantime from being reported — and killed — as our orphan.
func processStillAlive(probe processTreeProbe, ref processRef) bool {
	if ref.Identity == "" {
		// The process could not be identified when it was captured, so it
		// cannot be proven to be the same process now.
		return false
	}
	if !probe.alive(ref.PID) {
		return false
	}
	return probe.identity(ref.PID) == ref.Identity
}

// survivorPIDs returns the PIDs of the given processes, for log and error
// messages.
func survivorPIDs(refs []processRef) []int {
	pids := make([]int, 0, len(refs))
	for _, ref := range refs {
		pids = append(pids, ref.PID)
	}
	return pids
}

// mergeProcessTree adds the descendants that are not in base yet.
//
// The tree is captured several times during a close, because a descendant
// forked while the close was already running would be missing from the
// first snapshot — and it becomes invisible as soon as the direct child
// dies.
func mergeProcessTree(base, extra []processRef) []processRef {
	seen := make(map[int]bool, len(base))
	for _, ref := range base {
		seen[ref.PID] = true
	}
	for _, ref := range extra {
		if seen[ref.PID] {
			continue
		}
		seen[ref.PID] = true
		base = append(base, ref)
	}
	return base
}
