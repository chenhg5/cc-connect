package cursor

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestAgent_StartSessionWorkDirRace exercises concurrent SetWorkDir + StartSession.
// Without the fix, StartSession reads a.workDir without holding a.mu while
// SetWorkDir writes it under the lock, which Go's -race detector flags as a
// data race. With the fix, the field is captured inside the existing critical
// section and no race is reported.
//
// newCursorSession only initialises the session struct; it does not spawn the
// Cursor agent CLI until Send() is called, so this test runs without requiring
// the binary on PATH.
func TestAgent_StartSessionWorkDirRace(t *testing.T) {
	a := &Agent{cmd: "agent", workDir: "/initial"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			a.SetWorkDir(fmt.Sprintf("/path-%d", i))
		}(i)
		go func() {
			defer wg.Done()
			sess, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Errorf("StartSession: %v", err)
				return
			}
			_ = sess.Close()
		}()
	}
	wg.Wait()
}
