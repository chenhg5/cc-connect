package core

import (
	"path/filepath"
	"testing"
)

func TestPrivateFileLock_ExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	release, err := LockPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := LockPrivateFile(path); err == nil {
		other()
		t.Fatal("concurrent writer was allowed")
	}
	release()
	again, err := LockPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	again()
}
