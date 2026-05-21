package claudecode

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestClaudecodeAgent_WorkDirRaceFreeReaders pins the bug where
// ListSessions, DeleteSession, GetSessionHistory, CommandDirs,
// SkillDirs, and ProjectMemoryFile read a.workDir without holding
// a.mu, while SetWorkDir writes a.workDir under the lock. Run with
// -race to detect the data race; with the production fix the
// detector stays quiet.
//
// claudecode is the primary agent and exposes the most readers of
// the workDir field across the project, so the cumulative impact of
// this race is the largest of any agent.
func TestClaudecodeAgent_WorkDirRaceFreeReaders(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{workDir: dir}

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
			_ = a.CommandDirs()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.SkillDirs()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.ProjectMemoryFile()
		}()
	}
	wg.Wait()
}
