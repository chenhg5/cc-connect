package core

import (
	"context"
	"errors"
	"fmt"
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
	p := &previewCapturePlatform{}
	replyCtx := progressHintReplyCtx{
		style:   progressStyleCard,
		payload: true,
	}

	w := newCompactProgressWriter(context.Background(), p, replyCtx, "codex", LangEnglish, nil)
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
	})

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

type stubThrottledProgressPlatform struct {
	stubCompactProgressPlatform
	throttle time.Duration
}

func (p *stubThrottledProgressPlatform) ProgressUpdateInterval() time.Duration {
	return p.throttle
}

func TestCompactProgressWriter_ThrottlesRapidUpdates(t *testing.T) {
	p := &stubThrottledProgressPlatform{
		stubCompactProgressPlatform: stubCompactProgressPlatform{
			stubPlatformEngine: stubPlatformEngine{n: "discord"},
			style:              "card",
			supportPayload:     true,
		},
		throttle: 50 * time.Millisecond,
	}
	w := newCompactProgressWriter(context.Background(), p, "ctx", "cc", LangEnglish, nil)

	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step 1"}, "step 1")
	if len(p.getPreviewStarts()) != 1 {
		t.Fatal("first update should create the preview message")
	}

	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}, "pwd")
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryToolResult, Tool: "Bash", Text: "ok"}, "ok")
	editsBeforeThrottle := len(p.getPreviewEdits())
	if editsBeforeThrottle > 0 {
		t.Fatalf("rapid updates within throttle window should be skipped, got %d edits", editsBeforeThrottle)
	}

	time.Sleep(60 * time.Millisecond)
	w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryThinking, Text: "step 4"}, "step 4")
	editsAfterWait := len(p.getPreviewEdits())
	if editsAfterWait != 1 {
		t.Fatalf("update after throttle interval should go through, got %d edits", editsAfterWait)
	}

	ok := w.Finalize(ProgressCardStateCompleted)
	if !ok {
		t.Fatal("Finalize should succeed")
	}
	finalEdits := p.getPreviewEdits()
	last := finalEdits[len(finalEdits)-1]
	payload, parsed := ParseProgressCardPayload(last)
	if !parsed {
		t.Fatalf("final edit should be a valid payload, got %q", last)
	}
	if payload.State != ProgressCardStateCompleted {
		t.Fatalf("state = %q, want completed", payload.State)
	}
	if len(payload.Items) != 4 {
		t.Fatalf("items = %d, want 4 (all buffered items)", len(payload.Items))
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
	})

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

// A failed edit must not abandon the existing progress message for the turn.
type recoveringProgressPlatform struct {
	previewCapturePlatform
	updateErr error
}

func (p *recoveringProgressPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	if handle != "preview-1" {
		return errors.New("progress update used a different message")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.updated = append(p.updated, content)
	return p.updateErr
}

func TestCompactProgressWriter_RecoversAfterUpdateFailure(t *testing.T) {
	for _, style := range []string{progressStyleCompact, progressStyleCard} {
		t.Run(style, func(t *testing.T) {
			p := &recoveringProgressPlatform{updateErr: errors.New("temporary update failure")}
			w := newCompactProgressWriter(context.Background(), p,
				progressHintReplyCtx{style: style, payload: true}, "codex", LangEnglish, nil)
			if !w.Append("initial") || !w.Append("failed edit") {
				t.Fatal("progress events should remain handled when an edit fails")
			}
			if !w.Append("buffered during outage") || len(p.updated) != 1 {
				t.Fatalf("failed edits should be throttled, updates = %d", len(p.updated))
			}
			p.updateErr = nil
			time.Sleep(1100 * time.Millisecond)
			if !w.Append("recovered") {
				t.Fatal("next progress event should recover the existing message")
			}
			if len(p.started) != 1 || len(p.updated) != 2 {
				t.Fatalf("starts = %d, edits = %d; want one message and two edit attempts", len(p.started), len(p.updated))
			}
			for _, text := range []string{"failed edit", "buffered during outage", "recovered"} {
				if !strings.Contains(p.updated[1], text) {
					t.Errorf("recovered edit lost %q", text)
				}
			}
		})
	}
}

func TestCompactProgressWriter_FinalizeRecoversAfterUpdateFailure(t *testing.T) {
	p := &recoveringProgressPlatform{updateErr: errors.New("temporary update failure")}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)
	w.Append("initial")
	w.Append("pending progress")
	p.updateErr = nil
	if !w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("Finalize should retry the existing card even during edit backoff")
	}
	if len(p.updated) != 2 {
		t.Fatalf("edits = %d, want failed progress and successful final edit", len(p.updated))
	}
	payload, ok := ParseProgressCardPayload(p.updated[1])
	if !ok || payload.State != ProgressCardStateCompleted || len(payload.Items) != 2 {
		t.Fatalf("final payload = %#v, want completed with buffered progress", payload)
	}
}

func TestCompactProgressWriter_FinalizeCanRetryFailedCompletion(t *testing.T) {
	p := &recoveringProgressPlatform{updateErr: errors.New("temporary final edit failure")}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)
	w.Append("initial")
	if w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("failed completion should report failure")
	}
	p.updateErr = nil
	if !w.Finalize(ProgressCardStateCompleted) || len(p.updated) != 2 {
		t.Fatal("retrying completion should actually update the same card")
	}
}

func TestCompactProgressWriter_PersistentFailureDoesNotSpamOrBlockFinalization(t *testing.T) {
	p := &recoveringProgressPlatform{updateErr: errors.New("still unavailable")}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)
	for range 100 {
		if !w.Append("tool progress") {
			t.Fatal("an established card must keep handling events during an outage")
		}
	}
	if len(p.started) != 1 || len(p.updated) != 1 {
		t.Fatalf("starts = %d, edits = %d; want one card and one throttled failed edit", len(p.started), len(p.updated))
	}
	if w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("persistent outage should report failed final update")
	}
	if len(p.updated) != 2 {
		t.Fatal("Finalize must attempt the terminal state despite edit backoff")
	}
}

func TestCompactProgressWriter_CancellationStopsRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &recoveringProgressPlatform{}
	w := newCompactProgressWriter(ctx, p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)
	w.Append("initial")
	cancel()
	if !w.Append("cancelled progress") || len(p.updated) != 0 {
		t.Fatal("cancelled progress should not start an update or trigger fallback")
	}
	if w.Finalize(ProgressCardStateCompleted) {
		t.Fatal("cancelled final update should report failure")
	}
}

func TestCompactProgressWriter_IndependentQuotas_ToolsDoNotEvictThinkingOrUpdates(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)

	for i := 1; i <= 3; i++ {
		if !w.AppendEvent(ProgressEntryThinking, fmt.Sprintf("think-%d", i), "", "") {
			t.Fatalf("append thinking %d failed", i)
		}
	}
	for i := 1; i <= 3; i++ {
		if !w.AppendEvent(ProgressEntryInfo, fmt.Sprintf("update-%d", i), "", "") {
			t.Fatalf("append update %d failed", i)
		}
	}
	for i := 1; i <= 10; i++ {
		kind := ProgressEntryToolUse
		if i%2 == 0 {
			kind = ProgressEntryToolResult
		}
		if !w.AppendEvent(kind, fmt.Sprintf("tool-%d", i), "Bash", "") {
			t.Fatalf("append tool %d failed", i)
		}
	}

	beforeEvict, ok := ParseProgressCardPayload(w.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload failed: %q", w.content)
	}
	if beforeEvict.Truncated {
		t.Fatal("Truncated = true before any category exceeded its quota, want false")
	}
	if len(beforeEvict.Items) != 16 {
		t.Fatalf("items = %d, want 16 (3 thinking + 3 updates + 10 tools)", len(beforeEvict.Items))
	}

	// Exceeding the tool quota must evict only the oldest tools and preserve
	// all thinking and update entries in chronological order.
	if !w.AppendEvent(ProgressEntryToolUse, "tool-11", "Bash", "") {
		t.Fatal("append tool-11 failed")
	}
	if !w.AppendEvent(ProgressEntryToolResult, "tool-12", "Bash", "") {
		t.Fatal("append tool-12 failed")
	}

	afterEvict, ok := ParseProgressCardPayload(w.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload failed after tool eviction: %q", w.content)
	}
	if !afterEvict.Truncated {
		t.Fatal("Truncated = false after tool eviction, want true")
	}
	if len(afterEvict.Items) != 16 {
		t.Fatalf("items after tool eviction = %d, want 16", len(afterEvict.Items))
	}
	wantTexts := []string{
		"think-1", "think-2", "think-3",
		"update-1", "update-2", "update-3",
		"tool-3", "tool-4", "tool-5", "tool-6", "tool-7",
		"tool-8", "tool-9", "tool-10", "tool-11", "tool-12",
	}
	for i, want := range wantTexts {
		if afterEvict.Items[i].Text != want {
			t.Fatalf("item[%d].Text = %q, want %q (all=%v)", i, afterEvict.Items[i].Text, want, afterEvict.Items)
		}
	}
}

func TestCompactProgressWriter_IndependentQuotas_EachCategoryEvictsIndependently(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil)

	for i := 1; i <= 3; i++ {
		w.AppendEvent(ProgressEntryThinking, fmt.Sprintf("think-%d", i), "", "")
	}
	w.AppendEvent(ProgressEntryInfo, "upd-1", "", "")
	w.AppendEvent(ProgressEntryError, "upd-2", "", "")
	w.AppendEvent(ProgressEntryInfo, "upd-3", "", "")
	for i := 1; i <= 10; i++ {
		w.AppendEvent(ProgressEntryToolUse, fmt.Sprintf("tool-%d", i), "Bash", "")
	}

	// 4th thinking evicts think-1 only.
	w.AppendEvent(ProgressEntryThinking, "think-4", "", "")
	// 4th update (error) evicts upd-1 only.
	w.AppendEvent(ProgressEntryError, "upd-4", "", "")
	// 11th tool evicts tool-1 only.
	w.AppendEvent(ProgressEntryToolUse, "tool-11", "Bash", "")

	payload, ok := ParseProgressCardPayload(w.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload failed: %q", w.content)
	}
	if !payload.Truncated {
		t.Fatal("Truncated = false, want true")
	}

	var thinking, updates, tools []string
	for _, it := range payload.Items {
		switch it.Kind {
		case ProgressEntryThinking:
			thinking = append(thinking, it.Text)
		case ProgressEntryToolUse, ProgressEntryToolResult:
			tools = append(tools, it.Text)
		default:
			updates = append(updates, it.Text)
		}
	}
	if got := strings.Join(thinking, ","); got != "think-2,think-3,think-4" {
		t.Fatalf("thinking = %q, want think-2,think-3,think-4", got)
	}
	if got := strings.Join(updates, ","); got != "upd-2,upd-3,upd-4" {
		t.Fatalf("updates = %q, want upd-2,upd-3,upd-4", got)
	}
	if len(tools) != 10 || tools[0] != "tool-2" || tools[9] != "tool-11" {
		t.Fatalf("tools = %v, want tool-2..tool-11 (len 10)", tools)
	}

	// Verify Truncated remains sticky on a subsequent non-evicting append
	// after one bucket has already been truncated.
	w2 := newCompactProgressWriterWithQuotas(context.Background(), &previewCapturePlatform{},
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil,
		ProgressCardQuotas{Tools: 2, Thinking: 3, Updates: 3})
	w2.AppendEvent(ProgressEntryToolUse, "t1", "Bash", "")
	w2.AppendEvent(ProgressEntryToolUse, "t2", "Bash", "")
	w2.AppendEvent(ProgressEntryToolUse, "t3", "Bash", "")
	w2.AppendEvent(ProgressEntryThinking, "non-evicting-think", "", "")
	stickyPayload, ok := ParseProgressCardPayload(w2.content)
	if !ok || !stickyPayload.Truncated {
		t.Fatalf("Truncated should remain sticky after earlier eviction, got ok=%v payload=%#v", ok, stickyPayload)
	}
}

func TestCompactProgressWriter_IndependentQuotas_CustomOverridesAndFallback(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriterWithQuotas(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil,
		ProgressCardQuotas{Tools: 4, Thinking: 2, Updates: 1})

	for i := 1; i <= 3; i++ {
		w.AppendEvent(ProgressEntryThinking, fmt.Sprintf("think-%d", i), "", "")
	}
	for i := 1; i <= 2; i++ {
		w.AppendEvent(ProgressEntryInfo, fmt.Sprintf("upd-%d", i), "", "")
	}
	for i := 1; i <= 5; i++ {
		w.AppendEvent(ProgressEntryToolUse, fmt.Sprintf("tool-%d", i), "Bash", "")
	}

	payload, ok := ParseProgressCardPayload(w.content)
	if !ok {
		t.Fatalf("ParseProgressCardPayload failed: %q", w.content)
	}
	if !payload.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(payload.Items) != 7 {
		t.Fatalf("items = %d, want 7 (2 thinking + 1 update + 4 tools)", len(payload.Items))
	}
	want := []string{"think-2", "think-3", "upd-2", "tool-2", "tool-3", "tool-4", "tool-5"}
	for i, wText := range want {
		if payload.Items[i].Text != wText {
			t.Fatalf("item[%d] = %q, want %q", i, payload.Items[i].Text, wText)
		}
	}

	// Non-positive quotas fall back to defaults (10, 3, 3).
	wDefault := newCompactProgressWriterWithQuotas(context.Background(), &previewCapturePlatform{},
		progressHintReplyCtx{style: progressStyleCard, payload: true}, "codex", LangEnglish, nil,
		ProgressCardQuotas{Tools: 0, Thinking: -2, Updates: 0})
	if wDefault.quotas != (ProgressCardQuotas{Tools: DefaultProgressMaxTools, Thinking: DefaultProgressMaxThinking, Updates: DefaultProgressMaxUpdates}) {
		t.Fatalf("fallback quotas = %+v, want defaults (10/3/3)", wDefault.quotas)
	}
}

func TestCompactProgressWriter_IndependentQuotas_MarkdownFallback(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p,
		progressHintReplyCtx{style: progressStyleCard, payload: false}, "codex", LangEnglish, nil)

	w.AppendEvent(ProgressEntryThinking, "think-1", "", "think-1")
	w.AppendEvent(ProgressEntryThinking, "think-2", "", "think-2")
	w.AppendEvent(ProgressEntryInfo, "upd-1", "", "upd-1")
	w.AppendEvent(ProgressEntryInfo, "upd-2", "", "upd-2")
	for i := 1; i <= 12; i++ {
		line := fmt.Sprintf("tool-%d", i)
		w.AppendEvent(ProgressEntryToolUse, line, "Bash", line)
	}

	if !strings.Contains(w.content, "_Showing latest updates only._") {
		t.Fatalf("expected truncated notice in markdown fallback, got %q", w.content)
	}
	for _, want := range []string{"think-1", "think-2", "upd-1", "upd-2", "tool-3", "tool-12"} {
		if !strings.Contains(w.content, want) {
			t.Fatalf("markdown fallback missing %q: %q", want, w.content)
		}
	}
	for _, evicted := range []string{"tool-1\n", "tool-2\n"} {
		if strings.Contains(w.content, evicted) {
			t.Fatalf("markdown fallback should have evicted %q: %q", evicted, w.content)
		}
	}
}
