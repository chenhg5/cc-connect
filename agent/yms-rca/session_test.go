package ymsagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// mockEncoder captures writes done via writeFrame in tests.
type mockEncoder struct {
	mu      sync.Mutex
	frames  []map[string]any
	block   bool // when true Encode blocks until release
	release chan struct{}
	calls   atomic.Int32
}

func (m *mockEncoder) Encode(v any) error {
	m.calls.Add(1)
	if m.block {
		<-m.release
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var frame map[string]any
	_ = json.Unmarshal(b, &frame)
	m.mu.Lock()
	m.frames = append(m.frames, frame)
	m.mu.Unlock()
	return nil
}

func (m *mockEncoder) framesCopy() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(m.frames))
	copy(out, m.frames)
	return out
}

// newTestSession returns a session that does NOT spawn a subprocess.
func newTestSession(t *testing.T, mode string) (*session, *mockEncoder) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	enc := &mockEncoder{}
	s := &session{
		mode:           mode,
		cfg:            &Agent{confirmTimeout: 100 * time.Millisecond},
		ctx:            ctx,
		cancel:         cancel,
		events:         make(chan core.Event, 64),
		pendingConfirm: make(map[string]*pendingPermission),
		seenToolUse:    make(map[string]struct{}),
		seenToolDone:   make(map[string]struct{}),
	}
	s.alive.Store(true)
	s.sessionID.Store("")
	// Hook encoder via a tiny adapter so writeFrame uses it.
	s.encMock = enc
	t.Cleanup(func() {
		// best-effort cancel
		cancel()
	})
	return s, enc
}

// drainOnce waits up to d for one event matching the filter.
func waitFor(t *testing.T, s *session, d time.Duration, pred func(core.Event) bool) (core.Event, bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case evt, ok := <-s.events:
			if !ok {
				return core.Event{}, false
			}
			if pred(evt) {
				return evt, true
			}
		case <-deadline:
			return core.Event{}, false
		}
	}
}

func TestHandleConfirm_Default(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.handleConfirmRequest("req-1", "rm -rf", "really?")

	evt, ok := waitFor(t, s, 1*time.Second, func(e core.Event) bool {
		return e.Type == core.EventPermissionRequest
	})
	if !ok {
		t.Fatal("did not receive EventPermissionRequest")
	}
	if evt.RequestID != "req-1" || evt.ToolName != "rm -rf" {
		t.Fatalf("event mismatch: %+v", evt)
	}

	// Now allow it.
	if err := s.RespondPermission("req-1", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	frames := enc.framesCopy()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame written, got %d: %+v", len(frames), frames)
	}
	if frames[0]["confirmed"] != true || frames[0]["id"] != "req-1" {
		t.Fatalf("unexpected frame: %+v", frames[0])
	}
}

func TestHandleConfirm_Yolo_AutoApprove(t *testing.T) {
	s, enc := newTestSession(t, "yolo")
	s.handleConfirmRequest("req-y", "rm", "msg")

	frames := enc.framesCopy()
	if len(frames) != 1 || frames[0]["confirmed"] != true {
		t.Fatalf("yolo should auto-approve; frames=%+v", frames)
	}
	// Audit message must name the actual active mode.
	var auditMsg string
	select {
	case evt := <-s.events:
		if evt.Type == core.EventPermissionRequest {
			t.Fatalf("yolo emitted EventPermissionRequest")
		}
		auditMsg = evt.Content
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected audit EventThinking")
	}
	if !strings.Contains(auditMsg, "(yolo)") {
		t.Errorf("audit text wrong: %q", auditMsg)
	}
}

// Regression for code-review LOW: bypassPermissions previously printed
// "auto-approved (yolo)" in the audit trail — the message must reflect
// the actual active mode.
func TestHandleConfirm_BypassPermissions_AuditLabel(t *testing.T) {
	s, _ := newTestSession(t, "bypassPermissions")
	s.handleConfirmRequest("req-b", "rm", "msg")

	evt, ok := waitFor(t, s, 200*time.Millisecond, func(e core.Event) bool {
		return e.Type == core.EventThinking
	})
	if !ok {
		t.Fatal("no audit EventThinking")
	}
	if !strings.Contains(evt.Content, "(bypassPermissions)") {
		t.Errorf("audit should say bypassPermissions, got %q", evt.Content)
	}
	if strings.Contains(evt.Content, "(yolo)") {
		t.Errorf("audit mislabeled as yolo: %q", evt.Content)
	}
}

func TestHandleConfirm_DontAsk_AutoDeny(t *testing.T) {
	s, enc := newTestSession(t, "dontAsk")
	s.handleConfirmRequest("req-d", "rm", "msg")

	frames := enc.framesCopy()
	if len(frames) != 1 || frames[0]["confirmed"] != false {
		t.Fatalf("dontAsk should auto-deny; frames=%+v", frames)
	}
}

func TestRespondPermission_UnknownID(t *testing.T) {
	s, _ := newTestSession(t, "default")
	err := s.RespondPermission("does-not-exist", core.PermissionResult{Behavior: "allow"})
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestConfirmTimeout_AutoDeny(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.cfg.confirmTimeout = 50 * time.Millisecond
	s.handleConfirmRequest("req-t", "rm", "msg")

	// wait for timeout to fire
	time.Sleep(150 * time.Millisecond)

	// Pending should be cleared.
	s.confirmMu.Lock()
	left := len(s.pendingConfirm)
	s.confirmMu.Unlock()
	if left != 0 {
		t.Fatalf("expected pending cleared, %d remain", left)
	}

	frames := enc.framesCopy()
	foundDeny := false
	for _, f := range frames {
		if f["id"] == "req-t" && f["confirmed"] == false {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Fatalf("expected timeout deny frame, got: %+v", frames)
	}
}

func TestExtensionUI_Select_Cancelled(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.handleExtensionUIRequest(map[string]any{
		"type": "extension_ui_request", "id": "sel-1", "method": "select",
	})
	frames := enc.framesCopy()
	if len(frames) != 1 || frames[0]["cancelled"] != true {
		t.Fatalf("expected cancelled:true; got %+v", frames)
	}
}

func TestSetLiveMode_ResolvesPending(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.handleConfirmRequest("p1", "rm", "msg1")
	s.handleConfirmRequest("p2", "rm", "msg2")

	// Drain the two EventPermissionRequest before switching mode so they
	// don't crowd the channel.
	_, _ = waitFor(t, s, 200*time.Millisecond, func(e core.Event) bool { return e.Type == core.EventPermissionRequest })
	_, _ = waitFor(t, s, 200*time.Millisecond, func(e core.Event) bool { return e.Type == core.EventPermissionRequest })

	if !s.SetLiveMode("yolo") {
		t.Fatal("SetLiveMode(yolo) = false")
	}
	// Both pending should now have confirmed:true frames.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		frames := enc.framesCopy()
		approved := 0
		for _, f := range frames {
			if f["confirmed"] == true {
				approved++
			}
		}
		if approved >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 approvals after SetLiveMode; got: %+v", frames)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSend_RejectsClosedSession(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.alive.Store(false)
	if err := s.Send("hi", nil, nil); err == nil {
		t.Fatal("Send on closed session should error")
	}
}

func TestSend_BusyRejection(t *testing.T) {
	s, _ := newTestSession(t, "default")
	// First Send should succeed (no real subprocess, encoder writes synchronously).
	s.workDir = t.TempDir()
	if err := s.Send("first", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Second Send should be rejected because busy is still set.
	if err := s.Send("second", nil, nil); err == nil {
		t.Fatal("second Send should fail (busy)")
	}
}

func TestClose_Idempotent(t *testing.T) {
	s, _ := newTestSession(t, "default")
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close must NOT panic on a re-closed channel; must return same err.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestClose_ParallelSafe(t *testing.T) {
	s, _ := newTestSession(t, "default")
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
}

func TestClose_DeniesPendingAndEmitsReason(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.handleConfirmRequest("pc", "rm", "msg")
	// drain the EventPermissionRequest
	_, _ = waitFor(t, s, 200*time.Millisecond, func(e core.Event) bool { return e.Type == core.EventPermissionRequest })

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Drain remaining events; expect EventThinking with "auto-denied" reason.
	foundReason := false
	for evt := range s.events {
		if evt.Type == core.EventThinking && strings.Contains(evt.Content, "auto-denied") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("expected auto-denied EventThinking after Close")
	}
	// Frame must be confirmed:false.
	frames := enc.framesCopy()
	foundDeny := false
	for _, f := range frames {
		if f["id"] == "pc" && f["confirmed"] == false {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Errorf("expected deny frame on Close; got %+v", frames)
	}
}

func TestClose_FailClosed_PreEmptsAllow(t *testing.T) {
	// Race: Close() vs RespondPermission(allow). Whichever wins should be
	// the single response written; the other must observe sync.Once already
	// taken and silently drop.
	for i := 0; i < 30; i++ {
		s, enc := newTestSession(t, "default")
		s.handleConfirmRequest("rr", "rm", "msg")
		_, _ = waitFor(t, s, 100*time.Millisecond, func(e core.Event) bool { return e.Type == core.EventPermissionRequest })

		var allowErr error
		done := make(chan struct{}, 2)
		go func() {
			allowErr = s.RespondPermission("rr", core.PermissionResult{Behavior: "allow"})
			done <- struct{}{}
		}()
		go func() {
			_ = s.Close()
			done <- struct{}{}
		}()
		<-done
		<-done

		// Count responses for id=rr.
		frames := enc.framesCopy()
		count := 0
		for _, f := range frames {
			if f["id"] == "rr" && (f["confirmed"] == true || f["confirmed"] == false) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("iter=%d: expected exactly 1 response for rr, got %d: %+v",
				i, count, frames)
		}
		// allowErr may or may not be set depending on who won.
		_ = allowErr
		// Drain any events.
		for range s.events {
		}
	}
}

// writeFrame must encode using s.encMock when set (test path); otherwise
// fall through to the real encoder. The helper below is a tiny shim added
// in writeFrame_test_shim.go.
var _ = errors.New

// ── attachments / saveSessionFiles ────────────────────────

func TestSaveSessionFiles_UUIDPrefixAndIsolation(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.workDir = t.TempDir()

	files := []core.FileAttachment{
		{FileName: "report.pdf", Data: []byte("pdf-bytes")},
	}
	paths, err := s.saveSessionFiles("sess-1", files)
	if err != nil {
		t.Fatalf("saveSessionFiles: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths: %+v", paths)
	}
	// Path layout: <workDir>/.cc-connect/attachments/sess-1/<uuid>-report.pdf
	base := paths[0]
	if !strings.Contains(base, "/sess-1/") {
		t.Errorf("missing session dir in path: %s", base)
	}
	if !strings.HasSuffix(base, "-report.pdf") {
		t.Errorf("missing -report.pdf suffix: %s", base)
	}
	// UUID prefix should be hex (16 chars) followed by '-'.
	prefix := strings.TrimSuffix(filepath.Base(base), "-report.pdf")
	if len(prefix) != 16 {
		t.Errorf("uuid prefix len = %d, want 16: %q", len(prefix), prefix)
	}
}

func TestSaveSessionFiles_PathTraversalSanitized(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.workDir = t.TempDir()

	// Attempt path traversal — saveSessionFiles must strip it.
	files := []core.FileAttachment{
		{FileName: "../../etc/passwd", Data: []byte("malicious")},
	}
	paths, err := s.saveSessionFiles("sess-x", files)
	if err != nil {
		t.Fatalf("saveSessionFiles: %v", err)
	}
	got := paths[0]
	if !strings.HasSuffix(got, "-passwd") {
		t.Errorf("traversal not sanitized: %s", got)
	}
	// File must reside under the session attachment dir, not escape via "..".
	want := filepath.Join(s.workDir, ".cc-connect", "attachments", "sess-x")
	abs, _ := filepath.Abs(got)
	if !strings.HasPrefix(abs, want) {
		t.Errorf("file escaped session dir: abs=%s want_prefix=%s", abs, want)
	}
}

func TestSaveSessionFiles_CleansPreviousTurn(t *testing.T) {
	s, _ := newTestSession(t, "default")
	s.workDir = t.TempDir()

	first := []core.FileAttachment{{FileName: "a.txt", Data: []byte("1")}}
	if _, err := s.saveSessionFiles("sess-c", first); err != nil {
		t.Fatal(err)
	}
	// Confirm one file exists.
	dir := filepath.Join(s.workDir, ".cc-connect", "attachments", "sess-c")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("first turn: %d files", len(entries))
	}

	// Second turn with a different file — old file must be removed.
	second := []core.FileAttachment{{FileName: "b.txt", Data: []byte("2")}}
	if _, err := s.saveSessionFiles("sess-c", second); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("second turn: %d files, want 1 (previous turn should be cleaned)", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), "-b.txt") {
		t.Errorf("expected b.txt after cleanup, got %q", entries[0].Name())
	}
}

func TestSend_ImageBase64InFrame(t *testing.T) {
	s, enc := newTestSession(t, "default")
	s.workDir = t.TempDir()
	imgs := []core.ImageAttachment{
		{MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
	}
	if err := s.Send("hello", imgs, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	frames := enc.framesCopy()
	if len(frames) != 1 {
		t.Fatalf("expected 1 prompt frame, got %d", len(frames))
	}
	arr, ok := frames[0]["images"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("missing images field: %+v", frames[0])
	}
	img := arr[0].(map[string]any)
	if img["mimeType"] != "image/png" {
		t.Errorf("mimeType: %v", img)
	}
	if img["data"] != "iVBORw==" { // base64("\x89PNG"[:4])
		t.Errorf("base64 wrong: %v", img["data"])
	}
}

// ── Close: writeFrame永久阻塞场景 (§G4) ─────────────────────

// blockingEncoder hangs forever inside Encode until release is closed.
type blockingEncoder struct {
	release chan struct{}
	calls   int
}

func (b *blockingEncoder) Encode(_ any) error {
	b.calls++
	<-b.release // never returns until test closes the channel
	return nil
}

func TestClose_WriteFramePermanentlyBlocked(t *testing.T) {
	s, _ := newTestSession(t, "default")
	// Swap the encoder for one that never returns.
	stuck := &blockingEncoder{release: make(chan struct{})}
	s.encMock = stuck

	// Register a pending so cleanup actually runs through writeFrame.
	s.handleConfirmRequest("blocked", "rm", "msg")
	_, _ = waitFor(t, s, 200*time.Millisecond, func(e core.Event) bool {
		return e.Type == core.EventPermissionRequest
	})

	// Close must return within ~3s even though writeFrame is wedged.
	done := make(chan struct{})
	go func() {
		_ = s.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s while writeFrame was wedged")
	}

	// events channel must be closed (drainable to completion, no panic).
	for evt := range s.events {
		_ = evt
	}
	// Unblock the stuck goroutine so it does not leak the test runner.
	close(stuck.release)
}

func TestSessionAliveAndEventsAccessor(t *testing.T) {
	s, _ := newTestSession(t, "default")
	if !s.Alive() {
		t.Error("session should be alive at construction")
	}
	if s.Events() == nil {
		t.Error("Events() returned nil")
	}
	s.alive.Store(false)
	if s.Alive() {
		t.Error("Alive() did not reflect store")
	}
}
