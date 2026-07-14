package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatHistoryAppendWritesEntryWithHeader(t *testing.T) {
	ws := t.TempDir()
	w := NewChatHistoryWriter()
	ts := time.Date(2026, 7, 14, 15, 4, 0, 0, time.UTC)

	w.Append(ws, "Jay", ts, "hello there")

	data, err := os.ReadFile(filepath.Join(ws, chatHistoryFileName))
	if err != nil {
		t.Fatalf("read chat_history: %v", err)
	}
	got := string(data)
	if !strings.HasPrefix(got, chatHistoryHeader) {
		t.Errorf("expected read-only header prefix, got:\n%s", got)
	}
	if !strings.Contains(got, "## 2026-07-14 15:04 — Jay\nhello there\n") {
		t.Errorf("entry not formatted as expected, got:\n%s", got)
	}
}

func TestChatHistoryAppendIsolatesByWorkspace(t *testing.T) {
	wsA := t.TempDir()
	wsB := t.TempDir()
	w := NewChatHistoryWriter()
	ts := time.Now()

	w.Append(wsA, "Jay", ts, "message for topic A")
	w.Append(wsB, "architect", ts, "message for topic B")

	a, err := os.ReadFile(filepath.Join(wsA, chatHistoryFileName))
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(wsB, chatHistoryFileName))
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if strings.Contains(string(a), "topic B") {
		t.Errorf("topic A file leaked topic B content:\n%s", a)
	}
	if strings.Contains(string(b), "topic A") {
		t.Errorf("topic B file leaked topic A content:\n%s", b)
	}
}

func TestChatHistoryAppendSkipsMissingWorkspace(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-created-yet")
	w := NewChatHistoryWriter()

	w.Append(missing, "Jay", time.Now(), "should not persist")

	// The workspace dir must NOT be created, and no file must exist.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("workspace dir was created or stat error changed: err=%v", err)
	}
}

func TestChatHistoryAppendIgnoresEmptyInputs(t *testing.T) {
	ws := t.TempDir()
	w := NewChatHistoryWriter()
	ts := time.Now()

	w.Append("", "Jay", ts, "no workspace")
	w.Append(ws, "", ts, "no speaker")
	w.Append(ws, "Jay", ts, "   ")

	if _, err := os.Stat(filepath.Join(ws, chatHistoryFileName)); !os.IsNotExist(err) {
		t.Errorf("no-op inputs should not create a file, err=%v", err)
	}
}

func TestChatHistoryAppendConcurrentDoesNotCorrupt(t *testing.T) {
	ws := t.TempDir()
	w := NewChatHistoryWriter()
	ts := time.Now()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Append(ws, "Jay", ts, "line-body")
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(ws, chatHistoryFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Header written exactly once; one entry per concurrent append.
	if c := strings.Count(string(data), chatHistoryHeader); c != 1 {
		t.Errorf("header count = %d, want 1", c)
	}
	if c := strings.Count(string(data), "line-body"); c != n {
		t.Errorf("body count = %d, want %d", c, n)
	}
}
