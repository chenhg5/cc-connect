package core

import (
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

func TestNewMessageDedup_ConfigurableWindow(t *testing.T) {
	d := NewMessageDedup(20 * time.Millisecond)
	if d.IsDuplicate("m1") {
		t.Fatal("first call should not be a duplicate")
	}
	if !d.IsDuplicate("m1") {
		t.Fatal("second call within window should be a duplicate")
	}
	time.Sleep(30 * time.Millisecond)
	if d.IsDuplicate("m1") {
		t.Fatal("after window expiry the same id should be accepted again")
	}
}

func TestNewMessageDedup_DefaultOnZero(t *testing.T) {
	d := NewMessageDedup(0)
	if d.ttl != dedupTTL {
		t.Errorf("expected default TTL %v, got %v", dedupTTL, d.ttl)
	}
}

func TestNewMessageDedup_DefaultOnNegative(t *testing.T) {
	d := NewMessageDedup(-5 * time.Second)
	if d.ttl != dedupTTL {
		t.Errorf("expected default TTL %v on negative input, got %v", dedupTTL, d.ttl)
	}
}

func TestMessageDedup_ZeroValueStillUsesDefaultTTL(t *testing.T) {
	// Backward-compat: every platform that embeds `core.MessageDedup{}` must
	// continue to work with the original 60s window. First call primes,
	// second call inside the window must be flagged duplicate.
	var d MessageDedup
	if d.IsDuplicate("z1") {
		t.Fatal("first call should not be a duplicate")
	}
	if !d.IsDuplicate("z1") {
		t.Fatal("second call within window should be a duplicate")
	}
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
