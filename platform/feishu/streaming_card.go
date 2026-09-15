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
			if err := p.appendCardElements(ctx, handle, progressPanelThinkingElementID, renderProgressEntries(newThinking, payload.Lang)); err != nil {
				slog.Warn("feishu: append thinking entries failed, falling back to card update", "error", err)
				appendedOK = false
			}
		}
		if len(newTools) > 0 && appendedOK {
			if err := p.appendCardElements(ctx, handle, progressPanelToolsElementID, renderProgressEntries(newTools, payload.Lang)); err != nil {
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
		// rendered, so reset the append bookkeeping to the full totals.
		c.mu.Lock()
		c.appendedThinking = len(reasoning)
		c.appendedTools = len(tools)
		c.mu.Unlock()
	}
	cardJSON := cardJSONForContent(content, core.CardStatusWorking)
	if handle.cardID != "" {
		return p.updateCardEntity(ctx, handle, cardJSON)
	}
	return p.UpdateMessage(ctx, handle, cardJSON)
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
