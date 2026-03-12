package core

import (
	"testing"
	"time"
)

func TestWorkspacePool_GetOrCreate(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)

	state1 := pool.GetOrCreate("/workspace/a")
	state2 := pool.GetOrCreate("/workspace/a")
	state3 := pool.GetOrCreate("/workspace/b")

	if state1 != state2 {
		t.Error("expected same state for same workspace")
	}
	if state1 == state3 {
		t.Error("expected different state for different workspace")
	}
}

func TestWorkspacePool_Touch(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)
	state := pool.GetOrCreate("/workspace/a")

	before := state.LastActivity()
	time.Sleep(10 * time.Millisecond)
	state.Touch()
	after := state.LastActivity()

	if !after.After(before) {
		t.Error("expected lastActivity to advance after Touch()")
	}
}

func TestWorkspacePool_ReapIdle(t *testing.T) {
	pool := newWorkspacePool(50 * time.Millisecond)
	pool.GetOrCreate("/workspace/a")

	time.Sleep(100 * time.Millisecond)
	reaped := pool.ReapIdle()

	if len(reaped) != 1 || reaped[0] != "/workspace/a" {
		t.Errorf("expected [/workspace/a] reaped, got %v", reaped)
	}

	if s := pool.Get("/workspace/a"); s != nil {
		t.Error("expected workspace removed after reap")
	}
}

func TestWorkspacePool_ReapIdle_KeepsActive(t *testing.T) {
	pool := newWorkspacePool(200 * time.Millisecond)
	state := pool.GetOrCreate("/workspace/active")

	time.Sleep(100 * time.Millisecond)
	state.Touch() // Keep it alive

	reaped := pool.ReapIdle()
	if len(reaped) != 0 {
		t.Errorf("expected no reaping for active workspace, got %v", reaped)
	}

	if s := pool.Get("/workspace/active"); s == nil {
		t.Error("expected active workspace to still exist")
	}
}
