package core

import (
	"os"
	"path/filepath"
	"strings"
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

	paths := SaveFilesToDisk(workDir, files)

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
