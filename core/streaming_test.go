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

	ok := sp.finish("Hello World Final", "")
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

	// With degraded recovery, finish attempts UpdateMessage on the degraded
	// preview. Since mockCleanerPlatform embeds mockUpdaterPlatform,
	// UpdateMessage succeeds and finish returns true (recovered).
	ok := sp.finish("Hello World Final", "")
	if !ok {
		t.Error("finish should return true when degraded recovery via UpdateMessage succeeds")
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

	ok := sp.finish("Hello World Final", "")
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

// mockStatusFooterUpdater is a keep-preview platform that also implements
// StatusFooterUpdater so we can verify the footer-applying call path.
type mockStatusFooterUpdater struct {
	mockKeepPreviewPlatform
	footerCalls []string // captured "<content>|FOOTER|<footer>" tuples
}

func (m *mockStatusFooterUpdater) UpdateMessageWithStatusFooter(_ context.Context, _ any, content, footer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.footerCalls = append(m.footerCalls, content+"|FOOTER|"+footer)
	m.messages = append(m.messages, "footer:"+content+"|"+footer)
	m.lastMsg = content
	return nil
}

// TestStreamPreview_FinishAppliesFooterEvenWhenBodyUnchanged is the regression
// test for the bug where a streamPreview finalize with a non-empty
// statusFooter was short-circuited because finalText matched lastSentText
// from the prior streaming UpdateMessage. The previous behavior dropped the
// footer entirely; the fix makes the early-return only kick in when there
// is no footer to render.
func TestStreamPreview_FinishAppliesFooterEvenWhenBodyUnchanged(t *testing.T) {
	mp := &mockStatusFooterUpdater{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 1,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("Hello World")
	time.Sleep(100 * time.Millisecond)
	// Send another chunk so lastSentViaUpdate becomes true (a real
	// UpdateMessage has been issued).
	sp.appendText(" more")
	time.Sleep(100 * time.Millisecond)

	// Now finish with the SAME body that was last streamed but with a
	// non-empty footer. The old short-circuit would skip the API call and
	// silently drop the footer.
	finalBody := "Hello World more"
	ok := sp.finish(finalBody, "model · ctx 5%\n~/path")
	if !ok {
		t.Fatal("finish should return true when footer was applied")
	}

	mp.mu.Lock()
	footerCalls := append([]string(nil), mp.footerCalls...)
	mp.mu.Unlock()

	if len(footerCalls) != 1 {
		t.Fatalf("expected exactly 1 UpdateMessageWithStatusFooter call, got %d: %#v", len(footerCalls), footerCalls)
	}
	if !strings.Contains(footerCalls[0], "model · ctx 5%") || !strings.Contains(footerCalls[0], "~/path") {
		t.Fatalf("footer call missing expected segments: %q", footerCalls[0])
	}
	if !strings.Contains(footerCalls[0], finalBody) {
		t.Fatalf("footer call missing body: %q", footerCalls[0])
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

	ok := sp.finish("Final /root/code/demo/src/app.ts:42", "")
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

// TestStreamPreview_UnfreezeResumesStreaming is the unit-level regression
// test for the bug where sp.freeze()+detachPreview() (emitted by the
// EventPermissionRequest handler in core/engine.go) permanently degraded
// the stream preview. After a permission prompt or AskUserQuestion was
// resolved, all subsequent text in the same turn was buffered until
// EventResult and sent as a single bulk message, instead of opening a new
// streaming card.
//
// The fix is sp.unfreeze() in core/streaming.go, called by the engine
// after <-pending.Resolved returns. This test exercises the unfreeze
// contract in isolation: after freeze+detach, the preview is inactive;
// after unfreeze, canPreview() returns true and the next appendText opens
// a NEW preview card containing only the post-unfreeze text.
func TestStreamPreview_UnfreezeResumesStreaming(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    50,
		MinDeltaChars: 5,
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)

	// 1. Stream the pre-interruption segment so a preview handle is live.
	sp.appendText("pre-interruption text")
	time.Sleep(100 * time.Millisecond)
	preMsgs := mp.getMessages()
	if len(preMsgs) == 0 || !strings.HasPrefix(preMsgs[len(preMsgs)-1], "start:") {
		t.Fatalf("expected an initial preview start, got %#v", preMsgs)
	}

	// 2. Simulate the engine's EventPermissionRequest path: freeze the
	// preview (committing the pre-interruption text in place) and detach
	// the handle so the next flush opens a new card.
	sp.freeze()
	sp.detachPreview()

	// 3. While frozen, canPreview() must report false. Otherwise the
	// engine's text-throttling branch would attempt updates on a degraded
	// preview and silently drop them.
	if sp.canPreview() {
		t.Fatal("canPreview() = true while frozen, want false (regression: degraded not set)")
	}

	// 4. Simulate the user resolving the permission/question and the
	// engine calling sp.unfreeze() to restore the preview.
	sp.unfreeze()

	if !sp.canPreview() {
		t.Fatal("canPreview() = false after unfreeze, want true (unfreeze did not clear degraded)")
	}

	// 5. Stream the post-interruption text. We must observe a NEW preview
	// start (proving the post-resolution text gets its own card, not
	// overwriting the frozen pre-interruption card) and the text must be
	// the post-unfreeze content only.
	sp.appendText("post-resolution chunk one two three")
	time.Sleep(100 * time.Millisecond)

	postMsgs := mp.getMessages()
	if len(postMsgs) <= len(preMsgs) {
		t.Fatalf("expected new messages after unfreeze, preMsgs=%d postMsgs=%d (%#v)",
			len(preMsgs), len(postMsgs), postMsgs)
	}

	// Find the post-unfreeze start: the first message that contains
	// "post-resolution" and was issued after the pre-interruption messages.
	var postStart string
	for _, m := range postMsgs[len(preMsgs):] {
		if strings.Contains(m, "post-resolution") {
			postStart = m
			break
		}
	}
	if postStart == "" {
		t.Fatalf("no post-unfreeze message contains 'post-resolution'; messages = %#v", postMsgs)
	}
	if !strings.HasPrefix(postStart, "start:") {
		t.Fatalf("post-unfreeze first message = %q, want a new 'start:' (regression: post text was buffered, not streamed into a fresh card)", postStart)
	}
	if strings.Contains(postStart, "pre-interruption") {
		t.Fatalf("post-unfreeze card contains pre-interruption text: %q (regression: new card is not a clean slate)", postStart)
	}
}

// TestStreamPreview_UnfreezeBypassesThrottleOnFirstChunk locks down the
// behavior that the first chunk emitted after unfreeze is NOT throttled by
// the MinDeltaChars / IntervalMs gates. Without resetting lastSentAt, a
// user who answers an AskUserQuestion would see a 1.5s blank interval
// before the agent's next text appeared — defeating the point of resuming
// streaming. The reset is the entire reason lastSentAt is zeroed in
// unfreeze().
func TestStreamPreview_UnfreezeBypassesThrottleOnFirstChunk(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    5000, // huge interval so any non-zero lastSentAt would suppress the first chunk
		MinDeltaChars: 100,  // huge delta so any non-zero lastSentAt would suppress the first chunk
		MaxChars:      500,
	}

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.appendText("pre text")
	time.Sleep(100 * time.Millisecond)

	sp.freeze()
	sp.detachPreview()
	sp.unfreeze()

	// Tiny chunk that would NEVER pass the throttle on a non-zero
	// lastSentAt. The fact that it gets through proves lastSentAt was
	// reset to zero by unfreeze().
	sp.appendText("hi")
	time.Sleep(50 * time.Millisecond)

	msgs := mp.getMessages()
	// Find the most recent message and assert it contains "hi".
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "hi") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("first post-unfreeze chunk 'hi' was throttled and never sent; messages = %#v", msgs)
	}
}

// TestStreamPreview_UnfreezeIdempotent verifies that calling unfreeze() on
// an already-active (non-degraded) preview does not crash. The engine may
// in principle never call unfreeze() in that state, but the test guards
// against future callers that might.
func TestStreamPreview_UnfreezeIdempotent(t *testing.T) {
	mp := &mockUpdaterPlatform{}
	cfg := DefaultStreamPreviewCfg()

	sp := newStreamPreview(cfg, mp, "ctx", context.Background(), nil)
	sp.unfreeze() // no-op on a never-frozen preview; must not panic

	if sp.degraded {
		t.Fatal("unfreeze on a non-degraded preview marked it degraded")
	}
	if !sp.canPreview() {
		t.Fatal("canPreview() = false after no-op unfreeze, want true")
	}
}
