//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Only local shell fixtures run. No installed OpenCode, accounts or network.
func deleteFixture(t *testing.T, body string) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "delete-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return &Agent{cmd: path, workDir: dir}, dir
}

func TestDeleteSessionCancelledContextDoesNotStart(t *testing.T) {
	a, dir := deleteFixture(t, "touch started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.DeleteSession(ctx, "test-session"); !errors.Is(err, context.Canceled) || !errors.Is(err, core.ErrSessionDeletionUncertain) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
		t.Fatal("cancelled deletion started a process")
	}
}

func TestDeleteSessionDeadlineKillsInheritedOutputChild(t *testing.T) {
	a, dir := deleteFixture(t, "(sleep 2; touch late-delete) &\nwait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := a.DeleteSession(ctx, "test-session"); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, core.ErrSessionDeletionUncertain) {
		t.Fatalf("error = %v, want deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("deletion waited without a bound")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late-delete")); !os.IsNotExist(err) {
		t.Fatal("child deletion continued after the adapter returned")
	}
}

func TestDeleteSessionWaitDelayCleansInheritedOutput(t *testing.T) {
	a, dir := deleteFixture(t, "(sleep 3; touch late-delete) &\nexit 0")
	start := time.Now()
	if err := a.DeleteSession(context.Background(), "test-session"); !errors.Is(err, core.ErrSessionDeletionUncertain) {
		t.Fatal("inherited output pipe should report incomplete process cleanup")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("inherited output was not bounded by WaitDelay")
	}
	time.Sleep(3200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late-delete")); !os.IsNotExist(err) {
		t.Fatal("inherited-output child continued deletion after return")
	}
}

func TestDeleteSessionSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		body := "[ \"$1\" = session ] && [ \"$2\" = delete ] && [ \"$3\" = test-session ] || exit 9\n"
		if fail {
			body += "echo fixture-failure >&2; exit 7"
		} else {
			body += "touch deleted"
		}
		a, dir := deleteFixture(t, body)
		err := a.DeleteSession(context.Background(), "test-session")
		if fail {
			if err == nil || !strings.Contains(err.Error(), "fixture-failure") {
				t.Fatalf("missing command failure: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		} else if _, err := os.Stat(filepath.Join(dir, "deleted")); err != nil {
			t.Fatal("returned before deletion completed")
		}
	}
}
