package core

import (
	"context"
	"strings"
	"sync"
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
	mu      sync.Mutex
	started []string
	updated []string
}

func (p *previewCapturePlatform) Name() string                             { return "bridge" }
func (p *previewCapturePlatform) Start(MessageHandler) error               { return nil }
func (p *previewCapturePlatform) Reply(context.Context, any, string) error { return nil }
func (p *previewCapturePlatform) Send(context.Context, any, string) error  { return nil }
func (p *previewCapturePlatform) Stop() error                              { return nil }

func (p *previewCapturePlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.mu.Lock()
	p.started = append(p.started, content)
	p.mu.Unlock()
	return "preview-1", nil
}

func (p *previewCapturePlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	p.updated = append(p.updated, content)
	p.mu.Unlock()
	return nil
}

func (p *previewCapturePlatform) startedLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.started)
}

func (p *previewCapturePlatform) updatedLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.updated)
}

func (p *previewCapturePlatform) startedSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.started))
	copy(out, p.started)
	return out
}

func (p *previewCapturePlatform) updatedSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.updated))
	copy(out, p.updated)
	return out
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
	if p.startedLen() != 1 {
		t.Fatalf("started = %d, want 1", p.startedLen())
	}
	if !strings.HasPrefix(p.startedSnapshot()[0], ProgressCardPayloadPrefix) {
		t.Fatalf("preview start payload = %q, want progress payload prefix", p.startedSnapshot()[0])
	}

	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize() = false, want true")
	}

	time.Sleep(200 * time.Millisecond)
	if p.updatedLen() != 1 {
		t.Fatalf("updated = %d, want 1", p.updatedLen())
	}

	parsed, ok := ParseProgressCardPayload(p.updatedSnapshot()[0])
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
	if p.startedLen() != 1 {
		t.Fatalf("preview starts = %d, want 1", p.startedLen())
	}

	entry2 := ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	if ok := w.AppendStructured(entry2, "Tool: Bash\npwd"); !ok {
		t.Fatal("second AppendStructured() = false, want true")
	}

	// Direct PATCH must not be used for the second update.
	if p.updatedLen() != 0 {
		t.Fatalf("direct UpdateMessage calls = %d, want 0; updates should go through registry", p.updatedLen())
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
	if p.updatedLen() != 1 {
		t.Fatalf("direct UpdateMessage calls = %d, want 1", p.updatedLen())
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
	if p.updatedLen() != 0 {
		t.Fatalf("direct UpdateMessage calls = %d, want 0; final state should be flushed by registry ticker", p.updatedLen())
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

func TestInferLegacyEntryKind(t *testing.T) {
	cases := []struct {
		in   string
		want ProgressCardEntryKind
	}{
		{"💭 thinking", ProgressEntryThinking},
		{"🔧 tool", ProgressEntryToolUse},
		{"**Tool #1 pwd", ProgressEntryToolUse},
		{"🧾 result", ProgressEntryToolResult},
		{"❌ error", ProgressEntryError},
		{"plain info", ProgressEntryInfo},
	}
	for _, tc := range cases {
		got := inferLegacyEntryKind(tc.in)
		if got != tc.want {
			t.Errorf("inferLegacyEntryKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeProgressAgentLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "Agent"},
		{"agent", "Agent"},
		{"codex", "Codex"},
		{"claudecode", "CC"},
		{"claude-code", "CC"},
		{"cc", "CC"},
		{"gemini", "Gemini"},
		{"cursor", "Cursor"},
		{"qoder", "Qoder"},
		{"iflow", "iFlow"},
		{"opencode", "OpenCode"},
		{"pi", "PI"},
		{"myagent", "Myagent"},
	}
	for _, tc := range cases {
		got := normalizeProgressAgentLabel(tc.in)
		if got != tc.want {
			t.Errorf("normalizeProgressAgentLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMessageHandleID_NonIdentifier(t *testing.T) {
	if got := messageHandleHasID("not an identifier"); got {
		t.Errorf("messageHandleHasID(non-identifier) = true, want false")
	}
	if got := messageHandleID("not an identifier"); got != "" {
		t.Errorf("messageHandleID(non-identifier) = %q, want empty", got)
	}
}

func TestProgressCardPayloadForTarget_Fallback(t *testing.T) {
	plain := &stubPlatformNoProgress{}
	if progressCardPayloadForTarget(plain, "ctx") {
		t.Fatal("expected false for platform without payload support")
	}
}

func TestRegisterHandle_ErrorPaths(t *testing.T) {
	p := &previewCapturePlatformWithIdentifiableHandle{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: progressStyleCard, payload: true}, "Agent", LangEnglish, nil, nil)

	if w.registerHandle(&stubIdentifiableHandle{id: "msg_1"}, "content") {
		t.Fatal("registerHandle with nil registry should return false")
	}

	registry := NewCardRegistry(t.TempDir())
	defer registry.Stop()
	w2 := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{style: progressStyleCard, payload: true}, "Agent", LangEnglish, nil, registry)

	if w2.registerHandle("not identifiable", "content") {
		t.Fatal("registerHandle with non-identifiable handle should return false")
	}
	if w2.registerHandle(&stubIdentifiableHandle{id: "  "}, "content") {
		t.Fatal("registerHandle with empty messageID should return false")
	}
}

func TestCompactProgressWriter_Append(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCompact}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if !w.enabled {
		t.Fatal("writer should be enabled")
	}
	if !w.Append("hello") {
		t.Fatal("Append() = false, want true")
	}
	if got := p.getPreviewStarts(); len(got) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(got))
	}
}

func TestCompactProgressWriter_AppendStructured_Disabled(t *testing.T) {
	p := &stubPlatformNoProgress{}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if w.AppendStructured(ProgressCardEntry{Text: "hello"}, "hello") {
		t.Fatal("AppendStructured on disabled writer should return false")
	}
}

func TestCompactProgressWriter_AppendStructured_Empty(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCompact}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if !w.AppendStructured(ProgressCardEntry{Text: "   "}, "   ") {
		t.Fatal("AppendStructured with empty text should return true")
	}
}

func TestCompactProgressWriter_AppendStructured_DirectFallback(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCompact}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)

	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}, "step1") {
		t.Fatal("first AppendStructured failed")
	}
	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}, "Tool: Bash\npwd") {
		t.Fatal("second AppendStructured failed")
	}

	edits := p.getPreviewEdits()
	if len(edits) != 1 {
		t.Fatalf("direct edits = %d, want 1", len(edits))
	}
	if edits[0] != "step1\n\nTool: Bash\npwd" {
		t.Errorf("edit content = %q", edits[0])
	}
}

func TestCompactProgressWriter_AppendStructured_CardPayload(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCard, supportPayload: true}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)

	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}, "step1") {
		t.Fatal("AppendStructured failed")
	}
	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	if !strings.HasPrefix(starts[0], ProgressCardPayloadPrefix) {
		t.Fatalf("expected structured payload, got %q", starts[0])
	}
}

func TestCompactProgressWriter_AppendStructured_CardMarkdownFallback(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCard}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)

	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step1"}, "step1") {
		t.Fatal("AppendStructured failed")
	}
	if len(p.getPreviewStarts()) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(p.getPreviewStarts()))
	}
	if strings.HasPrefix(p.getPreviewStarts()[0], ProgressCardPayloadPrefix) {
		t.Fatal("expected markdown fallback, got payload")
	}
}

func TestCompactProgressWriter_AppendStructured_Transform(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCompact}
	transform := func(s string) string { return strings.ToUpper(s) }
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, transform, nil)

	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "hello"}, "hello") {
		t.Fatal("AppendStructured thinking failed")
	}
	if !w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, Text: "result"}, "result") {
		t.Fatal("AppendStructured tool result failed")
	}

	edits := p.getPreviewEdits()
	if len(edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(edits))
	}
	if !strings.Contains(edits[0], "HELLO") {
		t.Errorf("thinking not transformed: %q", edits[0])
	}
	if strings.Contains(edits[0], "RESULT") {
		t.Errorf("tool result should not be transformed: %q", edits[0])
	}
}

func TestCompactProgressWriter_Finalize_DirectFallback(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCard, supportPayload: true}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)

	if !w.AppendStructured(ProgressCardEntry{Text: "running"}, "running") {
		t.Fatal("AppendStructured failed")
	}
	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize failed")
	}
	if edits := p.getPreviewEdits(); len(edits) != 1 {
		t.Fatalf("finalize edits = %d, want 1", len(edits))
	}
}

func TestCompactProgressWriter_Finalize_Disabled(t *testing.T) {
	p := &stubPlatformNoProgress{}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize on disabled writer should return false")
	}
}

func TestCompactProgressWriter_Finalize_SameState(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCard, supportPayload: true}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if !w.AppendStructured(ProgressCardEntry{Text: "running"}, "running") {
		t.Fatal("AppendStructured failed")
	}
	if !w.Finalize(ProgressCardStateRunning) {
		t.Fatal("Finalize with same state should return true")
	}
}

func TestCompactProgressWriter_Finalize_DefaultState(t *testing.T) {
	p := &stubCompactProgressPlatform{style: progressStyleCard, supportPayload: true}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "codex", LangEnglish, nil, nil)
	if !w.AppendStructured(ProgressCardEntry{Text: "running"}, "running") {
		t.Fatal("AppendStructured failed")
	}
	if !w.Finalize("") {
		t.Fatal("Finalize with empty state should default to completed")
	}
	if w.state != ProgressCardStateCompleted {
		t.Errorf("state = %q, want completed", w.state)
	}
}

func TestWithAPITimeout_AlreadyHasDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	w := &compactProgressWriter{ctx: ctx}
	callCtx, cancel2 := w.withAPITimeout()
	if callCtx != ctx {
		t.Fatal("withAPITimeout should return existing context when deadline present")
	}
	cancel2()
}

func TestRenderCardProgressMarkdownFallback_Truncated(t *testing.T) {
	got := renderCardProgressMarkdownFallback([]string{"a", "b"}, true)
	if !strings.Contains(got, "Showing latest updates only") {
		t.Errorf("truncated marker missing: %q", got)
	}
}

func TestTrimCompactProgressText(t *testing.T) {
	if got := trimCompactProgressText("hello", 0); got != "hello" {
		t.Errorf("max<=0 should return unchanged, got %q", got)
	}
	short := "short text"
	if got := trimCompactProgressText(short, 100); got != short {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := trimCompactProgressText(long, 10)
	if !strings.HasPrefix(got, "…\n") {
		t.Errorf("truncated text missing ellipsis prefix: %q", got)
	}
}

func TestBuildProgressCardPayloadV2_Defaults(t *testing.T) {
	payload := BuildProgressCardPayloadV2([]ProgressCardEntry{{Text: "step"}}, false, "", LangEnglish, "")
	if payload == "" {
		t.Fatal("payload empty")
	}
	parsed, ok := ParseProgressCardPayload(payload)
	if !ok {
		t.Fatal("parse failed")
	}
	if parsed.State != ProgressCardStateRunning {
		t.Errorf("state default = %q, want running", parsed.State)
	}
}

func TestNewCompactProgressWriter_Disabled(t *testing.T) {
	plain := &stubPlatformNoProgress{}
	w := newCompactProgressWriter(context.Background(), plain, "ctx", "codex", LangEnglish, nil, nil)
	if w.enabled {
		t.Fatal("writer should be disabled for platform without MessageUpdater")
	}
}
