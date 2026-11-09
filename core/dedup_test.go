package core

import (
	"sync"
	"testing"
	"time"
)

func TestMessageDedup_Basic(t *testing.T) {
	var d MessageDedup
	if d.IsDuplicate("msg-1") {
		t.Error("first call should not be duplicate")
	}
	if !d.IsDuplicate("msg-1") {
		t.Error("second call should be duplicate")
	}
	if d.IsDuplicate("msg-2") {
		t.Error("different ID should not be duplicate")
	}
}

func TestMessageDedup_EmptyID(t *testing.T) {
	var d MessageDedup
	if d.IsDuplicate("") {
		t.Error("empty ID should never be duplicate")
	}
	if d.IsDuplicate("") {
		t.Error("empty ID should never be duplicate on second call")
	}
}

func TestMessageDedup_Concurrent(t *testing.T) {
	var d MessageDedup
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(id string) {
			d.IsDuplicate(id)
			done <- struct{}{}
		}("msg-" + string(rune('a'+i%26)))
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}

// TestMessageDedup_NilReceiver covers the nil-safety path: a nil *MessageDedup
// must treat every ID as new (the dispatcher may carry an uninitialised cache
// before factory init runs).
func TestMessageDedup_NilReceiver(t *testing.T) {
	var d *MessageDedup
	if d.IsDuplicate("anything") {
		t.Error("nil receiver should never report duplicate")
	}
	if d.TTL() != 0 {
		t.Errorf("nil TTL() = %v, want 0", d.TTL())
	}
}

// TestMessageDedup_ConfigurableTTL exercises the new configurable window
// (issue #1667). A fresh cache with TTL=20ms should let an ID re-appear
// after the window elapses.
func TestMessageDedup_ConfigurableTTL(t *testing.T) {
	d := NewMessageDedup(20 * time.Millisecond)
	if got := d.TTL(); got != 20*time.Millisecond {
		t.Fatalf("TTL() = %v, want 20ms", got)
	}
	if d.IsDuplicate("k") {
		t.Fatal("first call should not be duplicate")
	}
	if !d.IsDuplicate("k") {
		t.Fatal("second call within window should be duplicate")
	}
	time.Sleep(40 * time.Millisecond)
	if d.IsDuplicate("k") {
		t.Fatal("after window elapsed, ID should be accepted again")
	}
}

// TestMessageDedup_DefaultTTLForZero ensures callers that pass ttl<=0 fall
// back to DefaultDedupTTL instead of dividing by zero or panicking.
func TestMessageDedup_DefaultTTLForZero(t *testing.T) {
	d := NewMessageDedup(0)
	if got := d.TTL(); got != DefaultDedupTTL {
		t.Fatalf("TTL() = %v, want DefaultDedupTTL %v", got, DefaultDedupTTL)
	}
	d2 := NewMessageDedup(-1 * time.Second)
	if got := d2.TTL(); got != DefaultDedupTTL {
		t.Fatalf("negative TTL fallback = %v, want DefaultDedupTTL", got)
	}
}

// TestMessageDedup_ConcurrentConfigurable stress-tests the configurable-TTL
// path so race detector catches any future regression in the cleanup loop.
func TestMessageDedup_ConcurrentConfigurable(t *testing.T) {
	d := NewMessageDedup(50 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				d.IsDuplicate("k")
			}
		}(i)
	}
	wg.Wait()
}

func TestIsOldMessage(t *testing.T) {
	if IsOldMessage(time.Now()) {
		t.Error("current time should not be considered old")
	}
	if IsOldMessage(time.Now().Add(1 * time.Minute)) {
		t.Error("future time should not be considered old")
	}
	if !IsOldMessage(StartTime.Add(-10 * time.Second)) {
		t.Error("message 10s before startup should be old")
	}
	if IsOldMessage(StartTime.Add(-1 * time.Second)) {
		t.Error("message 1s before startup should be within grace period")
	}
}
