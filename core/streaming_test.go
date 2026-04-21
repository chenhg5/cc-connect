package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockUpdaterPlatform implements Platform + MessageUpdater + PreviewStarter.
type mockUpdaterPlatform struct {
	stubPlatformEngine
	mu       sync.Mutex
	messages []string // track all sent/updated messages
	lastMsg  string
}

func (m *mockUpdaterPlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, "start:"+content)
	m.lastMsg = content
	return "preview-handle", nil
}

func (m *mockUpdaterPlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, "update:"+content)
	m.lastMsg = content
	return nil
}

func (m *mockUpdaterPlatform) getMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.messages))
	copy(out, m.messages)
	return out
}

func TestStreamPreview_BasicFlow(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    100,
		MinDeltaChars: 5,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)

	if !sp.canPreview() {
		t.Fatal("should be able to preview")
	}

	sp.appendText("Hello ")
	time.Sleep(150 * time.Millisecond)

	msgs := mp.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message sent")
	}
	if msgs[0] != "start:Hello " {
		t.Errorf("first message = %q, want 'start:Hello '", msgs[0])
	}
}

func TestStreamPreview_ThrottlesUpdates(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    200,
		MinDeltaChars: 5,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)

	// Rapid-fire small appends
	for i := 0; i < 10; i++ {
		sp.appendText("ab")
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for throttle timers to fire
	time.Sleep(300 * time.Millisecond)

	msgs := mp.getMessages()
	// Should NOT have 10 individual updates; throttling should batch them
	if len(msgs) >= 10 {
		t.Errorf("expected throttling to reduce updates, got %d", len(msgs))
	}
	if len(msgs) == 0 {
		t.Error("expected at least one update")
	}
}

func TestStreamPreview_MaxChars(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      10,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("This is a very long text that exceeds max chars limit")
	time.Sleep(100 * time.Millisecond)

	msgs := mp.getMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	// Last message should be truncated
	for _, m := range msgs {
		if len(m) > 0 {
			// Content after "start:" or "update:" should respect maxChars
			content := m
			for _, prefix := range []string{"start:", "update:"} {
				if len(content) > len(prefix) && content[:len(prefix)] == prefix {
					content = content[len(prefix):]
				}
			}
			if len([]rune(content)) > 15 { // 10 chars + "…" with some margin
				t.Errorf("message too long: %q (%d runes)", content, len([]rune(content)))
			}
		}
	}
}

func TestStreamPreview_Disabled(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{Enabled: false}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	if sp.canPreview() {
		t.Error("should not be able to preview when disabled")
	}

	sp.appendText("Hello")
	time.Sleep(50 * time.Millisecond)

	msgs := mp.getMessages()
	if len(msgs) != 0 {
		t.Error("no messages should be sent when disabled")
	}
}

func TestStreamPreview_FinishInPlace(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)

	ok := sp.finish("Hello World Final")
	if !ok {
		t.Error("finish should return true when preview was active")
	}

	msgs := mp.getMessages()
	last := msgs[len(msgs)-1]
	if last != "update:Hello World Final" {
		t.Errorf("last message = %q, want 'update:Hello World Final'", last)
	}
}

// mockCleanerPlatform adds PreviewCleaner to mockUpdaterPlatform.
type mockCleanerPlatform struct {
	mockUpdaterPlatform
	deleted []any
}

func (m *mockCleanerPlatform) DeletePreviewMessage(_ context.Context, handle any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, handle)
	return nil
}

type mockKeepPreviewPlatform struct {
	mockCleanerPlatform
}

func (m *mockKeepPreviewPlatform) KeepPreviewOnFinish() bool {
	return true
}

func TestStreamPreview_FreezeDeletesOnFinish(t *testing.T) {
	mp := &mockCleanerPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)

	// Simulate a tool/thinking event → freeze
	sp.freeze()

	// finish should return false (degraded) and delete the stale preview
	ok := sp.finish("Hello World Final")
	if ok {
		t.Error("finish should return false when degraded")
	}

	mp.mu.Lock()
	deletedCount := len(mp.deleted)
	mp.mu.Unlock()
	if deletedCount != 1 {
		t.Errorf("expected 1 delete call, got %d", deletedCount)
	}
}

func TestStreamPreview_NonUpdaterPlatform(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	cfg := DefaultStreamPreviewCfg()

	sp := newStreamPreview(cfg, p, "ctx", context.Background(), nil)
	if sp.canPreview() {
		t.Error("should not preview on non-updater platform")
	}
}

func TestStreamPreview_DiscardDeletesPreview(t *testing.T) {
	mp := &mockCleanerPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)

	sp.discard()

	mp.mu.Lock()
	deletedCount := len(mp.deleted)
	msgs := append([]string(nil), mp.messages...)
	mp.mu.Unlock()

	if deletedCount != 1 {
		t.Fatalf("expected 1 delete call, got %d", deletedCount)
	}
	if len(msgs) != 1 || msgs[0] != "start:Hello World" {
		t.Fatalf("messages = %#v, want only initial preview", msgs)
	}
}

func TestStreamPreview_FinishKeepsPreviewWhenPlatformPrefersInPlaceFinalize(t *testing.T) {
	mp := &mockKeepPreviewPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)

	ok := sp.finish("Hello World Final")
	if !ok {
		t.Fatal("finish should return true when platform prefers in-place finalize")
	}

	mp.mu.Lock()
	deletedCount := len(mp.deleted)
	msgs := append([]string(nil), mp.messages...)
	mp.mu.Unlock()

	if deletedCount != 0 {
		t.Fatalf("expected no delete call, got %d", deletedCount)
	}
	if len(msgs) < 2 || msgs[len(msgs)-1] != "update:Hello World Final" {
		t.Fatalf("messages = %#v, want final update in place", msgs)
	}
}

func TestStreamPreview_NeedsDoneReaction_TrueAfterUpdate(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)

	if sp.needsDoneReaction() {
		t.Error("needsDoneReaction should be false before any send")
	}

	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)

	if sp.needsDoneReaction() {
		t.Error("needsDoneReaction should be false after only SendPreviewStart (no UpdateMessage yet)")
	}

	sp.appendText(" more text to trigger update")
	time.Sleep(100 * time.Millisecond)

	msgs := mp.getMessages()
	hasUpdate := false
	for _, m := range msgs {
		if len(m) > 7 && m[:7] == "update:" {
			hasUpdate = true
			break
		}
	}
	if !hasUpdate {
		t.Fatal("expected at least one UpdateMessage call")
	}

	if !sp.needsDoneReaction() {
		t.Error("needsDoneReaction should be true after UpdateMessage was used")
	}
}

func TestStreamPreview_NeedsDoneReaction_FalseAfterDiscard(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)
	sp.appendText(" more text")
	time.Sleep(100 * time.Millisecond)

	sp.discard()

	if sp.needsDoneReaction() {
		t.Error("needsDoneReaction should be false after discard (previewMsgID cleared)")
	}
}

func TestStreamPreview_NeedsDoneReaction_FalseWhenDisabled(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{Enabled: false}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello")
	time.Sleep(100 * time.Millisecond)

	if sp.needsDoneReaction() {
		t.Error("needsDoneReaction should be false when preview is disabled")
	}
}

func TestStreamPreview_AppliesTransform(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), func(s string) string {
		return strings.ReplaceAll(s, "/root/code/demo/src/app.ts:42", "📄 `src/app.ts:42`")
	})
	sp.appendText("See /root/code/demo/src/app.ts:42")
	time.Sleep(100 * time.Millisecond)

	ok := sp.finish("Final /root/code/demo/src/app.ts:42")
	if !ok {
		t.Fatal("finish should succeed when preview is active")
	}

	msgs := mp.getMessages()
	if len(msgs) < 2 {
		t.Fatalf("messages = %#v, want preview start and final update", msgs)
	}
	if got := msgs[0]; got != "start:See 📄 `src/app.ts:42`" {
		t.Fatalf("start message = %q, want transformed preview start", got)
	}
	if got := msgs[len(msgs)-1]; got != "update:Final 📄 `src/app.ts:42`" {
		t.Fatalf("final message = %q, want transformed final preview", got)
	}
}
