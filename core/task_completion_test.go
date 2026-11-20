package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskCompletionIncludesOnlyFreshLinkedWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	started := time.Now().Add(-time.Minute)
	for path, data := range map[string]string{
		filepath.Join(dir, "new report.txt"): "deliver",
		filepath.Join(dir, "old.txt"):        "old",
		filepath.Join(dir, ".hidden.txt"):    "private",
		filepath.Join(dir, "unlinked.txt"):   "not selected",
		outside:                              "outside",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stale := started.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "old.txt"), stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	text, files := completionFiles("[report](new%20report.txt) [again](<"+dir+"/new report.txt>) "+
		"[old](old.txt) [hidden](.hidden.txt) [escape](link.txt) [outside]("+outside+") [web](https://example.com)", dir, started, true)
	if len(files) != 1 || files[0].FileName != "new report.txt" || string(files[0].Data) != "deliver" {
		t.Fatalf("unexpected files: %+v", files)
	}
	if strings.Contains(text, dir) || strings.Contains(text, outside) || !strings.Contains(text, "[web](https://example.com)") {
		t.Fatalf("unexpected completion text: %q", text)
	}
}
