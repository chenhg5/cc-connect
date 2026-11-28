package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubReplayPlatform is a minimal Platform implementation used to exercise
// the reply-journal replay path. It satisfies ReplyContextReconstructor so
// ReplayPendingReply can recover a reply ctx from a session key. ReplyFunc
// and ReconstructFunc are configurable so each test can pick the success /
// duplicate / failure scenario it wants to exercise.
type stubReplayPlatform struct {
	name             string
	replyCalls       atomic.Int32
	replyCtx         atomic.Value // last reply ctx passed to Reply
	replyContent     atomic.Value // last content passed to Reply
	ReplyFunc        func(ctx context.Context, replyCtx any, content string) error
	ReconstructFunc  func(sessionKey string) (any, error)
	reconstructCalls atomic.Int32
}

func (p *stubReplayPlatform) Name() string                 { return p.name }
func (p *stubReplayPlatform) Start(_ MessageHandler) error { return nil }
func (p *stubReplayPlatform) Stop() error                  { return nil }
func (p *stubReplayPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}
func (p *stubReplayPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	p.replyCalls.Add(1)
	p.replyCtx.Store(replyCtx)
	p.replyContent.Store(content)
	if p.ReplyFunc != nil {
		return p.ReplyFunc(ctx, replyCtx, content)
	}
	return nil
}
func (p *stubReplayPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.reconstructCalls.Add(1)
	if p.ReconstructFunc != nil {
		return p.ReconstructFunc(sessionKey)
	}
	return "reconstructed:" + sessionKey, nil
}

func TestHashContent_StableAndDistinguishes(t *testing.T) {
	a := HashContent("hello world")
	b := HashContent("hello world")
	if a != b {
		t.Fatalf("same content produced different hashes: %q vs %q", a, b)
	}
	c := HashContent("hello WORLD")
	if a == c {
		t.Fatalf("different content produced same hash: %q", a)
	}
	if len(a) != 64 { // SHA-256 hex
		t.Fatalf("hash length = %d, want 64", len(a))
	}
}

func TestReplyJournalPath(t *testing.T) {
	got := ReplyJournalPath("/var/lib/cc-connect")
	want := filepath.Join("/var/lib/cc-connect", "run", "last_reply.json")
	if got != want {
		t.Fatalf("ReplyJournalPath = %q, want %q", got, want)
	}
}

func TestWritePendingReply_FileMode600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode not applicable on Windows")
	}
	dir := t.TempDir()
	entry := PendingReplyEntry{
		Platform:   "feishu",
		SessionKey: "feishu:chat:user",
		Content:    "secret reply text",
	}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("WritePendingReply: %v", err)
	}
	st, err := os.Stat(ReplyJournalPath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %v, want 0o600", mode)
	}
}

func TestWritePendingReply_OverwritesPrevious(t *testing.T) {
	dir := t.TempDir()
	first := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "first"}
	if err := WritePendingReply(dir, first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := PendingReplyEntry{Platform: "feishu", SessionKey: "k2", Content: "second"}
	if err := WritePendingReply(dir, second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got := ConsumePendingReply(dir)
	if got == nil {
		t.Fatalf("ConsumePendingReply returned nil after write")
	}
	if got.Content != "second" || got.SessionKey != "k2" {
		t.Fatalf("got entry %+v, want second/k2", got)
	}
	if got.ContentHash == "" || got.ContentHash != HashContent("second") {
		t.Fatalf("ContentHash not auto-populated: %q", got.ContentHash)
	}
}

func TestWritePendingReply_EmptyDataDirErrors(t *testing.T) {
	err := WritePendingReply("", PendingReplyEntry{Content: "x"})
	if err == nil {
		t.Fatalf("expected error for empty dataDir")
	}
}

func TestClearPendingReply_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// Missing file should not error.
	if err := ClearPendingReply(dir); err != nil {
		t.Fatalf("ClearPendingReply on missing file: %v", err)
	}
	// Round-trip: write, clear, clear again.
	if err := WritePendingReply(dir, PendingReplyEntry{Platform: "feishu", Content: "x"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ClearPendingReply(dir); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	if err := ClearPendingReply(dir); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

func TestConsumePendingReply_MissingReturnsNil(t *testing.T) {
	if got := ConsumePendingReply(t.TempDir()); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestConsumePendingReply_EmptyDataDirReturnsNil(t *testing.T) {
	if got := ConsumePendingReply(""); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestConsumePendingReply_MalformedReturnsNilAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ReplyJournalPath(dir), []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ConsumePendingReply(dir); got != nil {
		t.Fatalf("expected nil for malformed entry, got %+v", got)
	}
	if _, err := os.Stat(ReplyJournalPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed after malformed consume, stat err = %v", err)
	}
}

func TestReplayPendingReply_NoEntryNoOp(t *testing.T) {
	platforms := []Platform{&stubReplayPlatform{name: "feishu"}}
	res := ReplayPendingReply(t.TempDir(), platforms)
	if res.Delivered || res.Duplicate || res.ErrorMessage != "" {
		t.Fatalf("expected no-op result, got %+v", res)
	}
}

func TestReplayPendingReply_PlatformNotFoundLeavesEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "missing", SessionKey: "k", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	platforms := []Platform{&stubReplayPlatform{name: "feishu"}}
	res := ReplayPendingReply(dir, platforms)
	if res.Delivered {
		t.Fatalf("expected non-delivered when platform missing, got %+v", res)
	}
	if !strings.Contains(res.ErrorMessage, "missing") {
		t.Fatalf("ErrorMessage %q does not name missing platform", res.ErrorMessage)
	}
	// Entry should still be on disk for retry.
	if ConsumePendingReply(dir) == nil {
		t.Fatalf("entry was not preserved when platform missing")
	}
}

func TestReplayPendingReply_SuccessClearsEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := &stubReplayPlatform{name: "feishu"}
	res := ReplayPendingReply(dir, []Platform{p})
	if !res.Delivered || res.Duplicate {
		t.Fatalf("expected delivered non-duplicate, got %+v", res)
	}
	if p.replyCalls.Load() != 1 {
		t.Fatalf("platform Reply called %d times, want 1", p.replyCalls.Load())
	}
	if got, _ := p.replyContent.Load().(string); got != "hello" {
		t.Fatalf("platform got content %q, want %q", got, "hello")
	}
	if ConsumePendingReply(dir) != nil {
		t.Fatalf("entry should be cleared after successful replay")
	}
}

func TestReplayPendingReply_DuplicateErrorTreatsAsDelivered(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := &stubReplayPlatform{
		name:      "feishu",
		ReplyFunc: func(_ context.Context, _ any, _ string) error { return errors.New("message is duplicate") },
	}
	res := ReplayPendingReply(dir, []Platform{p})
	if !res.Delivered || !res.Duplicate {
		t.Fatalf("expected delivered+duplicate, got %+v", res)
	}
	if ConsumePendingReply(dir) != nil {
		t.Fatalf("entry should be cleared when platform reports duplicate")
	}
}

func TestReplayPendingReply_GenericErrorLeavesEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := &stubReplayPlatform{
		name:      "feishu",
		ReplyFunc: func(_ context.Context, _ any, _ string) error { return errors.New("network down") },
	}
	res := ReplayPendingReply(dir, []Platform{p})
	if res.Delivered {
		t.Fatalf("did not expect delivered, got %+v", res)
	}
	if !strings.Contains(res.ErrorMessage, "network down") {
		t.Fatalf("ErrorMessage %q missing platform error", res.ErrorMessage)
	}
	if ConsumePendingReply(dir) == nil {
		t.Fatalf("entry should be preserved on non-duplicate failure")
	}
}

func TestReplayPendingReply_ReconstructFailureLeavesEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := &stubReplayPlatform{
		name:            "feishu",
		ReconstructFunc: func(string) (any, error) { return nil, errors.New("no ctx") },
	}
	res := ReplayPendingReply(dir, []Platform{p})
	if res.Delivered {
		t.Fatalf("did not expect delivered, got %+v", res)
	}
	if ConsumePendingReply(dir) == nil {
		t.Fatalf("entry should be preserved when reconstruct fails")
	}
}

// noReconstructPlatform implements Platform but NOT ReplyContextReconstructor.
// ReplayPendingReply should leave the entry in place when no platform can
// reconstruct a reply ctx.
type noReconstructPlatform struct{ name string }

func (p *noReconstructPlatform) Name() string                 { return p.name }
func (p *noReconstructPlatform) Start(_ MessageHandler) error { return nil }
func (p *noReconstructPlatform) Stop() error                  { return nil }
func (p *noReconstructPlatform) Send(_ context.Context, _ any, _ string) error {
	return nil
}
func (p *noReconstructPlatform) Reply(_ context.Context, _ any, _ string) error {
	return nil
}

func TestReplayPendingReply_NoReconstructorLeavesEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PendingReplyEntry{Platform: "feishu", SessionKey: "k1", Content: "hello"}
	if err := WritePendingReply(dir, entry); err != nil {
		t.Fatalf("write: %v", err)
	}
	res := ReplayPendingReply(dir, []Platform{&noReconstructPlatform{name: "feishu"}})
	if res.Delivered {
		t.Fatalf("did not expect delivered when platform lacks reconstructor, got %+v", res)
	}
	if !strings.Contains(res.ErrorMessage, "ReplyContextReconstructor") {
		t.Fatalf("ErrorMessage %q missing reconstructor hint", res.ErrorMessage)
	}
	if ConsumePendingReply(dir) == nil {
		t.Fatalf("entry should be preserved when platform lacks reconstructor")
	}
}

func TestIsDuplicateReplyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("something else"), false},
		{"duplicate", errors.New("message is duplicate"), true},
		{"already exists", errors.New("message already exists"), true},
		{"already sent", errors.New("already sent"), true},
		{"already delivered", errors.New("already delivered"), true},
		{"case insensitive", errors.New("DUPLICATE"), true},
		{"zh", errors.New("消息已发送，重复"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDuplicateReplyError(tc.err); got != tc.want {
				t.Fatalf("isDuplicateReplyError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestEngineReplayPendingReplyJournal_NoEntryNoError(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{dataDir: dir}
	// Should be a no-op (no panic, no log spam).
	e.replayPendingReplyJournal()
}

func TestEngineReplayPendingReplyJournal_EmptyDataDirNoop(t *testing.T) {
	e := &Engine{dataDir: ""}
	e.replayPendingReplyJournal() // must not panic
}

func TestDrainInFlightReplies_NoWaitWhenIdle(t *testing.T) {
	e := &Engine{}
	// Should return quickly when replyWG has nothing to wait on.
	done := make(chan struct{})
	go func() {
		e.drainInFlightReplies(50 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("drainInFlightReplies blocked when idle")
	}
}

func TestDrainInFlightReplies_WaitsForInFlight(t *testing.T) {
	e := &Engine{}
	e.replyWG.Add(1)
	released := make(chan struct{})
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		e.drainInFlightReplies(2 * time.Second)
		done <- time.Since(start)
	}()
	// Hold the reply for 200ms, then release.
	go func() {
		time.Sleep(200 * time.Millisecond)
		e.replyWG.Done()
		close(released)
	}()
	elapsed := <-done
	if elapsed < 200*time.Millisecond {
		t.Fatalf("drain returned in %v, expected ≥200ms (should wait for in-flight reply)", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("drain took %v, expected well under 2s timeout", elapsed)
	}
	<-released
}

func TestDrainInFlightReplies_TimesOutOnStuckReply(t *testing.T) {
	e := &Engine{}
	e.replyWG.Add(1)
	defer e.replyWG.Done() // release so test cleanup doesn't hang
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		e.drainInFlightReplies(150 * time.Millisecond)
		done <- time.Since(start)
	}()
	elapsed := <-done
	// Should give up around the 150ms timeout, not block forever.
	if elapsed < 100*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Fatalf("drain elapsed = %v, expected ~150ms (timeout)", elapsed)
	}
}

func TestEngineTrackAndSend_WritesAndClearsJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode not applicable on Windows")
	}
	dir := t.TempDir()
	e := &Engine{dataDir: dir}
	// Construct a minimal Platform stub that exposes SessionKey via the
	// SessionKeyCarrier hook so writeJournalEntry records a session key.
	stub := &sessionKeyPlatformStub{name: "feishu", sessionKey: "k1"}
	if err := e.trackAndSend(stub, "ctx", "hello", "reply"); err != nil {
		t.Fatalf("trackAndSend: %v", err)
	}
	if ConsumePendingReply(dir) != nil {
		t.Fatalf("entry should be cleared after successful send")
	}
}

func TestEngineTrackAndSend_LeavesJournalOnError(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{dataDir: dir}
	stub := &sessionKeyPlatformStub{
		name:       "feishu",
		sessionKey: "k1",
		replyErr:   errors.New("boom"),
	}
	if err := e.trackAndSend(stub, "ctx", "hello", "reply"); err == nil {
		t.Fatalf("expected error to propagate")
	}
	entry := ConsumePendingReply(dir)
	if entry == nil {
		t.Fatalf("entry should be preserved when reply fails")
	}
	if entry.SessionKey != "k1" || entry.Content != "hello" {
		t.Fatalf("entry not what we wrote: %+v", entry)
	}
}

// sessionKeyPlatformStub satisfies both Platform and SessionKeyCarrier so
// the journal can record a session key from a generic reply ctx.
type sessionKeyPlatformStub struct {
	name       string
	sessionKey string
	replyErr   error
}

func (p *sessionKeyPlatformStub) Name() string { return p.name }
func (p *sessionKeyPlatformStub) Start(_ MessageHandler) error {
	return nil
}
func (p *sessionKeyPlatformStub) Stop() error { return nil }
func (p *sessionKeyPlatformStub) Send(_ context.Context, _ any, _ string) error {
	return nil
}
func (p *sessionKeyPlatformStub) Reply(_ context.Context, _ any, _ string) error {
	return p.replyErr
}
func (p *sessionKeyPlatformStub) SessionKey(_ any) string { return p.sessionKey }
