package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// fakePlatformForStreamCard is a minimal Platform that records streaming-card
// API calls made by feishuStreamingCard so tests can assert lazy creation,
// throttling, and finalize behavior without touching the Lark API.
type fakePlatformForStreamCard struct {
	mu                sync.Mutex
	useInteractive    bool
	sendPreviewCalls  int
	updateCalls       int
	finalizeCalls     int
	appendCalls       int
	insertCalls       int
	lastPreviewHandle *feishuPreviewHandle
	lastAppendPanel   string
	insertAnchor      string
	insertAfter       bool
	insertedElements  []map[string]any
	// missingPanels lists panel element ids the fake pretends the card does not
	// contain, so appends to them fail the way cardkit does: code 300315
	// "no such element id". Inserting the panel clears the entry, mirroring the
	// real API where the inserted panel then exists.
	missingPanels map[string]bool
}

func (f *fakePlatformForStreamCard) ProgressStyle() string { return "card" }

func (f *fakePlatformForStreamCard) SupportsProgressCardPayload() bool { return true }

func (f *fakePlatformForStreamCard) tag() string { return "fake-feishu" }

func (f *fakePlatformForStreamCard) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	if !f.useInteractive {
		return nil, core.ErrNotSupported
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendPreviewCalls++
	h := &feishuPreviewHandle{messageID: "msg_fake", chatID: "chat_fake", cardID: "card_fake"}
	f.lastPreviewHandle = h
	return h, nil
}

func (f *fakePlatformForStreamCard) StreamRichCardText(ctx context.Context, previewHandle any, fullText string) error {
	return nil
}

func (f *fakePlatformForStreamCard) appendCardElements(ctx context.Context, h *feishuPreviewHandle, targetElementID string, elements []map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missingPanels[targetElementID] {
		return fmt.Errorf("fake-feishu: append card elements failed: code=300315 msg=ErrMsg: msg: [no such element id: %s]", targetElementID)
	}
	f.appendCalls++
	f.lastAppendPanel = targetElementID
	return nil
}

func (f *fakePlatformForStreamCard) insertCardElements(ctx context.Context, h *feishuPreviewHandle, targetElementID string, after bool, elements []map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertCalls++
	f.insertAnchor = targetElementID
	f.insertAfter = after
	for _, el := range elements {
		if id, ok := el["element_id"].(string); ok {
			delete(f.missingPanels, id)
		}
	}
	f.insertedElements = append(f.insertedElements, elements...)
	return nil
}

func (f *fakePlatformForStreamCard) patchCardElementTitle(ctx context.Context, h *feishuPreviewHandle, elementID, title string) error {
	return nil
}

func (f *fakePlatformForStreamCard) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	return nil
}

func (f *fakePlatformForStreamCard) updateCardEntity(ctx context.Context, h *feishuPreviewHandle, cardJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	f.finalizeCalls++
	return nil
}

func (f *fakePlatformForStreamCard) count() (send, update, finalize int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendPreviewCalls, f.updateCalls, f.finalizeCalls
}

func (f *fakePlatformForStreamCard) appendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendCalls
}

func (f *fakePlatformForStreamCard) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insertCalls
}

func (f *fakePlatformForStreamCard) lastInsert() (anchor string, after bool, elements []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insertAnchor, f.insertAfter, f.insertedElements
}

func (f *fakePlatformForStreamCard) lastAppendElementID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAppendPanel
}

// CreateStreamingCard mirrors the real *Platform implementation for tests.
func (f *fakePlatformForStreamCard) CreateStreamingCard(ctx context.Context, replyCtx any) (core.StreamingCard, error) {
	if !f.useInteractive {
		return nil, core.ErrNotSupported
	}
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("fake-feishu: invalid reply context type %T", replyCtx)
	}
	if rc.chatID == "" {
		return nil, fmt.Errorf("fake-feishu: chatID is empty")
	}
	return &feishuStreamingCard{
		platform: f,
		replyCtx: replyCtx,
		done:     make(chan struct{}),
	}, nil
}

// TestCreateStreamingCard_LazyCreation verifies the card is not posted until
// the first Update arrives, and that the first Update creates exactly one
// preview message.
func TestCreateStreamingCard_LazyCreation(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake", messageID: "msg_fake"})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card, ok := sc.(*feishuStreamingCard)
	if !ok {
		t.Fatalf("unexpected streaming card type %T", sc)
	}
	if card.handle != nil {
		t.Error("card should be lazy (no handle before first Update)")
	}

	ctx := context.Background()
	if err := card.Update(ctx, "第一步"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Allow the flush to complete (it may be throttled by the 1.5s window;
	// call Finalize to force the terminal send path).
	_ = card.Finalize(ctx, "最终内容")

	send, _, _ := p.count()
	if send != 1 {
		t.Errorf("SendPreviewStart calls = %d, want 1 (lazy create once)", send)
	}
}

// TestCreateStreamingCard_NoInteractiveCard verifies ErrNotSupported is
// returned when interactive cards are disabled.
func TestCreateStreamingCard_NoInteractiveCard(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: false}
	_, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	if err != core.ErrNotSupported {
		t.Errorf("CreateStreamingCard err = %v, want core.ErrNotSupported", err)
	}
}

// TestCreateStreamingCard_InvalidReplyContext verifies invalid reply contexts
// are rejected up front.
func TestCreateStreamingCard_InvalidReplyContext(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	_, err := p.CreateStreamingCard(context.Background(), "not-a-reply-context")
	if err == nil {
		t.Error("CreateStreamingCard with invalid reply context should fail")
	}
}

// TestStreamingCard_FinalizeWithoutUpdate verifies Finalize before any Update
// still creates the card (lazy) and delivers the final content.
func TestStreamingCard_FinalizeWithoutUpdate(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*feishuStreamingCard)

	if err := card.Finalize(context.Background(), "直接最终内容"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	send, _, _ := p.count()
	if send != 1 {
		t.Errorf("SendPreviewStart calls = %d, want 1", send)
	}
	if card.Failed() {
		t.Error("card should not be failed after successful Finalize")
	}
}

// TestStreamingCard_UpdateAfterFinalizeIsNoop verifies updates after the card
// is finalized are ignored (no extra API calls).
func TestStreamingCard_UpdateAfterFinalizeIsNoop(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	sc, _ := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	card := sc.(*feishuStreamingCard)

	_ = card.Finalize(context.Background(), "完成")
	_ = card.Update(context.Background(), "不应发出")
	time.Sleep(50 * time.Millisecond) // allow any rogue timer to fire

	send, _, _ := p.count()
	if send != 1 {
		t.Errorf("SendPreviewStart calls = %d, want 1 (no extra sends after finalize)", send)
	}
}

// TestStreamingCard_FailedAfterCreateError verifies the card enters failed
// state when the underlying platform cannot create the preview.
func TestStreamingCard_FailedAfterCreateError(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*feishuStreamingCard)
	// Flip the fake to non-interactive so the lazy SendPreviewStart inside
	// Update fails, forcing the card into failed state.
	p.useInteractive = false
	_ = card.Update(context.Background(), "内容")
	// Finalize forces the terminal path; either way the card must be failed.
	_ = card.Finalize(context.Background(), "最终内容")
	if !card.Failed() {
		t.Error("card should be failed after preview creation error")
	}
}

// TestCardJSONForContent_PayloadRendersFoldablePanels verifies the shared
// renderer turns a structured payload into the same foldable-panel card JSON
// the compact progress card produces ("思考 (N)" / "工具 (N)" collapsible
// panels with the answer below) — the style the user expects.
func TestCardJSONForContent_PayloadRendersFoldablePanels(t *testing.T) {
	payload := core.BuildStreamingCardPayload(
		"第一行思考",
		[]string{"中间进展步骤"},
		nil,
		"最终答复",
		"opencode",
		core.LangChinese,
		core.ProgressCardStateRunning,
	)
	got := cardJSONForContent(payload, core.CardStatusWorking)
	if !strings.Contains(got, `"collapsible_panel"`) {
		t.Fatalf("card JSON missing collapsible_panel:\n%s", got)
	}
	if !strings.Contains(got, "思考 (2)") { // thinking + step text both fold into the panel
		t.Errorf("card JSON missing 思考 panel title:\n%s", got)
	}
	if !strings.Contains(got, "最终答复") {
		t.Errorf("card JSON missing answer body:\n%s", got)
	}
	// Panels must be collapsed by default so streaming updates don't make
	// the chat window jump.
	if strings.Contains(got, `"expanded":true`) {
		t.Errorf("card JSON has expanded panels, want collapsed by default:\n%s", got)
	}
	// Panels must carry element_ids (cardkit append targets) so intermediate
	// updates can add entries WITHOUT re-rendering the panel.
	if !strings.Contains(got, `"element_id":"thinking_panel"`) {
		t.Errorf("card JSON missing thinking_panel element_id:\n%s", got)
	}
	// The card must be in streaming mode for cardkit element APIs to work.
	if !strings.Contains(got, `"streaming_mode":true`) {
		t.Errorf("card JSON missing streaming_mode config:\n%s", got)
	}
	// No raw markdown wall: the old emoji markdown headers (💭 **思考** /
	// 🔧 **工具**) must be absent — thinking lives inside foldable panels.
	if strings.Contains(got, "💭 **思考**") || strings.Contains(got, "🔧 **工具") {
		t.Errorf("card JSON leaked emoji markdown headers:\n%s", got)
	}
}

// TestCardJSONForContent_PayloadDedupThinking verifies consecutive duplicate
// thinking text does not duplicate panel entries (opencode re-emits the same
// reasoning text across steps; the payload must contain it once).
func TestCardJSONForContent_PayloadDedupThinking(t *testing.T) {
	payload := core.BuildStreamingCardPayload(
		"重复思考",
		[]string{"重复思考", "重复思考", "新步骤"},
		nil,
		"",
		"opencode",
		core.LangChinese,
		core.ProgressCardStateRunning,
	)
	parsed, ok := core.ParseProgressCardPayload(payload)
	if !ok {
		t.Fatalf("payload did not parse back")
	}
	count := 0
	for _, item := range parsed.Items {
		if item.Text == "重复思考" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate thinking text appears %d times in payload, want 1", count)
	}
}

// TestCardJSONForContent_MarkdownFallback verifies non-payload content still
// renders as plain markdown (DingTalk/Slack-compatible path unchanged).
func TestCardJSONForContent_MarkdownFallback(t *testing.T) {
	got := cardJSONForContent("hello **world**", core.CardStatusDone)
	if strings.Contains(got, "collapsible_panel") {
		t.Errorf("markdown fallback rendered a panel:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("markdown fallback missing content:\n%s", got)
	}
}

// TestStreamingCard_PayloadUpdateAppends verifies structured payload content
// streams new thinking/tool entries into the collapsible panels via the
// cardkit element append API (type=append) — an update that does NOT
// re-render the panel, so a user-expanded panel stays expanded. Full-card
// updates happen only at Finalize. This is the user-approved behavior: live
// content growth without touching the user's panel expand/collapse state.
func TestStreamingCard_PayloadUpdateAppends(t *testing.T) {
	p := &fakePlatformForStreamCard{useInteractive: true}
	sc, _ := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	card := sc.(*feishuStreamingCard)

	payload1 := core.BuildStreamingCardPayload("思考一", nil, nil, "", "opencode", core.LangChinese, core.ProgressCardStateRunning)
	payload2 := core.BuildStreamingCardPayload("思考二", []string{"思考三"}, nil, "", "opencode", core.LangChinese, core.ProgressCardStateRunning)
	_ = card.Update(context.Background(), payload1)
	time.Sleep(feishuStreamingCardUpdateMinInterval + 200*time.Millisecond)
	_ = card.Update(context.Background(), payload2) // appends the delta entries
	time.Sleep(feishuStreamingCardUpdateMinInterval + 200*time.Millisecond)

	send, update, _ := p.count()
	if send != 1 {
		t.Errorf("SendPreviewStart calls = %d, want 1 (single lazy create)", send)
	}
	if update != 0 {
		t.Errorf("intermediate full-card updates = %d, want 0 (delta appends only)", update)
	}
	if p.appendCount() == 0 {
		t.Errorf("append calls = 0, want > 0 (delta entries appended to panels)")
	}

	_ = card.Finalize(context.Background(), payload2)
	send, update, _ = p.count()
	if send != 1 {
		t.Errorf("SendPreviewStart calls after finalize = %d, want 1", send)
	}
	if update != 1 {
		t.Errorf("full-card updates after finalize = %d, want 1 (final render)", update)
	}
}

// TestStreamingCard_InsertsMissingPanelInsteadOfFullUpdate covers the
// regression where the first entry of a lane whose panel the card does not
// have yet (appending to it fails with cardkit 300315 "no such element id")
// fell back to a full-card re-render. A full re-render re-sends the card JSON,
// which resets the expand/collapse state of every panel the user had touched —
// the opposite of what the incremental append path exists for. The panel must
// be inserted next to its sibling panel instead, and later entries of that
// lane must append to it.
func TestStreamingCard_InsertsMissingPanelInsteadOfFullUpdate(t *testing.T) {
	p := &fakePlatformForStreamCard{
		useInteractive: true,
		// The card is rendered with a thinking lane only, so it carries no
		// tools_panel: appends to it fail exactly like cardkit does.
		missingPanels: map[string]bool{progressPanelToolsElementID: true},
	}
	sc, _ := p.CreateStreamingCard(context.Background(), replyContext{chatID: "chat_fake"})
	card := sc.(*feishuStreamingCard)

	thinkingOnly := core.BuildProgressCardPayloadV2(
		[]core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "思考一"}},
		false, "opencode", core.LangChinese, core.ProgressCardStateRunning)
	_ = card.Update(context.Background(), thinkingOnly)
	time.Sleep(feishuStreamingCardUpdateMinInterval + 200*time.Millisecond)

	withOneTool := core.BuildProgressCardPayloadV2(
		[]core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "思考一"},
			{Kind: core.ProgressEntryToolUse, Tool: "bash", Text: "ls -l"},
		},
		false, "opencode", core.LangChinese, core.ProgressCardStateRunning)
	_ = card.Update(context.Background(), withOneTool)
	time.Sleep(feishuStreamingCardUpdateMinInterval + 200*time.Millisecond)

	if _, updates, _ := p.count(); updates != 0 {
		t.Errorf("full-card updates = %d, want 0: a missing panel must be inserted, never re-rendered", updates)
	}
	if got := p.insertCount(); got != 1 {
		t.Fatalf("insert calls = %d, want 1 (tools panel inserted once)", got)
	}
	anchor, after, inserted := p.lastInsert()
	if anchor != progressPanelThinkingElementID || !after {
		t.Errorf("insert anchor = (%q, after=%v), want (%q, after=true) so thinking stays above tools",
			anchor, after, progressPanelThinkingElementID)
	}
	if len(inserted) != 1 {
		t.Fatalf("inserted elements = %d, want 1 (the tools panel itself)", len(inserted))
	}
	panel := inserted[0]
	if panel["element_id"] != progressPanelToolsElementID {
		t.Errorf("inserted element_id = %v, want %q", panel["element_id"], progressPanelToolsElementID)
	}
	header, _ := panel["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	if content, _ := title["content"].(string); content != "工具 (1)" {
		t.Errorf("inserted panel title = %q, want %q", content, "工具 (1)")
	}
	if children, _ := panel["elements"].([]map[string]any); len(children) != 1 {
		t.Errorf("inserted panel entries = %d, want 1 (the tool entry that triggered the insert)", len(children))
	}

	// Once the panel exists, further entries of that lane must append to it —
	// still without any full-card re-render.
	withTwoTools := core.BuildProgressCardPayloadV2(
		[]core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "思考一"},
			{Kind: core.ProgressEntryToolUse, Tool: "bash", Text: "ls -l"},
			{Kind: core.ProgressEntryToolUse, Tool: "read", Text: "a.go"},
		},
		false, "opencode", core.LangChinese, core.ProgressCardStateRunning)
	_ = card.Update(context.Background(), withTwoTools)
	time.Sleep(feishuStreamingCardUpdateMinInterval + 200*time.Millisecond)

	if got := p.insertCount(); got != 1 {
		t.Errorf("insert calls = %d, want 1 (the panel is inserted once, then appended to)", got)
	}
	if got := p.appendCount(); got == 0 {
		t.Error("append calls = 0, want > 0 once the panel exists")
	}
	if got := p.lastAppendElementID(); got != progressPanelToolsElementID {
		t.Errorf("last append target = %q, want %q", got, progressPanelToolsElementID)
	}
	if _, updates, _ := p.count(); updates != 0 {
		t.Errorf("full-card updates = %d, want 0 after the panel exists", updates)
	}
}
