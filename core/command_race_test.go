package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Regression: CommandRegistry.SetAgentDirs wrote r.agentDirs without holding
// r.mu, and Resolve read r.agentDirs after releasing r.mu.RLock(), so a
// concurrent re-bind raced on the slice header. ListAll already accessed the
// same field under the lock, which made the inconsistency obvious.
//
// Run with `go test -race ./core/ -run TestCommandRegistry_ConcurrentDirSwap`.
func TestCommandRegistry_ConcurrentDirSwap(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "alpha.md"), []byte("alpha prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "beta.md"), []byte("beta prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewCommandRegistry()
	r.Add("config-cmd", "", "from config", "", "", "config")
	r.SetAgentDirs([]string{dirA})

	const writers = 8
	const readers = 16
	const iters = 50

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			dirs := []string{dirA}
			if id%2 == 0 {
				dirs = []string{dirB}
			}
			for i := 0; i < iters; i++ {
				r.SetAgentDirs(dirs)
			}
		}(w)
	}

	for rd := 0; rd < readers; rd++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Config command must always resolve.
				if _, ok := r.Resolve("config-cmd"); !ok {
					t.Errorf("config-cmd failed to resolve")
					return
				}
				// Agent commands may or may not resolve depending on the
				// active dir set; just ensure no panic / race.
				_, _ = r.Resolve("alpha")
				_, _ = r.Resolve("beta")
				_ = r.ListAll()
			}
		}()
	}

	wg.Wait()
}
