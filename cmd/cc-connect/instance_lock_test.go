//go:build !windows

package main

import (
	"path/filepath"
	"testing"
)

func TestAcquireInstanceLock_Success(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")

	lock, err := AcquireInstanceLock(cfg)
	if err != nil {
		t.Fatalf("AcquireInstanceLock: %v", err)
	}
	if lock == nil || !lock.acquired {
		t.Fatal("expected acquired lock")
	}
	defer lock.Release()

	if lock.Path() == "" {
		t.Fatal("expected non-empty lock path")
	}
}

func TestAcquireInstanceLock_AlreadyLocked(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")

	first, err := AcquireInstanceLock(cfg)
	if err != nil {
		t.Fatalf("first AcquireInstanceLock: %v", err)
	}
	defer first.Release()

	_, err = AcquireInstanceLock(cfg)
	if err == nil {
		t.Fatal("second AcquireInstanceLock should fail while lock held")
	}
}
