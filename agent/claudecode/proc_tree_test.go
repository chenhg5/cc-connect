package claudecode

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// ── Issue #2209: a session close must not leave an orphan process tree ─────
//
// On Windows the escalation that kills the agent process (taskkill /T /F)
// only terminates the tree as it exists at kill time. A descendant that was
// re-parented before the kill, or that was spawned while the kill was in
// flight, survives and keeps running with the session's credentials.
//
// The verification therefore snapshots the descendants while the direct
// child is still alive and checks them after the kill. The platform-specific
// process table access lives behind processTreeProbe, so the decision logic
// is tested here on any platform.

// fakeProcessTreeProbe is a process table stub.
type fakeProcessTreeProbe struct {
	// running maps a PID to whether the process is running.
	running map[int]bool
	// identities maps a PID to its identity token.
	identities map[int]string
	// killErr makes the kill of that PID fail.
	killErr map[int]error
	// killRemoves makes a successful kill remove the process.
	killRemoves bool
	// killed records the PIDs the probe was asked to kill.
	killed []int
	// tree is what descendants() reports.
	tree []processRef
}

func (f *fakeProcessTreeProbe) descendants(int) []processRef {
	return f.tree
}

func (f *fakeProcessTreeProbe) alive(pid int) bool {
	return f.running[pid]
}

func (f *fakeProcessTreeProbe) identity(pid int) string {
	return f.identities[pid]
}

func (f *fakeProcessTreeProbe) kill(pid int) error {
	f.killed = append(f.killed, pid)
	if err := f.killErr[pid]; err != nil {
		return err
	}
	if f.killRemoves {
		delete(f.running, pid)
	}
	return nil
}

func newFakeProbe(alive map[int]bool, identities map[int]string) *fakeProcessTreeProbe {
	return &fakeProcessTreeProbe{
		running:    alive,
		identities: identities,
		killErr:    map[int]error{},
	}
}

func TestKillSurvivingDescendants_NoSurvivors(t *testing.T) {
	probe := newFakeProbe(map[int]bool{}, map[int]string{})

	result := killSurvivingDescendants(probe, []processRef{
		{PID: 10, Identity: "a"},
		{PID: 11, Identity: "b"},
	})

	if len(result.killed) != 0 || len(result.survived) != 0 {
		t.Fatalf("result = %+v, want no survivors", result)
	}
	if len(probe.killed) != 0 {
		t.Fatalf("killed = %v, want no kill attempts", probe.killed)
	}
}

func TestKillSurvivingDescendants_KillsOrphan(t *testing.T) {
	probe := newFakeProbe(map[int]bool{11: true}, map[int]string{10: "a", 11: "b"})
	probe.killRemoves = true

	result := killSurvivingDescendants(probe, []processRef{
		{PID: 10, Identity: "a"},
		{PID: 11, Identity: "b"},
	})

	if len(result.killed) != 1 || result.killed[0].PID != 11 {
		t.Fatalf("killed = %+v, want pid 11", result.killed)
	}
	if len(result.survived) != 0 {
		t.Fatalf("survived = %+v, want none", result.survived)
	}
	if len(probe.killed) != 1 || probe.killed[0] != 11 {
		t.Fatalf("kill attempts = %v, want [11]", probe.killed)
	}
}

func TestKillSurvivingDescendants_ReportsSurvivorThatSurvivedTheKill(t *testing.T) {
	probe := newFakeProbe(map[int]bool{11: true}, map[int]string{11: "b"})
	probe.killRemoves = false

	result := killSurvivingDescendants(probe, []processRef{{PID: 11, Identity: "b"}})

	if len(result.survived) != 1 || result.survived[0].PID != 11 {
		t.Fatalf("survived = %+v, want pid 11", result.survived)
	}
}

func TestKillSurvivingDescendants_KillFailureIsReported(t *testing.T) {
	probe := newFakeProbe(map[int]bool{11: true}, map[int]string{11: "b"})
	probe.killErr[11] = errors.New("access denied")

	result := killSurvivingDescendants(probe, []processRef{{PID: 11, Identity: "b"}})

	if len(result.survived) != 1 || result.survived[0].PID != 11 {
		t.Fatalf("survived = %+v, want pid 11", result.survived)
	}
}

// A PID that has been recycled in the meantime belongs to a different
// process: it must not be reported as our orphan, and it must not be killed.
func TestKillSurvivingDescendants_IgnoresRecycledPID(t *testing.T) {
	probe := newFakeProbe(map[int]bool{11: true}, map[int]string{11: "different-process"})

	result := killSurvivingDescendants(probe, []processRef{{PID: 11, Identity: "captured"}})

	if len(result.killed) != 0 || len(result.survived) != 0 {
		t.Fatalf("result = %+v, want a recycled PID to be ignored", result)
	}
	if len(probe.killed) != 0 {
		t.Fatalf("killed = %v, want no kill attempt for a recycled PID", probe.killed)
	}
}

// A process that could not be identified when it was captured cannot be
// proven to be the same process later, so it must be left alone.
func TestKillSurvivingDescendants_IgnoresProcessWithoutIdentity(t *testing.T) {
	probe := newFakeProbe(map[int]bool{11: true}, map[int]string{11: "b"})

	result := killSurvivingDescendants(probe, []processRef{{PID: 11, Identity: ""}})

	if len(result.killed) != 0 || len(result.survived) != 0 {
		t.Fatalf("result = %+v, want an unidentifiable process to be ignored", result)
	}
	if len(probe.killed) != 0 {
		t.Fatalf("killed = %v, want no kill attempt", probe.killed)
	}
}

func TestVerifyTreeGone_ReportsSurvivingPIDs(t *testing.T) {
	cs := &claudeSession{}
	probe := newFakeProbe(map[int]bool{21: true, 22: true}, map[int]string{21: "a", 22: "b"})
	probe.killRemoves = false

	err := cs.verifyTreeGone(probe, []processRef{
		{PID: 21, Identity: "a"},
		{PID: 22, Identity: "b"},
	})

	if err == nil {
		t.Fatal("verifyTreeGone must report the survivors")
	}
	for _, want := range []string{"21", "22"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention surviving pid %s", err, want)
		}
	}
}

func TestVerifyTreeGone_EmptyTreeIsNoop(t *testing.T) {
	cs := &claudeSession{}
	probe := newFakeProbe(map[int]bool{21: true}, map[int]string{21: "a"})

	if err := cs.verifyTreeGone(probe, nil); err != nil {
		t.Fatalf("verifyTreeGone(nil) = %v, want nil", err)
	}
	if len(probe.killed) != 0 {
		t.Fatalf("killed = %v, want no kill attempts", probe.killed)
	}
}

// An orphan that the kill actually removes must not turn the close into a
// failure: the tree is gone, so the session can be resumed safely.
func TestVerifyTreeGone_KilledOrphanIsNotAFailure(t *testing.T) {
	cs := &claudeSession{}
	probe := newFakeProbe(map[int]bool{21: true}, map[int]string{21: "a"})
	probe.killRemoves = true

	if err := cs.verifyTreeGone(probe, []processRef{{PID: 21, Identity: "a"}}); err != nil {
		t.Fatalf("verifyTreeGone = %v, want nil after the orphan was killed", err)
	}
}

// A descendant forked while the close was already running is missing from
// the first snapshot; merging the later ones must pick it up.
func TestMergeProcessTree_PicksUpNewDescendants(t *testing.T) {
	base := []processRef{{PID: 10, Identity: "a"}}
	extra := []processRef{
		{PID: 10, Identity: "a"},
		{PID: 11, Identity: "b"},
	}

	merged := mergeProcessTree(base, extra)

	if len(merged) != 2 {
		t.Fatalf("merged = %+v, want 2 entries", merged)
	}
	if merged[0].PID != 10 || merged[1].PID != 11 {
		t.Fatalf("merged = %+v, want pids 10 and 11", merged)
	}
}

// TestClaudeSessionClose_KillsOrphanedDescendant is the end-to-end
// regression test for issue #2209: a close must not leave a descendant of
// the agent process running.
//
// Before the fix the close only waited for the direct child, so the
// descendant survived silently — and the close reported success.
func TestClaudeSessionClose_KillsOrphanedDescendant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand(ctx, "stdin-eof-exit")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	probe := newFakeProbe(map[int]bool{4242: true}, map[int]string{4242: "orphan"})
	probe.tree = []processRef{{PID: 4242, Identity: "orphan"}}
	probe.killRemoves = true

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		ctx:                 ctx,
		cancel:              cancel,
		done:                done,
		gracefulStopTimeout: 200 * time.Millisecond,
		treeProbe:           probe,
	}
	cs.alive.Store(true)

	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(probe.killed) != 1 || probe.killed[0] != 4242 {
		t.Fatalf("kill attempts = %v, want the orphaned descendant 4242 to be killed", probe.killed)
	}
}

// A descendant that survives the kill must make the close fail, so that the
// caller treats the session as unsafe to resume and tells the user.
func TestClaudeSessionClose_ReportsSurvivingDescendant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand(ctx, "stdin-eof-exit")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	probe := newFakeProbe(map[int]bool{4242: true}, map[int]string{4242: "orphan"})
	probe.tree = []processRef{{PID: 4242, Identity: "orphan"}}
	probe.killRemoves = false

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		ctx:                 ctx,
		cancel:              cancel,
		done:                done,
		gracefulStopTimeout: 200 * time.Millisecond,
		treeProbe:           probe,
	}
	cs.alive.Store(true)

	err = cs.Close()
	if err == nil {
		t.Fatal("Close must fail when a descendant is still running")
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Fatalf("error %q must mention the surviving pid 4242", err)
	}
}
