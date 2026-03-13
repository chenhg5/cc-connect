package core

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewChatLog_DefaultSize(t *testing.T) {
	cl := NewChatLog(0)
	if cl.maxSize != 500 {
		t.Errorf("expected default maxSize=500, got %d", cl.maxSize)
	}
}

func TestNewChatLog_NegativeSize(t *testing.T) {
	cl := NewChatLog(-10)
	if cl.maxSize != 500 {
		t.Errorf("expected default maxSize=500 for negative input, got %d", cl.maxSize)
	}
}

func TestNewChatLog_CustomSize(t *testing.T) {
	cl := NewChatLog(100)
	if cl.maxSize != 100 {
		t.Errorf("expected maxSize=100, got %d", cl.maxSize)
	}
}

func TestChatLog_RecordAndGetRecent(t *testing.T) {
	cl := NewChatLog(10)
	now := time.Now()

	for i := 0; i < 5; i++ {
		cl.Record("chat1", ChatLogEntry{
			UserID:    fmt.Sprintf("user%d", i),
			UserName:  fmt.Sprintf("User %d", i),
			Content:   fmt.Sprintf("message %d", i),
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	entries := cl.GetRecent("chat1", 3)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Should return the last 3 entries
	if entries[0].Content != "message 2" {
		t.Errorf("expected first entry 'message 2', got %q", entries[0].Content)
	}
	if entries[2].Content != "message 4" {
		t.Errorf("expected last entry 'message 4', got %q", entries[2].Content)
	}
}

func TestChatLog_GetRecentAll(t *testing.T) {
	cl := NewChatLog(10)
	cl.Record("chat1", ChatLogEntry{Content: "a"})
	cl.Record("chat1", ChatLogEntry{Content: "b"})

	// n=0 should return all
	entries := cl.GetRecent("chat1", 0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for n=0, got %d", len(entries))
	}

	// n > len should return all
	entries = cl.GetRecent("chat1", 100)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for n=100, got %d", len(entries))
	}
}

func TestChatLog_GetRecentEmpty(t *testing.T) {
	cl := NewChatLog(10)
	entries := cl.GetRecent("nonexistent", 10)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for nonexistent chat, got %d", len(entries))
	}
}

func TestChatLog_RingBufferEviction(t *testing.T) {
	cl := NewChatLog(3)

	for i := 0; i < 5; i++ {
		cl.Record("chat1", ChatLogEntry{
			Content:   fmt.Sprintf("msg%d", i),
			Timestamp: time.Now(),
		})
	}

	entries := cl.GetRecent("chat1", 0)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries after eviction, got %d", len(entries))
	}
	// Should contain the last 3 messages
	if entries[0].Content != "msg2" {
		t.Errorf("expected first entry 'msg2', got %q", entries[0].Content)
	}
	if entries[2].Content != "msg4" {
		t.Errorf("expected last entry 'msg4', got %q", entries[2].Content)
	}
}

func TestChatLog_GetSince(t *testing.T) {
	cl := NewChatLog(100)
	base := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		cl.Record("chat1", ChatLogEntry{
			Content:   fmt.Sprintf("msg%d", i),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		})
	}

	// Get messages since minute 7 (should get msg7, msg8, msg9)
	since := base.Add(7 * time.Minute)
	entries := cl.GetSince("chat1", since)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries since minute 7, got %d", len(entries))
	}
	if entries[0].Content != "msg7" {
		t.Errorf("expected first entry 'msg7', got %q", entries[0].Content)
	}
}

func TestChatLog_GetSinceEmpty(t *testing.T) {
	cl := NewChatLog(10)
	entries := cl.GetSince("nonexistent", time.Now())
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for nonexistent chat, got %d", len(entries))
	}
}

func TestChatLog_GetSinceFuture(t *testing.T) {
	cl := NewChatLog(10)
	cl.Record("chat1", ChatLogEntry{Content: "old", Timestamp: time.Now()})

	entries := cl.GetSince("chat1", time.Now().Add(time.Hour))
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for future since, got %d", len(entries))
	}
}

func TestChatLog_Clear(t *testing.T) {
	cl := NewChatLog(100)
	cl.Record("chat1", ChatLogEntry{Content: "msg1"})
	cl.Record("chat1", ChatLogEntry{Content: "msg2"})
	cl.Record("chat2", ChatLogEntry{Content: "other"})

	cl.Clear("chat1")

	if entries := cl.GetRecent("chat1", 0); len(entries) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(entries))
	}
	// chat2 should be unaffected
	if entries := cl.GetRecent("chat2", 0); len(entries) != 1 {
		t.Errorf("expected chat2 to still have 1 entry, got %d", len(entries))
	}
}

func TestChatLog_IsolatedChats(t *testing.T) {
	cl := NewChatLog(10)
	cl.Record("chat1", ChatLogEntry{Content: "hello from chat1"})
	cl.Record("chat2", ChatLogEntry{Content: "hello from chat2"})

	e1 := cl.GetRecent("chat1", 0)
	e2 := cl.GetRecent("chat2", 0)

	if len(e1) != 1 || e1[0].Content != "hello from chat1" {
		t.Errorf("chat1 entries unexpected: %v", e1)
	}
	if len(e2) != 1 || e2[0].Content != "hello from chat2" {
		t.Errorf("chat2 entries unexpected: %v", e2)
	}
}

func TestChatLog_GetRecentReturnsCopy(t *testing.T) {
	cl := NewChatLog(10)
	cl.Record("chat1", ChatLogEntry{Content: "original"})

	entries := cl.GetRecent("chat1", 0)
	entries[0].Content = "modified"

	original := cl.GetRecent("chat1", 0)
	if original[0].Content != "original" {
		t.Error("GetRecent should return a copy, but original was modified")
	}
}

func TestChatLog_ConcurrentAccess(t *testing.T) {
	cl := NewChatLog(100)
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cl.Record("chat1", ChatLogEntry{
					UserID:    fmt.Sprintf("user%d", id),
					Content:   fmt.Sprintf("msg-%d-%d", id, j),
					Timestamp: time.Now(),
				})
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cl.GetRecent("chat1", 10)
				cl.GetSince("chat1", time.Now().Add(-time.Minute))
			}
		}()
	}

	wg.Wait()
	// No race/panic = pass
}
