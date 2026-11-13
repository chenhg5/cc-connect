package feishu

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func stateTestStub() *rootStub {
	return &rootStub{rootOf: func(string) (string, bool) { return "", false }}
}

func TestThreadSeenPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	opts := map[string]any{"cc_data_dir": dir, "cc_project": "persist-test"}

	p1 := newRootTestPlatform(t, stateTestStub(), opts)
	p1.markThread("omt_keep")
	if !p1.threadAllowed("omt_keep") {
		t.Fatal("thread must be allowed before restart")
	}
	if _, err := os.Stat(p1.threadStatePath); err == nil {
		t.Fatal("markThread must not write synchronously")
	}
	p1.flushThreadSeen()
	if _, err := os.Stat(p1.threadStatePath); err != nil {
		t.Fatalf("thread state file not written: %v", err)
	}

	p2 := newRootTestPlatform(t, stateTestStub(), opts)
	if !p2.threadAllowed("omt_keep") {
		t.Fatal("thread must remain allowed after restart")
	}

	// Refreshing an already-persisted thread within the refresh window must
	// stay memory-only. Check both the timer and the file itself: a synchronous
	// write regression would not leave a timer behind.
	beforeData, err := os.ReadFile(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}

	p2.markThread("omt_keep")
	p2.threadSeenMu.Lock()
	pending := p2.threadSaveTimer != nil
	p2.threadSeenMu.Unlock()
	if pending {
		t.Fatal("recently saved thread refresh must not schedule a write")
	}
	time.Sleep(50 * time.Millisecond)

	afterData, err := os.ReadFile(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeData, afterData) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("recently saved thread refresh must not write the state file")
	}

	// Positive control: an explicit flush must change the file, proving the
	// zero-write assertions above can detect a real write.
	p2.flushThreadSeen()
	flushedData, err := os.ReadFile(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}
	flushedInfo, err := os.Stat(p2.threadStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(beforeData, flushedData) && os.SameFile(beforeInfo, flushedInfo) && beforeInfo.ModTime().Equal(flushedInfo.ModTime()) {
		t.Fatal("flush did not change the state file; zero-write assertion is ineffective")
	}
}

func TestThreadSeenLoadUsesThreeDayTTL(t *testing.T) {
	dir := t.TempDir()
	opts := map[string]any{"cc_data_dir": dir, "cc_project": "ttl-test"}
	p := newRootTestPlatform(t, stateTestStub(), opts)

	now := time.Now()
	state := threadState{
		Version: threadStateVersion,
		Threads: map[string]time.Time{
			"omt_recent": now.Add(-48 * time.Hour),
			"omt_old":    now.Add(-4 * 24 * time.Hour),
			"omt_future": now.Add(24 * time.Hour),
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.threadStatePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	reloaded := newRootTestPlatform(t, stateTestStub(), opts)
	if !reloaded.threadAllowed("omt_recent") {
		t.Fatal("48h-old thread must be allowed with 3d TTL")
	}
	if reloaded.threadAllowed("omt_old") {
		t.Fatal("4d-old thread must be expired")
	}
	if reloaded.threadAllowed("omt_future") {
		t.Fatal("future timestamp must not extend the TTL")
	}
	reloaded.threadSeenMu.Lock()
	loaded := len(reloaded.threadSeen)
	reloaded.threadSeenMu.Unlock()
	if loaded != 1 {
		t.Fatalf("loaded threads = %d, want 1", loaded)
	}
}

func TestThreadSeenCorruptStateStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	opts := map[string]any{"cc_data_dir": dir, "cc_project": "corrupt-test"}
	p := newRootTestPlatform(t, stateTestStub(), opts)
	if p.threadStatePath == "" {
		t.Fatal("thread state path must be configured")
	}
	if err := os.WriteFile(p.threadStatePath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reloaded := newRootTestPlatform(t, stateTestStub(), opts)
	if reloaded.threadAllowed("omt_keep") {
		t.Fatal("corrupt state must not grant access")
	}
	reloaded.markThread("omt_keep")
	reloaded.flushThreadSeen()
	if !reloaded.threadAllowed("omt_keep") {
		t.Fatal("state must recover after the next write")
	}

	again := newRootTestPlatform(t, stateTestStub(), opts)
	if !again.threadAllowed("omt_keep") {
		t.Fatal("recovered state must persist")
	}
}

func TestPruneThreadSeenCapsOldestEntries(t *testing.T) {
	now := time.Now()
	seen := make(map[string]time.Time, threadSeenMax+5)
	for i := 0; i < threadSeenMax+5; i++ {
		seen["omt_"+time.Unix(int64(i), 0).Format("150405")+"_"+string(rune('a'+i%26))] = now.Add(time.Duration(i) * time.Second)
	}
	pruneThreadSeen(seen, now.Add(10*time.Minute))
	if len(seen) != threadSeenMax {
		t.Fatalf("cache size = %d, want %d", len(seen), threadSeenMax)
	}
}

func TestThreadSeenStatePathRequiresDataDir(t *testing.T) {
	if got := threadSeenStatePath("feishu", map[string]any{}); got != "" {
		t.Fatalf("path = %q, want empty without data dir", got)
	}
	got := threadSeenStatePath("feishu", map[string]any{
		"cc_data_dir": t.TempDir(),
		"cc_project":  "../bad/name",
	})
	if got == "" {
		t.Fatal("expected a configured path")
	}
}
