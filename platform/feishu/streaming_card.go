package feishu

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// feishuStreamingCardUpdateMinInterval coalesces card updates for the
// streaming card. Feishu cardkit-v1 update API has a generous QPS budget
// (50 QPS per element), but full-card entity updates are heavier than the
// streaming-text PUT; coalescing avoids turning a burst of tool events into
// a burst of full-card updates.
const feishuStreamingCardUpdateMinInterval = 1500 * time.Millisecond

// feishuStreamingCard aggregates one agent turn (thinking + tool steps +
// answer) into a single Feishu interactive card that updates in place — the
// cc-connect equivalent of DingTalk's AI Card and Slack's streaming card.
// Implements core.StreamingCard.
//
// The card is created LAZILY on the first non-empty content via the existing
// SendPreviewStart flow (cardkit-v1 two-step when available, inline card JSON
// otherwise), so the platform's typing indicator stays visible until the bot
// actually has something to show. Updates prefer the cardkit-v1 streaming
// text PUT (typewriter effect) when the card was created with a card entity
// id; otherwise they fall back to the full-card entity update.
// streamingCardAPI is the subset of *Platform the streaming card needs.
// Defined as an interface so tests can substitute a fake without touching
// the Lark API client.
type streamingCardAPI interface {
	SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error)
	StreamRichCardText(ctx context.Context, previewHandle any, fullText string) error
	UpdateMessage(ctx context.Context, previewHandle any, content string) error
	updateCardEntity(ctx context.Context, h *feishuPreviewHandle, cardJSON string) error
	// appendCardElements appends elements to a container element (e.g. a
	// collapsible_panel) via the cardkit element API — an intermediate update
	// that does NOT re-render the panel, so a user-expanded panel stays open.
	appendCardElements(ctx context.Context, h *feishuPreviewHandle, targetElementID string, elements []map[string]any) error
	// insertCardElements adds elements positioned against targetElementID, or
	// at the end of the card body when it is empty. Used to create a panel the
	// rendered card does not contain yet without re-rendering the whole card.
	insertCardElements(ctx context.Context, h *feishuPreviewHandle, targetElementID string, after bool, elements []map[string]any) error
	// patchCardElementTitle updates ONLY the header title of a collapsible
	// panel (keeping the "思考 (N)" count live) without touching the panel's
	// expanded state.
	patchCardElementTitle(ctx context.Context, h *feishuPreviewHandle, elementID, title string) error
	tag() string
}

// Ensure *Platform satisfies streamingCardAPI.
var _ streamingCardAPI = (*Platform)(nil)

type feishuStreamingCard struct {
	platform streamingCardAPI
	replyCtx any

	mu         sync.Mutex
	handle     *feishuPreviewHandle // nil until first content arrives
	failed     bool
	pending    string
	timer      *time.Timer
	inFlight   bool
	done       chan struct{} // closed when finalized or failed
	lastUpdate time.Time

	// Incremental append bookkeeping for payload cards: how many thinking /
	// tool entries have already been appended via cardkit element APIs since
	// the card was created (or last full-card synced). Intermediate updates
	// append only the delta so the collapsible panels are never re-rendered —
	// a user-expanded panel stays expanded.
	appendedThinking int
	appendedTools    int

	// hasThinkingPanel/hasToolsPanel record whether the card entity currently
	// contains that lane's collapsible panel. A lane panel only exists once the
	// lane had at least one entry at render time, so the first entry of a lane
	// must INSERT the panel — appending to a panel the card does not have fails
	// with cardkit error 300315 ("no such element id").
	hasThinkingPanel bool
	hasToolsPanel    bool
}

// Ensure feishuStreamingCard implements core.StreamingCard.
var _ core.StreamingCard = (*feishuStreamingCard)(nil)

// SupportsStreamingCardPayload implements core.StreamingCardPayloadSupporter:
// the Feishu streaming card renders the structured progress payload as the
// same foldable panels as the compact progress card ("思考 (N)" / "工具 (N)"
// collapsible panels with the final answer below), instead of a raw markdown
// wall of intermediate process.
func (c *feishuStreamingCard) SupportsStreamingCardPayload() bool { return true }

// cardJSONForContent renders card JSON for the given content. When content is
// a structured progress payload (ProgressCardPayloadPrefix), the card uses the
// shared foldable-panel renderer; otherwise it falls back to plain markdown.
func cardJSONForContent(content string, status core.CardStatus) string {
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		// Keep the state in sync with the lifecycle status so the header
		// color reflects working → done, mirroring the compact progress card.
		if status == core.CardStatusDone {
			payload.State = core.ProgressCardStateCompleted
		} else if payload.State == "" {
			payload.State = core.ProgressCardStateRunning
		}
		return buildProgressCardJSONFromPayload(payload)
	}
	return buildCardJSONWithStatus(content, status)
}

// CreateStreamingCard implements core.StreamingCardPlatform. The card is not
// posted until the first Update arrives (lazy), so no work happens here beyond
// capturing the reply context.
func (p *Platform) CreateStreamingCard(ctx context.Context, replyCtx any) (core.StreamingCard, error) {
	if !p.useInteractiveCard {
		return nil, core.ErrNotSupported
	}
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("%s: invalid reply context type %T", p.tag(), replyCtx)
	}
	if rc.chatID == "" {
		return nil, fmt.Errorf("%s: chatID is empty", p.tag())
	}
	return &feishuStreamingCard{
		platform: p,
		replyCtx: replyCtx,
		done:     make(chan struct{}),
	}, nil
}

// Update accumulates the latest content and schedules a throttled flush.
//
// Structured progress payloads are posted ONCE — the first Update creates the
// card (lazy) and subsequent payload Updates are silent (the send path
// short-circuits) so intermediate process never re-renders the collapsible
// panels and a user-expanded panel stays open. The full content lands at
// Finalize.
func (c *feishuStreamingCard) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return nil
	}
	c.pending = content

	// If a request is in flight, let the scheduled timer pick up the latest.
	if c.inFlight {
		c.scheduleFlushLocked()
		c.mu.Unlock()
		return nil
	}

	if c.timer == nil && time.Since(c.lastUpdate) >= feishuStreamingCardUpdateMinInterval {
		c.mu.Unlock()
		return c.flush(ctx)
	}
	c.scheduleFlushLocked()
	c.mu.Unlock()
	return nil
}

// scheduleFlushLocked schedules a flush after the throttle window.
// Must be called with c.mu held.
func (c *feishuStreamingCard) scheduleFlushLocked() {
	if c.timer != nil {
		return
	}
	delay := feishuStreamingCardUpdateMinInterval - time.Since(c.lastUpdate)
	if delay < 0 {
		delay = 0
	}
	c.timer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		c.timer = nil
		if c.failed {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		_ = c.flush(context.Background())
	})
}

// flush sends the current content to the card. Caller must NOT hold c.mu
// (send paths acquire the platform's own locks).
func (c *feishuStreamingCard) flush(ctx context.Context) error {
	c.mu.Lock()
	c.inFlight = true
	content := c.pending
	c.mu.Unlock()

	err := c.send(ctx, content)

	c.mu.Lock()
	c.inFlight = false
	if err != nil {
		slog.Debug("feishu: streaming card flush failed", "error", err)
	} else {
		c.lastUpdate = time.Now()
	}
	c.mu.Unlock()
	return err
}

// send lazily creates the card on first use, then updates it in place.
// Caller must NOT hold c.mu.
func (c *feishuStreamingCard) send(ctx context.Context, content string) error {
	p := c.platform

	c.mu.Lock()
	handle := c.handle
	c.mu.Unlock()

	if handle == nil {
		// First content: create the card via the preview flow. The card JSON
		// carries the main_text element so cardkit-v1 streaming text updates
		// (typewriter) work when the card entity path is available.
		initialJSON := cardJSONForContent(content, core.CardStatusWorking)
		h, err := p.SendPreviewStart(ctx, c.replyCtx, initialJSON)
		if err != nil {
			c.mu.Lock()
			c.failed = true
			select {
			case <-c.done:
			default:
				close(c.done)
			}
			c.mu.Unlock()
			return fmt.Errorf("%s: streaming card create: %w", p.tag(), err)
		}
		fh, ok := h.(*feishuPreviewHandle)
		if !ok {
			c.mu.Lock()
			c.failed = true
			select {
			case <-c.done:
			default:
				close(c.done)
			}
			c.mu.Unlock()
			return fmt.Errorf("%s: streaming card: invalid preview handle type %T", p.tag(), h)
		}
		c.mu.Lock()
		c.handle = fh
		// The created card already renders the entries present in this first
		// content; record them so later updates append only the delta.
		if payload, ok := core.ParseProgressCardPayload(content); ok {
			reasoning, tools, _ := splitProgressItemsByLane(payload.Items)
			c.appendedThinking = len(reasoning)
			c.appendedTools = len(tools)
			c.hasThinkingPanel = len(reasoning) > 0
			c.hasToolsPanel = len(tools) > 0
		}
		c.mu.Unlock()
		return nil
	}

	// In-place update.
	//
	// Structured progress payloads append only the NEW thinking/tool entries
	// to their collapsible panels via the cardkit element API (type=append).
	// This never re-renders the panel itself, so a panel the user expanded
	// stays expanded while new entries stream in — the user's explicit
	// requirement ("if I expanded it, don't fold it; if I folded it, don't
	// expand it"). The "思考 (N)" / "工具 (N)" counts are kept live with a
	// PATCH on the panel header title (expanded is not part of that request).
	// When the card has no cardkit entity (cardID empty) or an append fails,
	// fall back to the full-card update.
	//
	// Plain markdown prefers the streaming-text PUT (typewriter effect) when
	// the card has an entity id.
	if _, isPayload := core.ParseProgressCardPayload(content); !isPayload && handle.cardID != "" {
		if err := p.StreamRichCardText(ctx, handle, content); err == nil {
			return nil
		} else if err != core.ErrNotSupported {
			slog.Debug("feishu: streaming text update failed, falling back to card update", "error", err)
		}
	}
	if payload, isPayload := core.ParseProgressCardPayload(content); isPayload && handle.cardID != "" {
		reasoning, tools, _ := splitProgressItemsByLane(payload.Items)
		c.mu.Lock()
		newThinking := reasoning[c.appendedThinking:]
		newTools := tools[c.appendedTools:]
		c.mu.Unlock()
		appendedOK := true
		if len(newThinking) > 0 {
			if err := c.writeLane(ctx, p, handle, laneThinking, newThinking, len(reasoning), payload.Lang); err != nil {
				slog.Warn("feishu: append thinking entries failed, falling back to card update", "error", err)
				appendedOK = false
			}
		}
		if len(newTools) > 0 && appendedOK {
			if err := c.writeLane(ctx, p, handle, laneTools, newTools, len(tools), payload.Lang); err != nil {
				slog.Warn("feishu: append tool entries failed, falling back to card update", "error", err)
				appendedOK = false
			}
		}
		if appendedOK {
			// Success: panels were not re-rendered; record the new totals so
			// the next update appends only the next delta. Also keep the
			// panel title counts live via PATCH (header title only — the
			// expanded state is untouched).
			if len(newThinking) > 0 {
				if err := p.patchCardElementTitle(ctx, handle, progressPanelThinkingElementID, progressPanelTitle("Reasoning", len(reasoning), payload.Lang)); err != nil {
					slog.Warn("feishu: patch thinking panel title failed (title stays stale)", "error", err)
				}
			}
			if len(newTools) > 0 {
				if err := p.patchCardElementTitle(ctx, handle, progressPanelToolsElementID, progressPanelTitle("Tools", len(tools), payload.Lang)); err != nil {
					slog.Warn("feishu: patch tools panel title failed (title stays stale)", "error", err)
				}
			}
			c.mu.Lock()
			c.appendedThinking = len(reasoning)
			c.appendedTools = len(tools)
			c.mu.Unlock()
			return nil
		}
		// Fall through to full-card sync; after it the whole payload is
		// rendered, so reset the append + panel bookkeeping to the full totals.
		c.mu.Lock()
		c.appendedThinking = len(reasoning)
		c.appendedTools = len(tools)
		c.hasThinkingPanel = len(reasoning) > 0
		c.hasToolsPanel = len(tools) > 0
		c.mu.Unlock()
	}
	cardJSON := cardJSONForContent(content, core.CardStatusWorking)
	if handle.cardID != "" {
		return p.updateCardEntity(ctx, handle, cardJSON)
	}
	return p.UpdateMessage(ctx, handle, cardJSON)
}

// progressLane identifies one collapsible process panel on the card.
type progressLane int

const (
	laneThinking progressLane = iota
	laneTools
)

// elementID returns the fixed cardkit element id of the lane's panel.
func (l progressLane) elementID() string {
	if l == laneTools {
		return progressPanelToolsElementID
	}
	return progressPanelThinkingElementID
}

// titleKey is the English label that progressPanelTitle localizes.
func (l progressLane) titleKey() string {
	if l == laneTools {
		return "Tools"
	}
	return "Reasoning"
}

// laneHasPanel reports whether the card entity currently contains the lane's
// panel.
func (c *feishuStreamingCard) laneHasPanel(l progressLane) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l == laneTools {
		return c.hasToolsPanel
	}
	return c.hasThinkingPanel
}

// setLanePanel records that the lane's panel is (no longer) on the card.
func (c *feishuStreamingCard) setLanePanel(l progressLane, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l == laneTools {
		c.hasToolsPanel = present
		return
	}
	c.hasThinkingPanel = present
}

// laneInsertAnchor picks where to position a lane panel that has to be
// inserted: next to its sibling panel, in the order a full render uses
// (thinking above tools). With no sibling panel yet the element goes at the
// end of the card body (empty target).
func (c *feishuStreamingCard) laneInsertAnchor(l progressLane) (target string, after bool) {
	if l == laneTools {
		if c.laneHasPanel(laneThinking) {
			return progressPanelThinkingElementID, true
		}
		return "", false
	}
	if c.laneHasPanel(laneTools) {
		return progressPanelToolsElementID, false
	}
	return "", false
}

// writeLane streams the new entries of one lane into its collapsible panel.
//
// It appends to the panel when the card already has it. When the panel is
// missing — its lane was still empty when the card was rendered — it INSERTS
// the panel (positioned against the sibling panel, or at the end of the card
// body) filled with these entries. Inserting avoids the full-card re-render
// that a missing panel used to trigger, and a full re-render resets the
// expand/collapse state of every panel the user had touched.
//
// A failed append against an EXISTING panel is still returned to the caller,
// which falls back to a full render: that failure is not a missing element
// (rate limit, transient API error, …) and inserting a second panel would
// render the lane twice.
func (c *feishuStreamingCard) writeLane(ctx context.Context, p streamingCardAPI, handle *feishuPreviewHandle, lane progressLane, entries []core.ProgressCardEntry, total int, lang string) error {
	rendered := renderProgressEntries(entries, lang)
	if c.laneHasPanel(lane) {
		return p.appendCardElements(ctx, handle, lane.elementID(), rendered)
	}

	panel := buildProgressPanel(progressPanelTitle(lane.titleKey(), total, lang), false, lane.elementID(), rendered)
	target, after := c.laneInsertAnchor(lane)
	if err := p.insertCardElements(ctx, handle, target, after, []map[string]any{panel}); err != nil {
		return err
	}
	c.setLanePanel(lane, true)
	slog.Debug("feishu: inserted missing progress panel",
		"element_id", lane.elementID(), "anchor", target, "insert_after", after, "entries", len(entries))
	return nil
}

// renderProgressEntries renders payload entries as card elements for the
// cardkit append API.
func renderProgressEntries(items []core.ProgressCardEntry, lang string) []map[string]any {
	elements := make([]map[string]any, 0, len(items))
	for _, item := range items {
		elements = append(elements, renderProgressEntryElement(item, lang))
	}
	return elements
}

// Finalize sends the final content and marks the card complete.
func (c *feishuStreamingCard) Finalize(ctx context.Context, content string) error {
	c.mu.Lock()
	if c.failed {
		c.mu.Unlock()
		return nil
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	handle := c.handle
	c.mu.Unlock()

	if handle == nil {
		// No intermediate content ever arrived; create the card now with the
		// final content directly.
		return c.send(ctx, content)
	}

	// Full-card update with the final content; the engine already composed
	// the rich-card markdown (thinking + tools + answer). Prefer the cardkit
	// entity update when available, else the patch API.
	cardJSON := cardJSONForContent(content, core.CardStatusDone)
	var err error
	if handle.cardID != "" {
		err = c.platform.updateCardEntity(ctx, handle, cardJSON)
	} else {
		err = c.platform.UpdateMessage(ctx, handle, cardJSON)
	}
	c.finish(err)
	return err
}

// finish marks the card terminal and closes the done channel.
func (c *feishuStreamingCard) finish(err error) {
	c.mu.Lock()
	if err != nil {
		c.failed = true
	} else {
		c.lastUpdate = time.Now()
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	c.mu.Unlock()
}

// Failed returns true once the card has entered a failed state.
func (c *feishuStreamingCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}
