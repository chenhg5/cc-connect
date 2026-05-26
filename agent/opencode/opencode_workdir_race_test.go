package opencode

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// TestOpencodeAgent_WorkDirRaceFreeReaders pins the bug where
// ListSessions and ProjectMemoryFile read a.workDir (and a.cmd, in
// ListSessions' case) without holding a.mu, while SetWorkDir writes
// a.workDir under the lock. Run with -race to detect the data race;
// with the production fix the test stays clean.
func TestOpencodeAgent_WorkDirRaceFreeReaders(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{workDir: dir, cmd: "opencode"}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				a.SetWorkDir(filepath.Join(dir, "a"))
			} else {
				a.SetWorkDir(filepath.Join(dir, "b"))
			}
		}(i)
	}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// ListSessions execs `opencode session list`; with a
			// non-existent binary it returns an error immediately.
			// The race detector still observes the unlocked field
			// reads before the exec attempt.
			_, _ = a.ListSessions(context.Background())
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.ProjectMemoryFile()
		}()
	}
	wg.Wait()
}
