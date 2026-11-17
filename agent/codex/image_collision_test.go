package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Sessions share the project work directory; each attachment must remain intact
// after other sessions have staged their images there.
func TestStageImages_ConcurrentSessionsPreserveAttachments(t *testing.T) {
	const sessions = 128
	workDir := t.TempDir()
	start := make(chan struct{})
	type staged struct {
		paths []string
		data  [][]byte
		err   error
	}
	results := make(chan staged, sessions)
	var group sync.WaitGroup
	for i := 0; i < sessions; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			cs := &codexSession{workDir: workDir}
			data := [][]byte{[]byte(fmt.Sprintf("session-%d-png", index)), []byte(fmt.Sprintf("session-%d-jpeg", index))}
			<-start
			_, paths, err := cs.stageImages("read images", []core.ImageAttachment{
				{MimeType: "image/png", Data: data[0]},
				{MimeType: "image/jpeg", Data: data[1]},
			})
			results <- staged{paths: paths, data: data, err: err}
		}(i)
	}
	close(start)
	group.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.paths) != 2 {
			t.Fatalf("staged %d images, want 2", len(result.paths))
		}
		for i, path := range result.paths {
			if seen[path] {
				t.Fatalf("concurrent sessions reused image path %q", path)
			}
			seen[path] = true
			if filepath.Ext(path) != []string{".png", ".jpg"}[i] {
				t.Fatalf("image has wrong extension: %q", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(result.data[i]) {
				t.Fatalf("another session overwrote image %q: got %q, want %q", path, data, result.data[i])
			}
		}
	}
}
