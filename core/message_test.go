package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSanitizeAttachmentFileName covers the basename-stripping rules used by
// SaveFilesToDisk to reject path-traversal in user-supplied filenames.
func TestSanitizeAttachmentFileName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"image.png", "image.png"},
		{"subdir/file.txt", "file.txt"},
		{"../../escape.txt", "escape.txt"},
		{"/etc/passwd", "passwd"},
		// Windows-style separators get normalized so Linux strips them too.
		{`..\..\windows-escape.txt`, "windows-escape.txt"},
		{`C:\Users\foo\bar.exe`, "bar.exe"},
		// Anything that would still join to a parent / current directory is
		// returned as "" so the caller falls back to a generated name.
		{"..", ""},
		{".", ""},
		{"", ""},
		{"../", ""},
		{`..\`, ""},
		{"./../foo", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := sanitizeAttachmentFileName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeAttachmentFileName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUnauthorizedAccessMessage(t *testing.T) {
	if !strings.Contains(UnauthorizedAccessMessage, "角色未授权") {
		t.Fatalf("UnauthorizedAccessMessage = %q, want user-facing authorization hint", UnauthorizedAccessMessage)
	}
	if strings.Contains(UnauthorizedAccessMessage, "allow_from") {
		t.Fatalf("UnauthorizedAccessMessage leaks implementation details: %q", UnauthorizedAccessMessage)
	}
}

// TestSaveFilesToDisk_RejectsPathTraversal is a regression test for a real
// path-traversal vulnerability in SaveFilesToDisk: the attachment FileName
// (which comes from user-controlled IM/HTTP upload metadata) was passed
// directly to filepath.Join, so an attacker uploading a file named
// "../../escape.txt" wrote outside the intended attachments directory into
// the agent's workDir / above. The fix sanitizes FileName to a basename;
// this test asserts every file lands inside attachDir, with no escapees.
func TestSaveFilesToDisk_RejectsPathTraversal(t *testing.T) {
	workDir := t.TempDir()
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")

	files := []FileAttachment{
		// The original repro: walks two levels up out of attachments and
		// out of .cc-connect/, landing directly in workDir.
		{FileName: "../../escape.txt", Data: []byte("payload")},
		// Three levels up — would land in workDir's parent without the fix.
		{FileName: "../../../way-up.txt", Data: []byte("payload")},
		// Windows-style separators must also be stripped on Linux so a
		// cross-platform attacker can't bypass the basename guard.
		{FileName: `..\..\winescape.txt`, Data: []byte("payload")},
		// Subdirectory in the name — file should land in attachDir, not in
		// a created subdir, since we strip directory components.
		{FileName: "subdir/inner.txt", Data: []byte("payload")},
		// Plain name should still work normally.
		{FileName: "ok.txt", Data: []byte("payload")},
		// A name that sanitizes to empty should fall back to a generated
		// name in attachDir, not crash and not escape.
		{FileName: "..", Data: []byte("payload")},
	}

	paths := SaveFilesToDisk(workDir, "", files)

	// Every returned path must live inside attachDir.
	for _, p := range paths {
		if !strings.HasPrefix(p, attachDir+string(filepath.Separator)) {
			t.Errorf("SaveFilesToDisk wrote outside attachments dir: %q (attachDir=%q)", p, attachDir)
		}
	}

	// Walk the workDir tree and confirm no file landed above attachDir.
	if err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(path, attachDir+string(filepath.Separator)) {
			t.Errorf("found stray attachment outside attachments dir: %q", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Sanity: at minimum the legitimate "ok.txt" must have been written.
	okPath := filepath.Join(attachDir, "ok.txt")
	if _, err := os.Stat(okPath); err != nil {
		t.Errorf("legitimate ok.txt not saved: %v", err)
	}
}

// TestSaveFilesToDisk_RelativeWorkDirReturnsAbsolutePaths guards the
// regression from issue #1459: when workDir is relative (e.g. ".cc-connect"
// or "project/sub"), SaveFilesToDisk used to return relative paths that did
// not match where the file actually landed once the agent process started
// from a different cwd. The fix absolutizes workDir before joining, so the
// returned paths are usable from anywhere — including the spawned agent
// process whose cwd may differ from cc-connect's.
func TestSaveFilesToDisk_RelativeWorkDirReturnsAbsolutePaths(t *testing.T) {
	// Build a real directory under t.TempDir() and feed a relative path
	// to SaveFilesToDisk. The returned paths must be absolute regardless
	// of how the caller spelled workDir.
	base := t.TempDir()
	relWorkDir := filepath.Join(base, "rel") // t.TempDir() is absolute; this makes it relative after the abs() inside SaveFilesToDisk
	// Actually we want a RELATIVE path here, so strip the leading slash:
	// base is absolute (/tmp/xxx), relWorkDir is also absolute. To exercise
	// the relative path, point workDir at "." (cwd-relative) and assert the
	// returned paths are absolute.
	files := []FileAttachment{{FileName: "photo.png", Data: []byte("png")}}

	got := SaveFilesToDisk(relWorkDir, "", files)
	if len(got) != 1 {
		t.Fatalf("SaveFilesToDisk(absolute=%q) returned %d paths, want 1", relWorkDir, len(got))
	}
	if !filepath.IsAbs(got[0]) {
		t.Errorf("SaveFilesToDisk(absolute workDir) returned non-absolute path %q", got[0])
	}

	// Now also exercise a truly relative workDir.
	baseName := filepath.Base(base)
	relativeWorkDir := filepath.Join("rel", baseName, "sub")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join("rel", baseName)) })
	gotRel := SaveFilesToDisk(relativeWorkDir, "", files)
	if len(gotRel) != 1 {
		t.Fatalf("SaveFilesToDisk(relative workDir) returned %d paths, want 1", len(gotRel))
	}
	if !filepath.IsAbs(gotRel[0]) {
		t.Errorf("SaveFilesToDisk(relative workDir) returned non-absolute path %q — agent could not open it (issue #1459)", gotRel[0])
	}
	// The absolute path must resolve to a real file on disk.
	if _, err := os.Stat(gotRel[0]); err != nil {
		t.Errorf("SaveFilesToDisk returned path %q that does not exist: %v", gotRel[0], err)
	}
}

// TestSaveFilesToDisk_AbsoluteWorkDirReturnsAbsolutePaths confirms the
// common case (deploys with an absolute workDir) keeps working — no
// regression for users who already configured cc-connect with absolute
// paths. The returned path is the abs version of the input joined with
// the standard attachments directory.
func TestSaveFilesToDisk_AbsoluteWorkDirReturnsAbsolutePaths(t *testing.T) {
	workDir := t.TempDir() // already absolute
	files := []FileAttachment{
		{FileName: "a.txt", Data: []byte("a")},
		{FileName: "b.pdf", Data: []byte("b")},
	}
	got := SaveFilesToDisk(workDir, "", files)
	if len(got) != 2 {
		t.Fatalf("got %d paths, want 2", len(got))
	}
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("absolute workDir yielded non-absolute path %q", p)
		}
		if !strings.HasPrefix(p, workDir+string(filepath.Separator)) {
			t.Errorf("path %q escaped workDir %q", p, workDir)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("returned path %q does not exist on disk: %v", p, err)
		}
	}
}

// TestSaveFilesToDisk_EmptyWorkDirFallsBackToCwd guards the degraded path
// where workDir was not injected (older host code or test harness). The
// attachment should still land somewhere writable rather than fail the
// spawn — falling back to the process cwd is the documented contract.
func TestSaveFilesToDisk_EmptyWorkDirFallsBackToCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	files := []FileAttachment{{FileName: "hello.txt", Data: []byte("hi")}}
	got := SaveFilesToDisk("", "", files)
	if len(got) != 1 {
		t.Fatalf("empty workDir returned %d paths, want 1", len(got))
	}
	if !filepath.IsAbs(got[0]) {
		t.Errorf("empty workDir yielded non-absolute path %q", got[0])
	}
	wantPrefix := filepath.Join(cwd, ".cc-connect", "attachments") + string(filepath.Separator)
	if !strings.HasPrefix(got[0], wantPrefix) {
		t.Errorf("empty workDir path %q did not fall back to cwd-based attachDir %q", got[0], wantPrefix)
	}
	// Clean up so we don't litter the test cwd.
	t.Cleanup(func() { _ = os.Remove(got[0]) })
}

func TestSaveFilesToDisk_SameNameInOneCall(t *testing.T) {
	workDir := t.TempDir()
	paths := SaveFilesToDisk(workDir, "message-1", []FileAttachment{
		{FileName: "same.txt", Data: []byte("first")},
		{FileName: "same.txt", Data: []byte("second")},
	})
	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2", len(paths))
	}
	if paths[0] == paths[1] {
		t.Fatalf("duplicate attachments share path %q", paths[0])
	}
	contents := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		contents[string(data)] = path
	}
	for _, want := range []string{"first", "second"} {
		if _, ok := contents[want]; !ok {
			t.Errorf("missing attachment content %q; got %#v", want, contents)
		}
	}

	entries, err := os.ReadDir(filepath.Join(workDir, ".cc-connect", "attachments", "message-1"))
	if err != nil {
		t.Fatalf("read message attachment directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("message attachment directory contains %d entries, want 2", len(entries))
	}
}

func TestSaveFilesToDisk_ConcurrentDifferentMessages(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	messageIDs := []string{"message-a", "message-b"}
	contents := [][]byte{[]byte("from a"), []byte("from b")}
	paths := make([][]string, len(messageIDs))

	var wg sync.WaitGroup
	wg.Add(len(messageIDs))
	for i := range messageIDs {
		go func(i int) {
			defer wg.Done()
			paths[i] = SaveFilesToDisk(workDir, messageIDs[i], []FileAttachment{{
				FileName: "same.txt",
				Data:     contents[i],
			}})
		}(i)
	}
	wg.Wait()

	for i, messageID := range messageIDs {
		if len(paths[i]) != 1 {
			t.Fatalf("message %q returned %d paths, want 1", messageID, len(paths[i]))
		}
		path := paths[i][0]
		if filepath.Base(filepath.Dir(path)) != messageID {
			t.Errorf("path %q is not scoped to message %q", path, messageID)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		if string(data) != string(contents[i]) {
			t.Errorf("message %q content = %q, want %q", messageID, data, contents[i])
		}
	}
}

func TestSaveFilesToDisk_NoSubdirBackwardsCompat(t *testing.T) {
	workDir := t.TempDir()
	first := SaveFilesToDisk(workDir, "", []FileAttachment{{
		FileName: "legacy.txt",
		Data:     []byte("original"),
	}})
	if len(first) != 1 {
		t.Fatalf("first legacy save returned %d paths, want 1", len(first))
	}
	wantPath := filepath.Join(workDir, ".cc-connect", "attachments", "legacy.txt")
	if first[0] != wantPath {
		t.Fatalf("legacy path = %q, want %q", first[0], wantPath)
	}

	second := SaveFilesToDisk(workDir, "", []FileAttachment{{
		FileName: "legacy.txt",
		Data:     []byte("replacement"),
	}})
	if len(second) != 0 {
		t.Fatalf("second legacy save returned %d paths, want collision to be rejected", len(second))
	}
	data, err := os.ReadFile(first[0])
	if err != nil {
		t.Fatalf("read original legacy attachment: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("legacy attachment was overwritten: got %q", data)
	}
}

// TestAppendFileRefs_AbsolutizesRelativePaths guards the defensive layer
// in AppendFileRefs: even if a caller (or a future regression) passes
// relative file paths, the prompt handed to the agent is rewritten so
// every path is absolute. The agent then sees real on-disk locations
// regardless of the cwd it was spawned into.
func TestAppendFileRefs_AbsolutizesRelativePaths(t *testing.T) {
	// Mixed input: one absolute, one relative, one that is exactly ".".
	in := []string{
		"/tmp/explicit.txt",
		"sub/dir/relative.txt",
		".",
	}
	got := AppendFileRefs("see attached", in)
	for _, want := range []string{"/tmp/explicit.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("AppendFileRefs dropped absolute path %q from prompt: %q", want, got)
		}
	}
	// Parse the comma-separated entries between "please read them: " and ")"
	// and verify each one is an absolute path. This is the load-bearing check:
	// the prompt handed to the agent must always point at real on-disk
	// locations, never bare relative paths that only resolve from the
	// cc-connect process's cwd.
	listStart := strings.Index(got, "please read them: ")
	if listStart < 0 {
		t.Fatalf("AppendFileRefs output missing the file-ref list marker: %q", got)
	}
	listStart += len("please read them: ")
	listEnd := strings.LastIndex(got, ")")
	if listEnd < 0 || listEnd <= listStart {
		t.Fatalf("AppendFileRefs output missing trailing ')': %q", got)
	}
	entries := strings.Split(got[listStart:listEnd], ", ")
	if len(entries) != len(in) {
		t.Errorf("AppendFileRefs emitted %d entries, want %d (got=%q)", len(entries), len(in), got)
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			t.Errorf("AppendFileRefs emitted an empty entry: %q", got)
			continue
		}
		if !filepath.IsAbs(entry) {
			t.Errorf("AppendFileRefs left non-absolute entry %q in prompt %q", entry, got)
		}
	}
	// Sanity: the original "sub/dir/relative.txt" must NOT appear as a
	// comma-bounded token (i.e. as its own entry). It may still appear as
	// a substring inside an absolute path — that's fine and expected.
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "sub/dir/relative.txt" || entry == "." || entry == "" {
			t.Errorf("AppendFileRefs emitted a bare relative entry %q at index %d: %q", entry, i, got)
		}
	}
}

// TestAppendFileRefs_AbsoluteInputsPassthrough confirms that already-
// absolute paths are not rewritten (no extra syscalls, no normalization
// surprises for callers who handed us a clean path).
func TestAppendFileRefs_AbsoluteInputsPassthrough(t *testing.T) {
	in := []string{"/already/abs/a.txt", "/already/abs/b.txt"}
	got := AppendFileRefs("see attached", in)
	for _, want := range in {
		if !strings.Contains(got, want) {
			t.Errorf("AppendFileRefs rewrote absolute path %q: %q", want, got)
		}
	}
}
