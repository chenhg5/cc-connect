package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// StreamPreviewCfg controls the streaming preview behavior.
type StreamPreviewCfg struct {
	Enabled           bool     // global toggle
	DisabledPlatforms []string // platforms where streaming preview is disabled (e.g. "feishu")
	IntervalMs        int      // minimum ms between updates (default 1500)
	MinDeltaChars     int      // minimum new chars before sending an update (default 30)
	MaxChars          int      // max preview length (default 2000)
}

// DefaultStreamPreviewCfg returns sensible defaults.
func DefaultStreamPreviewCfg() StreamPreviewCfg {
	return StreamPreviewCfg{
		Enabled:           true,
		DisabledPlatforms: nil,
		IntervalMs:        1500,
		MinDeltaChars:     30,
		MaxChars:          2000,
	}
}

// streamPreview manages the state and throttling of a single streaming preview.
// It accumulates text from EventText events and periodically pushes
// updates to the platform via MessageUpdater.UpdateMessage.
type streamPreview struct {
	mu sync.Mutex

	cfg       StreamPreviewCfg
	platform  Platform
	replyCtx  any
	ctx       context.Context
	transform func(string) string

	fullText          string // accumulated full text so far
	lastSentText      string // what was last successfully sent to the platform
	lastSentAt        time.Time
	lastSentViaUpdate bool // true if lastSentText was delivered via UpdateMessage (not SendPreviewStart)
	previewMsgID      any  // platform-specific ID for the preview message (returned by SendPreviewStart)
	degraded          bool // if true, stop trying (platform doesn't support it or permanent error)

	timer     *time.Timer
	timerStop chan struct{} // closed when preview ends
}

// PreviewStarter is an optional interface for platforms that can initiate a
// streaming preview message and return a handle for subsequent updates.
type PreviewStarter interface {
	// SendPreviewStart sends the initial preview message and returns a handle
	// that can be passed to UpdateMessage for edits. Returns nil handle if
	// preview is not supported for this context.
	SendPreviewStart(ctx context.Context, replyCtx any, content string) (previewHandle any, err error)
}

// PreviewCleaner is an optional interface for platforms that need to clean up
// the preview message after the final response is sent (e.g. Discord deletes
// the preview and sends a fresh message).
type PreviewCleaner interface {
	DeletePreviewMessage(ctx context.Context, previewHandle any) error
}

// PreviewFinishPreference is an optional interface for platforms that want to
// keep the preview message as the final delivered message on normal completion.
type PreviewFinishPreference interface {
	KeepPreviewOnFinish() bool
}

func newStreamPreview(cfg StreamPreviewCfg, p Platform, replyCtx any, ctx context.Context, transform func(string) string) *streamPreview {
	return &streamPreview{
		cfg:       cfg,
		platform:  p,
		replyCtx:  replyCtx,
		ctx:       ctx,
		transform: transform,
		timerStop: make(chan struct{}),
	}
}

// canPreview returns true if the platform supports message updating and is not disabled.
func (sp *streamPreview) canPreview() bool {
	sp.mu.Lock()
	degraded := sp.degraded
	sp.mu.Unlock()
	if degraded || !sp.cfg.Enabled {
		return false
	}
	// Check if platform is in disabled list
	platformName := sp.platform.Name()
	for _, disabled := range sp.cfg.DisabledPlatforms {
		if strings.EqualFold(disabled, platformName) {
			return false
		}
	}
	_, ok := sp.platform.(MessageUpdater)
	return ok
}

// appendText adds new text content and triggers a throttled flush if needed.
func (sp *streamPreview) appendText(text string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if sp.degraded || !sp.cfg.Enabled {
		return
	}

	sp.fullText += text

	displayText := sp.fullText
	maxChars := sp.cfg.MaxChars
	if maxChars > 0 && len([]rune(displayText)) > maxChars {
		displayText = string([]rune(displayText)[:maxChars]) + "…"
	}

	delta := len([]rune(displayText)) - len([]rune(sp.lastSentText))
	elapsed := time.Since(sp.lastSentAt)
	interval := time.Duration(sp.cfg.IntervalMs) * time.Millisecond

	if delta < sp.cfg.MinDeltaChars && !sp.lastSentAt.IsZero() {
		sp.scheduleFlushLocked(interval)
		return
	}

	if elapsed < interval && !sp.lastSentAt.IsZero() {
		remaining := interval - elapsed
		sp.scheduleFlushLocked(remaining)
		return
	}

	sp.cancelTimerLocked()
	sp.flushLocked(displayText)
}

func (sp *streamPreview) scheduleFlushLocked(delay time.Duration) {
	if sp.timer != nil {
		return // already scheduled
	}
	sp.timer = time.AfterFunc(delay, func() {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		sp.timer = nil
		if sp.degraded {
			return
		}
		displayText := sp.fullText
		maxChars := sp.cfg.MaxChars
		if maxChars > 0 && len([]rune(displayText)) > maxChars {
			displayText = string([]rune(displayText)[:maxChars]) + "…"
		}
		sp.flushLocked(displayText)
	})
}

func (sp *streamPreview) cancelTimerLocked() {
	if sp.timer != nil {
		sp.timer.Stop()
		sp.timer = nil
	}
}

// flushLocked sends the current preview text to the platform. Must hold sp.mu.
func (sp *streamPreview) flushLocked(text string) {
	if sp.transform != nil {
		text = sp.transform(text)
	}
	if text == sp.lastSentText || text == "" {
		return
	}

	updater, ok := sp.platform.(MessageUpdater)
	if !ok {
		slog.Debug("stream preview: platform does not support UpdateMessage, degrading")
		sp.degraded = true
		return
	}

	if sp.previewMsgID == nil {
		// First preview: try to send a new preview message
		if starter, ok := sp.platform.(PreviewStarter); ok {
			slog.Debug("stream preview: sending first preview via SendPreviewStart", "text_len", len(text))
			handle, err := starter.SendPreviewStart(sp.ctx, sp.replyCtx, text)
			if err != nil {
				slog.Debug("stream preview: start failed, degrading", "error", err)
				sp.degraded = true
				return
			}
			sp.previewMsgID = handle
		} else {
			if err := sp.platform.Send(sp.ctx, sp.replyCtx, text); err != nil {
				slog.Debug("stream preview: initial send failed", "error", err)
				sp.degraded = true
				return
			}
			sp.previewMsgID = sp.replyCtx
		}
		sp.lastSentText = text
		sp.lastSentViaUpdate = false
		sp.lastSentAt = time.Now()
		return
	}

	// Update existing preview message
	slog.Debug("stream preview: updating via UpdateMessage", "text_len", len(text))
	if err := updater.UpdateMessage(sp.ctx, sp.previewMsgID, text); err != nil {
		slog.Debug("stream preview: update failed, degrading", "error", err)
		sp.degraded = true
		return
	}
	sp.lastSentText = text
	sp.lastSentViaUpdate = true
	sp.lastSentAt = time.Now()
}

// freeze stops the streaming preview permanently: cancels pending timers,
// updates the preview message in-place with the accumulated text, and marks
// the preview as degraded so no further updates are sent.
// Call this when a permission prompt or other interruption occurs.
func (sp *streamPreview) freeze() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.cancelTimerLocked()

	if sp.previewMsgID != nil && !sp.degraded {
		if updater, ok := sp.platform.(MessageUpdater); ok {
			text := sp.fullText
			maxChars := sp.cfg.MaxChars
			if maxChars > 0 && len([]rune(text)) > maxChars {
				text = string([]rune(text)[:maxChars]) + "…"
			}
			if text != "" {
				if sp.transform != nil {
					text = sp.transform(text)
				}
				_ = updater.UpdateMessage(sp.ctx, sp.previewMsgID, text)
			}
		}
	}

	sp.degraded = true
}

// discard removes the preview message when possible and disables further
// preview updates. Call this when the caller intends to send a separate
// non-preview message (for example after tool use or on terminal errors).
func (sp *streamPreview) discard() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.cancelTimerLocked()

	select {
	case <-sp.timerStop:
	default:
		close(sp.timerStop)
	}

	if sp.previewMsgID != nil {
		if cleaner, ok := sp.platform.(PreviewCleaner); ok {
			slog.Debug("stream preview discard: deleting preview")
			_ = cleaner.DeletePreviewMessage(sp.ctx, sp.previewMsgID)
		}
	}

	sp.previewMsgID = nil
	sp.degraded = true
}

// finish is called when the agent response is complete. It cancels any pending
// timer and optionally cleans up the preview message.
// Returns true if a preview was active and the final message was sent via preview
// (so the caller should skip sending the full response separately).
func (sp *streamPreview) finish(finalText string) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	sp.cancelTimerLocked()

	select {
	case <-sp.timerStop:
	default:
		close(sp.timerStop)
	}

	if sp.transform != nil {
		finalText = sp.transform(finalText)
	}
	if sp.previewMsgID == nil || sp.degraded {
		if sp.previewMsgID != nil && sp.degraded {
			if cleaner, ok := sp.platform.(PreviewCleaner); ok {
				slog.Debug("stream preview finish: deleting stale preview (degraded)")
				_ = cleaner.DeletePreviewMessage(sp.ctx, sp.previewMsgID)
			}
		}
		slog.Debug("stream preview finish: no active preview", "hasHandle", sp.previewMsgID != nil, "degraded", sp.degraded)
		return false
	}

	keepPreview := false
	if pref, ok := sp.platform.(PreviewFinishPreference); ok {
		keepPreview = pref.KeepPreviewOnFinish()
	}

	// If platform wants to delete the preview and send fresh, let it.
	if cleaner, ok := sp.platform.(PreviewCleaner); ok && !keepPreview {
		slog.Debug("stream preview finish: deleting preview (PreviewCleaner)")
		_ = cleaner.DeletePreviewMessage(sp.ctx, sp.previewMsgID)
		return false
	}

	updater, ok := sp.platform.(MessageUpdater)
	if !ok {
		slog.Debug("stream preview finish: no MessageUpdater")
		return false
	}

	if finalText == "" {
		slog.Debug("stream preview finish: empty final text")
		return false
	}

	// If the final text is identical to what was last sent via UpdateMessage,
	// skip the redundant API call. This prevents duplicate messages on platforms
	// (e.g. Feishu) where patching with identical content may fail.
	// Only skip when lastSentViaUpdate is true — if the text was only sent via
	// SendPreviewStart (first flush), we must still call UpdateMessage because
	// it may apply different formatting (e.g. Markdown→HTML for Telegram).
	if finalText == sp.lastSentText && sp.lastSentViaUpdate {
		slog.Debug("stream preview finish: text unchanged since last UpdateMessage, skipping",
			"text_len", len(finalText))
		return true
	}

	// Try to update the preview in-place with the full final text.
	// maxChars only throttles intermediate streaming updates; at finish time
	// we always attempt a single final update regardless of length.
	slog.Debug("stream preview finish: sending final UpdateMessage",
		"text_len", len(finalText), "lastSent_len", len(sp.lastSentText),
		"same", finalText == sp.lastSentText, "viaUpdate", sp.lastSentViaUpdate)
	if err := updater.UpdateMessage(sp.ctx, sp.previewMsgID, finalText); err != nil {
		slog.Debug("stream preview finish: final update FAILED, cleaning up preview", "error", err)
		// Update failed (e.g. text too long for platform edit API).
		// Try to delete the stale preview so caller can send a fresh message.
		if cleaner, ok := sp.platform.(PreviewCleaner); ok {
			_ = cleaner.DeletePreviewMessage(sp.ctx, sp.previewMsgID)
		}
		return false
	}
	slog.Debug("stream preview finish: success via UpdateMessage")
	return true
}

// detachPreview clears the preview message handle so that finish() won't
// delete it. Call this after freeze() when the frozen preview should remain
// visible as a permanent message (e.g. text before the first tool call).
func (sp *streamPreview) detachPreview() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.previewMsgID = nil
}
