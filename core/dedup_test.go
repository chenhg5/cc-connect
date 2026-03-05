package core

import "testing"

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
