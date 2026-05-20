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

func TestIsOldMessage(t *testing.T) {
	if IsOldMessage(time.Now()) {
		t.Error("current time should not be considered old")
	}
	if IsOldMessage(time.Now().Add(1 * time.Minute)) {
		t.Error("future time should not be considered old")
	}
	if IsOldMessage(StartTime.Add(-10 * time.Second)) {
		t.Error("message 10s before startup should be within 5min grace period")
	}
	if !IsOldMessage(StartTime.Add(-6 * time.Minute)) {
		t.Error("message 6min before startup should be old (outside 5min grace)")
	}
	if IsOldMessage(StartTime.Add(-4 * time.Minute)) {
		t.Error("message 4min before startup should be within 5min grace period")
	}
}
