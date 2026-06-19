package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type suppressTestPlatform struct {
	style string
}

func (s *suppressTestPlatform) Name() string                             { return "test" }
func (s *suppressTestPlatform) Start(MessageHandler) error               { return nil }
func (s *suppressTestPlatform) Reply(context.Context, any, string) error { return nil }
func (s *suppressTestPlatform) Send(context.Context, any, string) error  { return nil }
func (s *suppressTestPlatform) Stop() error                              { return nil }
func (s *suppressTestPlatform) ProgressStyle() string                    { return s.style }

func TestSuppressStandaloneToolResultEvent(t *testing.T) {
	if SuppressStandaloneToolResultEvent(&stubPlatformNoProgress{}) {
		t.Fatal("platform without ProgressStyleProvider should not suppress")
	}
	if !SuppressStandaloneToolResultEvent(&suppressTestPlatform{style: "legacy"}) {
		t.Fatal("legacy ProgressStyleProvider should suppress standalone tool results")
	}
	if SuppressStandaloneToolResultEvent(&suppressTestPlatform{style: "compact"}) {
		t.Fatal("compact should not suppress (writer absorbs tool results)")
	}
	if SuppressStandaloneToolResultEvent(&suppressTestPlatform{style: "card"}) {
		t.Fatal("card should not suppress")
	}
}

// stubPlatformNoProgress is a minimal Platform without ProgressStyleProvider.
type stubPlatformNoProgress struct{}

func (stubPlatformNoProgress) Name() string                             { return "plain" }
func (stubPlatformNoProgress) Start(MessageHandler) error               { return nil }
func (stubPlatformNoProgress) Reply(context.Context, any, string) error { return nil }
func (stubPlatformNoProgress) Send(context.Context, any, string) error  { return nil }
func (stubPlatformNoProgress) Stop() error                              { return nil }

type progressHintReplyCtx struct {
	style   string
	payload bool
}

func (r progressHintReplyCtx) progressStyleHint() string { return r.style }

func (r progressHintReplyCtx) supportsProgressCardPayloadHint() bool { return r.payload }

type previewCapturePlatform struct {
	started []string
	updated []string
}

func (p *previewCapturePlatform) Name() string                             { return "bridge" }
func (p *previewCapturePlatform) Start(MessageHandler) error               { return nil }
func (p *previewCapturePlatform) Reply(context.Context, any, string) error { return nil }
func (p *previewCapturePlatform) Send(context.Context, any, string) error  { return nil }
func (p *previewCapturePlatform) Stop() error                              { return nil }

func (p *previewCapturePlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.started = append(p.started, content)
	return "preview-1", nil
}

func (p *previewCapturePlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	p.updated = append(p.updated, content)
	return nil
}

func TestBuildAndParseProgressCardPayload(t *testing.T) {
	payload := BuildProgressCardPayload([]string{" step1 ", "", "step2"}, true)
	if payload == "" {
		t.Fatal("BuildProgressCardPayload returned empty string")
	}
	if !strings.HasPrefix(payload, ProgressCardPayloadPrefix) {
		t.Fatalf("payload = %q, want prefix %q", payload, ProgressCardPayloadPrefix)
	}

	parsed, ok := ParseProgressCardPayload(payload)
	if !ok {
		t.Fatalf("ParseProgressCardPayload should succeed, payload=%q", payload)
	}
	if len(parsed.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(parsed.Entries))
	}
	if parsed.Entries[0] != "step1" || parsed.Entries[1] != "step2" {
		t.Fatalf("entries = %#v, want [step1 step2]", parsed.Entries)
	}
	if !parsed.Truncated {
		t.Fatal("parsed.Truncated = false, want true")
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(parsed.Items))
	}
	if parsed.Items[0].Kind != ProgressEntryInfo || parsed.Items[0].Text != "step1" {
		t.Fatalf("items[0] = %#v, want info/step1", parsed.Items[0])
	}
}

func TestCompactProgressWriter_UsesReplyContextHints(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}

	registry.StartTicker(p, 50*time.Millisecond)

	w := newCompactProgressWriter(context.Background(), p, replyCtx, "codex", LangEnglish, nil, registry)
	if !w.enabled {
		t.Fatal("progress writer should be enabled")
	}
	if !w.usePayload {
		t.Fatal("progress writer should use payload when reply context advertises it")
	}
	if got := w.style; got != progressStyleCard {
		t.Fatalf("style = %q, want %q", got, progressStyleCard)
	}

	if !w.AppendEvent(ProgressEntryThinking, "planning bridge progress", "", "planning bridge progress") {
		t.Fatal("AppendEvent() = false, want true")
	}
	if len(p.started) != 1 {
		t.Fatalf("started = %d, want 1", len(p.started))
	}
	if !strings.HasPrefix(p.started[0], ProgressCardPayloadPrefix) {
		t.Fatalf("preview start payload = %q, want progress payload prefix", p.started[0])
	}

	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize() = false, want true")
	}

	time.Sleep(200 * time.Millisecond)
	if len(p.updated) != 1 {
		t.Fatalf("updated = %d, want 1", len(p.updated))
	}

	parsed, ok := ParseProgressCardPayload(p.updated[0])
	if !ok {
		t.Fatalf("ParseProgressCardPayload() failed for %q", p.updated[0])
	}
	if parsed.State != ProgressCardStateCompleted {
		t.Fatalf("state = %q, want %q", parsed.State, ProgressCardStateCompleted)
	}
}

func TestBuildAndParseProgressCardPayloadV2(t *testing.T) {
	payload := BuildProgressCardPayloadV2([]ProgressCardEntry{
		{Kind: ProgressEntryThinking, Text: " plan "},
		{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"},
	}, false, "Codex", LangChinese, ProgressCardStateRunning)
	if payload == "" {
		t.Fatal("BuildProgressCardPayloadV2 returned empty string")
	}

	parsed, ok := ParseProgressCardPayload(payload)
	if !ok {
		t.Fatalf("ParseProgressCardPayload should succeed, payload=%q", payload)
	}
	if parsed.Version != 2 {
		t.Fatalf("version = %d, want 2", parsed.Version)
	}
	if parsed.Agent != "Codex" {
		t.Fatalf("agent = %q, want Codex", parsed.Agent)
	}
	if parsed.Lang != string(LangChinese) {
		t.Fatalf("lang = %q, want %q", parsed.Lang, LangChinese)
	}
	if parsed.State != ProgressCardStateRunning {
		t.Fatalf("state = %q, want %q", parsed.State, ProgressCardStateRunning)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(parsed.Items))
	}
	if parsed.Items[1].Kind != ProgressEntryToolUse || parsed.Items[1].Tool != "Bash" {
		t.Fatalf("items[1] = %#v, want tool_use/Bash", parsed.Items[1])
	}
}

func TestParseProgressCardPayloadRejectsInvalid(t *testing.T) {
	if _, ok := ParseProgressCardPayload("plain text"); ok {
		t.Fatal("expected parse failure for plain text")
	}
	if _, ok := ParseProgressCardPayload(ProgressCardPayloadPrefix + "{not-json"); ok {
		t.Fatal("expected parse failure for invalid json")
	}
	if _, ok := ParseProgressCardPayload(ProgressCardPayloadPrefix + `{"entries":[]}`); ok {
		t.Fatal("expected parse failure for empty entries")
	}
}

func TestCompactProgressWriter_AppliesTransformToCardPayloadEntries(t *testing.T) {
	p := &stubCompactProgressPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		style:              "card",
		supportPayload:     true,
	}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, func(s string) string {
		return strings.ReplaceAll(s, "/root/code/demo/src/app.ts:42", "📄 `src/app.ts:42`")
	}, nil)

	if ok := w.AppendStructured(ProgressCardEntry{
		Kind: ProgressEntryThinking,
		Text: "Inspect /root/code/demo/src/app.ts:42",
	}, "Inspect /root/code/demo/src/app.ts:42"); !ok {
		t.Fatal("AppendStructured() = false, want true")
	}

	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	payload, ok := ParseProgressCardPayload(starts[0])
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", starts[0])
	}
	if len(payload.Items) != 1 {
		t.Fatalf("payload items = %d, want 1", len(payload.Items))
	}
	if got := payload.Items[0].Text; got != "Inspect 📄 `src/app.ts:42`" {
		t.Fatalf("payload item text = %q, want transformed text", got)
	}
}

func TestCompactProgressWriter_DoesNotTransformToolResults(t *testing.T) {
	p := &stubCompactProgressPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		style:              "card",
		supportPayload:     true,
	}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, func(s string) string {
		return strings.ReplaceAll(s, "/root/code/demo/src/app.ts:42", "📄 `src/app.ts:42`")
	}, nil)

	raw := "/root/code/demo/src/app.ts:42"
	if ok := w.AppendStructured(ProgressCardEntry{
		Kind: ProgressEntryToolResult,
		Text: raw,
	}, raw); !ok {
		t.Fatal("AppendStructured() = false, want true")
	}

	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	payload, ok := ParseProgressCardPayload(starts[0])
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", starts[0])
	}
	if got := payload.Items[0].Text; got != raw {
		t.Fatalf("tool result text = %q, want raw %q", got, raw)
	}
}

type stubIdentifiableHandle struct{ id string }

func (h *stubIdentifiableHandle) MessageID() string { return h.id }

type previewCapturePlatformWithIdentifiableHandle struct {
	previewCapturePlatform
}

func (p *previewCapturePlatformWithIdentifiableHandle) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.previewCapturePlatform.SendPreviewStart(context.Background(), nil, content)
	return &stubIdentifiableHandle{id: "msg_xxx"}, nil
}

func TestAppendStructuredUsesRegistryAfterStart(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	entry1 := ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}
	if ok := w.AppendStructured(entry1, "step1"); !ok {
		t.Fatal("first AppendStructured() = false, want true")
	}
	if len(p.started) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(p.started))
	}

	entry2 := ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	if ok := w.AppendStructured(entry2, "Tool: Bash\npwd"); !ok {
		t.Fatal("second AppendStructured() = false, want true")
	}

	// Direct PATCH must not be used for the second update.
	if len(p.updated) != 0 {
		t.Fatalf("direct UpdateMessage calls = %d, want 0; updates should go through registry", len(p.updated))
	}

	card := registry.lookup("msg_xxx")
	if card == nil {
		t.Fatal("card not registered in registry")
	}

	parsed, ok := ParseProgressCardPayload(card.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", card.content)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("registry payload items = %d, want 2", len(parsed.Items))
	}
	if parsed.Items[1].Kind != ProgressEntryToolUse || parsed.Items[1].Tool != "Bash" || parsed.Items[1].Text != "pwd" {
		t.Fatalf("registry payload item[1] = %#v, want tool_use/Bash/pwd", parsed.Items[1])
	}
}

func TestAppendStructuredFallbackWithoutIdentifier(t *testing.T) {
	p := &previewCapturePlatform{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	entry1 := ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}
	if ok := w.AppendStructured(entry1, "step1"); !ok {
		t.Fatal("first AppendStructured() = false, want true")
	}

	entry2 := ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	if ok := w.AppendStructured(entry2, "Tool: Bash\npwd"); !ok {
		t.Fatal("second AppendStructured() = false, want true")
	}

	// Handle has no message ID, so registry must not be used.
	if registry.lookup("msg_xxx") != nil {
		t.Fatal("registry should not contain a card when handle lacks an identifier")
	}
	// Direct PATCH should have been used instead.
	if len(p.updated) != 1 {
		t.Fatalf("direct UpdateMessage calls = %d, want 1", len(p.updated))
	}
}

func TestFinalizeWritesRegistry(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	if !w.AppendEvent(ProgressEntryThinking, "planning", "", "planning") {
		t.Fatal("AppendEvent() = false, want true")
	}

	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize() = false, want true")
	}

	time.Sleep(200 * time.Millisecond)

	card := registry.lookup("msg_xxx")
	if card == nil {
		t.Fatal("card not found in registry")
	}
	if !card.finalized {
		t.Fatal("finalized = false, want true")
	}
	if card.state != ProgressCardStateCompleted {
		t.Fatalf("state = %q, want %q", card.state, ProgressCardStateCompleted)
	}

	parsed, ok := ParseProgressCardPayload(card.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", card.content)
	}
	if parsed.State != ProgressCardStateCompleted {
		t.Fatalf("payload state = %q, want %q", parsed.State, ProgressCardStateCompleted)
	}

	// Non-final updates after finalization must be rejected.
	if err := registry.UpdateCard("msg_xxx", card.handle, "stale", ProgressCardStateRunning, w.cardUpdater); err == nil {
		t.Fatal("expected registry to reject non-final update after finalize")
	}
}

func TestFinalizeDoesNotPatchImmediately(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	if !w.AppendEvent(ProgressEntryThinking, "planning", "", "planning") {
		t.Fatal("AppendEvent() = false, want true")
	}

	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize() = false, want true")
	}

	// Before the registry ticker fires, the platform should not have received a
	// direct PATCH.
	time.Sleep(20 * time.Millisecond)
	if len(p.updated) != 0 {
		t.Fatalf("direct UpdateMessage calls = %d, want 0; final state should be flushed by registry ticker", len(p.updated))
	}
}

func TestAppendStructuredRegistersHandle(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	entry1 := ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}
	if ok := w.AppendStructured(entry1, "step1"); !ok {
		t.Fatal("first AppendStructured() = false, want true")
	}

	entry2 := ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	if ok := w.AppendStructured(entry2, "Tool: Bash\npwd"); !ok {
		t.Fatal("second AppendStructured() = false, want true")
	}

	card := registry.lookup("msg_xxx")
	if card == nil {
		t.Fatal("handle was not registered in cardRegistry")
	}
	if card.handle == nil {
		t.Fatal("registered card has nil handle")
	}
	ident, ok := card.handle.(MessageHandleIdentifier)
	if !ok {
		t.Fatal("registered handle does not implement MessageHandleIdentifier")
	}
	if got := ident.MessageID(); got != "msg_xxx" {
		t.Fatalf("registered message ID = %q, want msg_xxx", got)
	}

	parsed, ok := ParseProgressCardPayload(card.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", card.content)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("registered payload items = %d, want 2", len(parsed.Items))
	}
	if parsed.Items[1].Kind != ProgressEntryToolUse || parsed.Items[1].Tool != "Bash" {
		t.Fatalf("registered payload item[1] = %#v, want tool_use/Bash", parsed.Items[1])
	}
}

func TestRegisterHandleIdempotent(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}
	w := newCompactProgressWriter(context.Background(), p, replyCtx, "Agent", LangEnglish, nil, registry)

	handle := &stubIdentifiableHandle{id: "msg_yyy"}
	if !w.registerHandle(handle, "first") {
		t.Fatal("first registerHandle() = false, want true")
	}
	if !w.registerHandle(handle, "second") {
		t.Fatal("second registerHandle() = false, want true")
	}

	card := registry.lookup("msg_yyy")
	if card == nil {
		t.Fatal("card not found after repeated registration")
	}
	if card.content != "second" {
		t.Fatalf("card content = %q, want second", card.content)
	}
	if card.handle != handle {
		t.Fatal("card handle was not updated by idempotent registration")
	}
}

func TestNewCompactProgressWriterWithRegistry(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}

	t.Run("stores provided registry", func(t *testing.T) {
		registry := NewCardRegistry(t.TempDir())
		defer registry.Stop()

		w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: progressStyleCompact}, "codex", LangEnglish, nil, registry)
		if w == nil {
			t.Fatal("newCompactProgressWriter returned nil")
		}
		if w.registry != registry {
			t.Fatalf("writer.registry = %p, want %p", w.registry, registry)
		}
		if w.cardUpdater == nil {
			t.Fatal("writer.cardUpdater = nil, want messageUpdaterCardAdapter instance")
		}
	})

	t.Run("nil registry keeps direct update fallback", func(t *testing.T) {
		registry := NewCardRegistry(t.TempDir())
		defer registry.Stop()

		w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: progressStyleCompact}, "codex", LangEnglish, nil, nil)
		if w == nil {
			t.Fatal("newCompactProgressWriter(nil) returned nil")
		}
		if w.registry != nil {
			t.Fatalf("writer.registry = %p, want nil", w.registry)
		}
		if w.cardUpdater != nil {
			t.Fatalf("writer.cardUpdater = %v, want nil when no registry", w.cardUpdater)
		}

		// AppendStructured should still work via the direct UpdateMessage fallback.
		entry := ProgressCardEntry{Kind: ProgressEntryThinking, Text: "fallback step"}
		if ok := w.AppendStructured(entry, "fallback step"); !ok {
			t.Fatal("AppendStructured() = false, want true (direct fallback)")
		}

		// AppendStructured with identifiable handle and nil registry must NOT
		// register anything in the unrelated registry.
		if card := registry.lookup("msg_xxx"); card != nil {
			t.Fatalf("nil-registry writer should not touch the unrelated registry, found %v", card)
		}
	})
}
