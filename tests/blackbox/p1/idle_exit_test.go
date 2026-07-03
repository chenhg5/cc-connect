//go:build blackbox

package p1

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

// TestP1_IdleExit_ReclaimAndResume_ClaudeCode verifies idle_exit_mins
// end-to-end with a real agent: after the idle timeout the agent subprocess
// is closed (memory reclaimed), and the next message transparently resumes
// the same conversation with its context intact.
func TestP1_IdleExit_ReclaimAndResume_ClaudeCode(t *testing.T) {
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetIdleExit(5 * time.Second)
	})

	env.SendComplete("Remember the codeword: PINEAPPLE42. Reply with just OK.")

	// The reaper ticks once per minute; poll generously for the subprocess
	// to disappear. Process observation uses /proc, so it is Linux-only;
	// on other platforms fall back to asserting resume behavior only.
	if runtime.GOOS == "linux" {
		if pids := agentProcsInDir(t, env.WorkDir); len(pids) == 0 {
			t.Fatal("expected a live agent subprocess after the first turn")
		}
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			if len(agentProcsInDir(t, env.WorkDir)) == 0 {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if pids := agentProcsInDir(t, env.WorkDir); len(pids) != 0 {
			t.Fatalf("agent subprocess still alive after idle timeout: %v", pids)
		}
	} else {
		// Give the reaper time to fire before testing resume.
		time.Sleep(70 * time.Second)
	}

	reply := env.SendWithTimeout("What is the codeword? Reply with just the codeword.", 120*time.Second)
	if !strings.Contains(reply.Text(), "PINEAPPLE42") {
		t.Fatalf("resumed session lost context: reply %q does not contain codeword", reply.Text())
	}
}

// agentProcsInDir returns PIDs of processes whose cwd is dir (the agent
// subprocess is spawned with its working directory set to the session's
// work_dir, which is a unique temp dir per test).
func agentProcsInDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	var pids []string
	for _, e := range entries {
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd"))
		if err != nil {
			continue // process exited or not ours
		}
		if cwd == dir {
			pids = append(pids, pid)
		}
	}
	return pids
}
