package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/daemon"
)

// fakeServiceManager records the lifecycle calls made against it so the tests
// below can assert ordering rather than just that each call happened.
type fakeServiceManager struct {
	calls    []string
	stopErr  error
	startErr error
}

func (f *fakeServiceManager) Stop() error {
	f.calls = append(f.calls, "stop")
	return f.stopErr
}

func (f *fakeServiceManager) Start() error {
	f.calls = append(f.calls, "start")
	return f.startErr
}

// recordReap returns a reap func that appends to the same call log.
func recordReap(calls *[]string, killed bool) func() bool {
	return func() bool {
		*calls = append(*calls, "reap")
		return killed
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}
}

// The reap must land after Stop. On Windows the wrapper powershell.exe loops on
// a 10s sleep and relaunches the binary, so a child killed while the wrapper is
// still alive comes straight back.
func TestStopService_ReapsAfterStop(t *testing.T) {
	mgr := &fakeServiceManager{}
	if err := stopService(mgr, recordReap(&mgr.calls, true)); err != nil {
		t.Fatalf("stopService: %v", err)
	}
	assertCalls(t, mgr.calls, []string{"stop", "reap"})
}

// A failed Stop still leaves whatever it failed to kill behind, so the reap has
// to run regardless — and the error must still reach the caller.
func TestStopService_ReapsEvenWhenStopFails(t *testing.T) {
	stopErr := errors.New("stop timed out")
	mgr := &fakeServiceManager{stopErr: stopErr}

	err := stopService(mgr, recordReap(&mgr.calls, true))

	assertCalls(t, mgr.calls, []string{"stop", "reap"})
	if !errors.Is(err, stopErr) {
		t.Fatalf("err = %v, want %v", err, stopErr)
	}
}

// Starting with a leftover process still holding the instance lock is the exact
// silent-failure mode: the new process exits 1 on stderr and nothing reaches the
// log. So the reap has to come first.
func TestStartService_ReapsBeforeStart(t *testing.T) {
	mgr := &fakeServiceManager{}
	if err := startService(mgr, recordReap(&mgr.calls, true)); err != nil {
		t.Fatalf("startService: %v", err)
	}
	assertCalls(t, mgr.calls, []string{"reap", "start"})
}

// Regression test for `daemon restart` silently keeping the old process: the
// restart path must stop, then reap, then start — and reap only once.
func TestRestartService_ReapsBetweenStopAndStart(t *testing.T) {
	mgr := &fakeServiceManager{}
	if err := restartService(mgr, recordReap(&mgr.calls, true)); err != nil {
		t.Fatalf("restartService: %v", err)
	}
	assertCalls(t, mgr.calls, []string{"stop", "reap", "start"})
}

// A restart must still bring the service back up when stopping it reported an
// error; that is what the old mgr.Restart() path did.
func TestRestartService_StartsEvenWhenStopFails(t *testing.T) {
	mgr := &fakeServiceManager{stopErr: errors.New("stop timed out")}
	if err := restartService(mgr, recordReap(&mgr.calls, false)); err != nil {
		t.Fatalf("restartService: %v", err)
	}
	assertCalls(t, mgr.calls, []string{"stop", "reap", "start"})
}

// A nil reap must be tolerated rather than panicking on the call.
func TestStopAndStartService_NilReap(t *testing.T) {
	mgr := &fakeServiceManager{}
	if err := stopService(mgr, nil); err != nil {
		t.Fatalf("stopService: %v", err)
	}
	if err := startService(mgr, nil); err != nil {
		t.Fatalf("startService: %v", err)
	}
	assertCalls(t, mgr.calls, []string{"stop", "start"})
}

// An empty config path means there is no lock file to consult; it must report
// "nothing killed" instead of guessing at a path.
func TestReapLeftover_EmptyPathIsNoOp(t *testing.T) {
	if reapLeftover("") {
		t.Fatal("reapLeftover(\"\") should not report a kill")
	}
}

// reapLeftoverFromMeta resolves the config path out of the daemon metadata. With
// metadata absent there is nothing to reap.
func TestReapLeftoverFromMeta_NoMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows os.UserHomeDir reads USERPROFILE

	if reapLeftoverFromMeta() {
		t.Fatal("reapLeftoverFromMeta should be a no-op without daemon.json")
	}
}

// With metadata present the config path is derived, but an instance lock naming
// no live PID must still be a no-op. The lock file is left empty, which is what
// InstanceLock.Release leaves behind, so no process is ever signalled.
func TestReapLeftoverFromMeta_EmptyLockIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows os.UserHomeDir reads USERPROFILE

	dataDir := filepath.Join(home, ".cc-connect")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	// json.Marshal rather than hand-rolled quoting: a Windows temp path contains
	// backslashes, which must be escaped to stay valid JSON. A metadata file that
	// failed to parse would make this test pass for the wrong reason.
	meta, err := json.Marshal(daemon.Meta{WorkDir: dataDir})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.json"), meta, 0o644); err != nil {
		t.Fatalf("write daemon.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".config.toml.lock"), nil, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if reapLeftoverFromMeta() {
		t.Fatal("reapLeftoverFromMeta should not kill anything for a lock with no PID")
	}
}
