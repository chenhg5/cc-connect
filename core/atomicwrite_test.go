package core

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	data := []byte("hello world")

	if err := AtomicWriteFile(path, data, 0644); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestAtomicWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := AtomicWriteFile(path, []byte("first"), 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("second"), 0644); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

func TestAtomicWriteFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := AtomicWriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
}

func TestAtomicWriteFile_NoTempLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	_ = AtomicWriteFile(path, []byte("data"), 0644)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.txt" {
			t.Errorf("unexpected file left: %s", e.Name())
		}
	}
}

func TestAtomicWrite_TempRenameSameDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")

	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("version %d", i))
		if err := AtomicWriteFile(path, data, 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got) != string(data) {
			t.Errorf("write %d: got %q, want %q", i, got, data)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "target.txt" {
			t.Errorf("unexpected temp file left: %s", e.Name())
		}
	}
}

// TestSecurity_FilePermission verifies that AtomicWriteFile creates files with
// mode 0600, that the temporary file is also restricted before rename, and that
// persisted progress-card JSON files are unreadable by other users.
func TestSecurity_FilePermission(t *testing.T) {
	t.Run("final_json_file_0600", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "data.json")
		data := []byte(`{"message_id":"msg-1","content":"secret"}`)

		if err := AtomicWriteFile(path, data, 0600); err != nil {
			t.Fatalf("AtomicWriteFile: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("final file mode = %o, want 0600", got)
		}
	})

	t.Run("temp_file_0600_before_rename", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "data.json")
		data := []byte(`{"message_id":"msg-1","content":"secret"}`)

		var tempPerm os.FileMode
		hook := func(tmpPath string) error {
			info, err := os.Stat(tmpPath)
			if err != nil {
				return err
			}
			tempPerm = info.Mode().Perm()
			return nil
		}

		if err := atomicWriteFile(path, data, 0600, hook); err != nil {
			t.Fatalf("atomicWriteFile: %v", err)
		}
		if tempPerm != 0600 {
			t.Errorf("temp file mode = %o, want 0600", tempPerm)
		}
	})

	t.Run("progress_content_json_0600", func(t *testing.T) {
		dir := t.TempDir()
		r := NewCardRegistry(dir)
		defer r.Stop()

		if err := r.UpdateCard("msg-1", "h", "progress content", ProgressCardStateRunning, nil); err != nil {
			t.Fatalf("UpdateCard: %v", err)
		}

		path := filepath.Join(dir, "cc-connect-progress-msg-1.json")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("progress content file mode = %o, want 0600", got)
		}
	})
}
