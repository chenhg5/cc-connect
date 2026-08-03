package grok

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsTaskkillCommand_KillsDescendantTree(t *testing.T) {
	name, args := windowsTaskkillCommand(1234)
	if name != "taskkill" {
		t.Fatalf("command = %q, want taskkill", name)
	}
	want := []string{"/T", "/F", "/PID", "1234"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestKillWindowsProcessTree_PrimarySuccessSkipsFallback(t *testing.T) {
	fallbackCalled := false
	err := killWindowsProcessTree(1234, func(name string, args ...string) error {
		return nil
	}, func() error {
		fallbackCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("killWindowsProcessTree: %v", err)
	}
	if fallbackCalled {
		t.Fatal("fallback called after successful taskkill")
	}
}

func TestKillWindowsProcessTree_TaskkillFailureFallsBack(t *testing.T) {
	taskkillErr := errors.New("taskkill unavailable")
	fallbackCalled := false
	err := killWindowsProcessTree(1234, func(name string, args ...string) error {
		return taskkillErr
	}, func() error {
		fallbackCalled = true
		return nil
	})
	if !errors.Is(err, taskkillErr) {
		t.Fatalf("error = %v, want taskkill error after partial cleanup", err)
	}
	if !fallbackCalled {
		t.Fatal("fallback was not called after taskkill failure")
	}
}

func TestKillWindowsProcessTree_ReportsBothFailures(t *testing.T) {
	taskkillErr := errors.New("taskkill unavailable")
	fallbackErr := errors.New("process kill failed")
	err := killWindowsProcessTree(1234, func(name string, args ...string) error {
		return taskkillErr
	}, func() error {
		return fallbackErr
	})
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("error = %v, want fallback error", err)
	}
	if !errors.Is(err, taskkillErr) || !strings.Contains(err.Error(), taskkillErr.Error()) {
		t.Fatalf("error = %v, want taskkill error", err)
	}
}
