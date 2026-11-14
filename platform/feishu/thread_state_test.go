package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
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

func TestP2PMessageDoesNotWriteThreadState(t *testing.T) {
	dir := t.TempDir()
	p := newRootTestPlatform(t, stateTestStub(), map[string]any{
		"cc_data_dir": dir,
		"cc_project":  "p2p-test",
	})
	p.botOpenID = "ou_bot"

	handled := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg.SessionKey
	}
	ev := rootTestEvent("om_p2p", "", "", "私聊消息")
	ev.Event.Message.ChatType = stringPtr("p2p")
	if err := p.onMessage(context.Background(), ev); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("p2p message was not dispatched")
	}

	p.threadSeenMu.Lock()
	count := len(p.threadSeen)
	pending := p.threadSaveTimer != nil
	p.threadSeenMu.Unlock()
	if count != 0 || pending {
		t.Fatalf("p2p message must not touch thread state: entries=%d timer=%v", count, pending)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(p.threadStatePath); err == nil {
		t.Fatal("p2p message must not write the thread state file")
	}
}

func TestCreateMessageMarksOnlyThreadID(t *testing.T) {
	p := newRootTestPlatform(t, stateTestStub())
	if err := p.createMessage(context.Background(), "oc_test", "text", `{"text":"hi"}`, "test"); err != nil {
		t.Fatalf("createMessage() error = %v", err)
	}

	p.threadSeenMu.Lock()
	_, messageMarked := p.threadSeen["om_created"]
	_, threadMarked := p.threadSeen["omt_new"]
	p.threadSeenMu.Unlock()
	if messageMarked {
		t.Fatal("messageID must not be recorded as a thread")
	}
	if !threadMarked {
		t.Fatal("returned threadID must be recorded")
	}
}

func TestLargeGroupMentionDoesNotMarkRootAsThread(t *testing.T) {
	dir := t.TempDir()
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_card" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub, map[string]any{
		"cc_data_dir": dir,
		"cc_project":  "large-group-test",
	})
	p.botOpenID = "ou_bot"
	handled := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg.SessionKey
	}

	if err := p.onMessage(context.Background(), rootTestEvent("om_q", "om_card", "", "@bot 看下")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("mentioned group message was not dispatched")
	}

	p.threadSeenMu.Lock()
	count := len(p.threadSeen)
	pending := p.threadSaveTimer != nil
	p.threadSeenMu.Unlock()
	if count != 0 || pending {
		t.Fatalf("large-group root id must not enter thread state: entries=%d timer=%v", count, pending)
	}
}

func TestTopicMessageMarksRealThreadID(t *testing.T) {
	dir := t.TempDir()
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_card" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub, map[string]any{
		"cc_data_dir": dir,
		"cc_project":  "topic-test",
	})
	p.botOpenID = "ou_bot"
	handled := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg.SessionKey
	}

	if err := p.onMessage(context.Background(), rootTestEvent("om_topic_msg", "om_card", "omt_topic", "@bot 看下")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("topic message was not dispatched")
	}

	p.threadSeenMu.Lock()
	_, threadMarked := p.threadSeen["omt_topic"]
	_, rootMarked := p.threadSeen["om_card"]
	p.threadSeenMu.Unlock()
	if !threadMarked {
		t.Fatal("real threadID must be recorded")
	}
	if rootMarked {
		t.Fatal("rootID must not be recorded as a thread")
	}
	p.flushThreadSeen()
}

func TestStartCronTopicMarksReturnedThreadID(t *testing.T) {
	p := newRootTestPlatform(t, stateTestStub())
	p.botOpenID = "ou_bot"

	key, rc, err := p.StartCronTopic(context.Background(), "feishu:oc_test:root:om_old", "定时任务")
	if err != nil {
		t.Fatalf("StartCronTopic() error = %v", err)
	}
	if key != "feishu:oc_test:root:om_created" {
		t.Fatalf("session key = %q", key)
	}
	if rc == nil {
		t.Fatal("reply context must be returned")
	}

	p.threadSeenMu.Lock()
	_, threadMarked := p.threadSeen["omt_new"]
	_, messageMarked := p.threadSeen["om_created"]
	p.threadSeenMu.Unlock()
	if !threadMarked {
		t.Fatal("cron new_topic must mark the returned threadID")
	}
	if messageMarked {
		t.Fatal("cron new_topic must not mark messageID as a thread")
	}
}

// Feishu returns no thread_id on the create response for a new topic root, and
// this chat is not a topic chat: the topic is created by the first reply.
// StartCronTopic must not wait for, or query, a thread id at all.
func TestStartCronTopicDoesNotLookUpThreadID(t *testing.T) {
	stub := &rootStub{
		omitCreateThreadID: true,
		rootOf:             func(string) (string, bool) { return "", true },
	}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	key, rawRC, err := p.StartCronTopic(context.Background(), "feishu:oc_test:root:om_old", "定时任务")
	if err != nil {
		t.Fatalf("StartCronTopic() error = %v", err)
	}
	if key != "feishu:oc_test:root:om_created" {
		t.Fatalf("session key = %q", key)
	}
	rc, ok := rawRC.(replyContext)
	if !ok || !rc.forceThread {
		t.Fatal("cron reply context must force a thread so the first reply opens the topic")
	}
	if n := atomic.LoadInt32(&stub.calls); n != 0 {
		t.Fatalf("message lookups = %d, want 0: a non-topic chat has no thread id to read", n)
	}

	p.threadSeenMu.Lock()
	entries := len(p.threadSeen)
	p.threadSeenMu.Unlock()
	if entries != 0 {
		t.Fatalf("thread state entries = %d, want 0", entries)
	}
}

// The first reply is what opens the topic, and its response is the only
// reliable source of the thread id.
func TestReplyMessageRegistersThreadFromResponse(t *testing.T) {
	stub := &rootStub{
		rootOf:        func(string) (string, bool) { return "", true },
		replyThreadID: "omt_reply",
	}
	p := newRootTestPlatform(t, stub)

	rc := replyContext{messageID: "om_root", chatID: "oc_test", sessionKey: "feishu:oc_test:root:om_root", forceThread: true}
	if err := p.replyMessage(context.Background(), rc, larkim.MsgTypeText, `{"text":"hi"}`); err != nil {
		t.Fatalf("replyMessage() error = %v", err)
	}
	if !p.threadAllowed("omt_reply") {
		t.Fatal("thread from the reply response must be registered for mention state")
	}
}

func TestReplyMessageWithoutThreadIDRegistersNothing(t *testing.T) {
	p := newRootTestPlatform(t, &rootStub{rootOf: func(string) (string, bool) { return "", true }})

	rc := replyContext{messageID: "om_root", chatID: "oc_test", sessionKey: "feishu:oc_test:root:om_root"}
	if err := p.replyMessage(context.Background(), rc, larkim.MsgTypeText, `{"text":"hi"}`); err != nil {
		t.Fatalf("replyMessage() error = %v", err)
	}
	p.threadSeenMu.Lock()
	entries := len(p.threadSeen)
	p.threadSeenMu.Unlock()
	if entries != 0 {
		t.Fatalf("thread state entries = %d, want 0", entries)
	}
}

// An unmentioned message in a topic that never got registered is dropped.
func TestUnmentionedTopicMessageDroppedWhenNotRegistered(t *testing.T) {
	p := newRootTestPlatform(t, &rootStub{rootOf: func(string) (string, bool) { return "", true }}, map[string]any{
		"cc_data_dir": t.TempDir(),
		"cc_project":  "no-adopt-test",
	})
	p.botOpenID = "ou_bot"
	handled := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { handled <- msg.SessionKey }

	if err := p.onMessage(context.Background(), rootTestEventUnmentioned("om_u1", "om_root", "omt_chat", "在吗")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case <-handled:
		t.Fatal("unregistered topic must not be adopted")
	case <-time.After(200 * time.Millisecond):
	}

	p.threadSeenMu.Lock()
	entries := len(p.threadSeen)
	p.threadSeenMu.Unlock()
	if entries != 0 {
		t.Fatalf("topic must not be registered, got %d entries", entries)
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
	evicted := pruneThreadSeen(seen, now.Add(10*time.Minute))
	if len(seen) != threadSeenMax {
		t.Fatalf("cache size = %d, want %d", len(seen), threadSeenMax)
	}
	if evicted != 5 {
		t.Fatalf("evicted = %d, want 5", evicted)
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
